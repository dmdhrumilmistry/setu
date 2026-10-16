# setu — design and implementation plan

*setu* (सेतु, "bridge") shares a local terminal with a browser or another
terminal, peer-to-peer over WebRTC, with **no server you have to run**. Its
main job is driving Claude Code and other AI agents on your machine from
anywhere, such as your phone.

## Goals

| Goal | How |
| --- | --- |
| No central server | WebRTC data channel straight between the peers. Signaling goes over public **nostr relays** (a decentralised network with many independent operators) or over **copy/paste**. Google STUN only helps the peers find a network path. |
| Works from a phone | Static web client on **GitHub Pages**, with a QR code, a mobile key bar (Esc, Tab, ⇧Tab, Ctrl, arrows) and a compose box for long prompts. |
| CLI on both ends | `setu share` (host) and `setu join` (terminal client). Both are one static Go binary. |
| Secure by default | The link is a 256-bit key, relays only see ciphertext, DTLS runs end to end, and there are optional passwords, view-only links, single-use links, expiry and host approval. See [SECURITY.md](../SECURITY.md). |

## Why Go?

Go has [pion/webrtc](https://github.com/pion/webrtc), the most mature WebRTC
stack outside browsers, and [creack/pty](https://github.com/creack/pty). It also
cross-compiles to a single static binary with `CGO_ENABLED=0`, and it has
HKDF, PBKDF2, AES-GCM and HMAC in its standard library. The Rust options
(`webrtc-rs`) are less mature for this use.

## Architecture

```
  ┌────────────── host machine ───────────────┐                 ┌──── phone / laptop ────┐
  │ claude / $SHELL ⇄ PTY ⇄ setu share         │                 │ browser (GitHub Pages) │
  │                       │  ▲                 │                 │   or `setu join`       │
  │        local mirror ◀─┘  │ pion/webrtc     │                 │                        │
  └──────────────────────────┼─────────────────┘                 └───────────┬────────────┘
                             │  ① encrypted hello / offer / answer          │
                             └──────▶  public nostr relays  ◀────────────────┘
                                       (ephemeral events, ciphertext only)
                             │  ② STUN binding (stun.l.google.com)          │
                             │  ③ DTLS/SCTP data channel, peer-to-peer ═════│
```

### Packages

| Path | Responsibility |
| --- | --- |
| `cmd/setu` | CLI: flags, prompts, printing invites and QR codes |
| `internal/host` | PTY session (creack/pty on Unix, ConPTY on Windows), output fan-out with a 256 KiB replay buffer, admission control, per-peer handshake, nostr and manual signaling |
| `internal/client` | `Dial` (signaling, WebRTC, handshake) and the raw-mode terminal attach for `setu join` |
| `internal/secure` | Invite secrets, HKDF room derivation, AES-GCM sealing, SAS, PBKDF2/HMAC password proof |
| `internal/nostr` | A minimal NIP-01 client: BIP-340 signed events and a relay pool with reconnects |
| `internal/rtc` | pion helpers: non-trickle offer/answer, ICE config validation |
| `internal/link` | Invite links (secrets in the `#fragment`) and deflate+base64url copy/paste codes |
| `internal/proto` | Wire messages shared with `web/js/*.mjs` |
| `internal/audit` | OpenTelemetry audit log: rebuilds commands from keystrokes, redacts secrets, exports OTLP/HTTP log records through a bounded batch queue |
| `internal/service` | The `setu service` supervisor: persistent profiles (keys, password verifiers), hot-reloaded config, restart with backoff, and the systemd / launchd / Task Scheduler autostart entries |
| `internal/daemon` | Detached `setu share -d` processes and their registry |
| `internal/desktop` | `setu desktop`: screen capture and input injection (GDI and `SendInput` on Windows, CoreGraphics and NSPasteboard through purego on macOS, X11 with MIT-SHM and XTEST on Linux and the BSDs; no cgo), clipboard, and the hub that streams changed tiles to every viewer with per-viewer flow control |
| `web/` | The static client: `crypto.mjs` mirrors `secure`/`link`, `nostr.mjs` mirrors `nostr`, `app.mjs` handles the UI, WebRTC and xterm.js |

## Protocol

### Invite

`https://<pages>/#k=<secret>[&r=<relays>]`

The secret is 32 random bytes in base64url. It is derived into two values:

* `room_id = HKDF-SHA256(secret, info="setu/v1/room-id")`: a public rendezvous
  tag that looks like a nostr pubkey.
* `key = HKDF-SHA256(secret, info="setu/v1/signal-key")`: the AES-256-GCM key
  for every signaling message.

A `--view-link` creates a second, independent secret. The host decides each
client's role from which room the request arrived on, never from anything the
client claims.

### Signaling over nostr (default)

Every message is an **ephemeral** event (kind `21210`, which relays forward but
never store). Each one is signed by a throwaway keypair, carries a `p` tag
naming the recipient, and has this content:
`base64(nonce ‖ AES-GCM(key, json, aad="setu/v1/signal|room|from|to"))`.

```
client ──hello{sid,ts}──────────────▶ p=room_id         (retransmitted every 2 s)
client ◀─offer{sid,sdp,ice,ts}─────── p=client_pubkey   (host owns the ICE config)
client ──answer{sid,sdp,ts}─────────▶ p=room_id         (sent twice)
```

The host drops any message that:

* fails decryption,
* is more than ±2 minutes old,
* repeats a `(pubkey, sid)` pair it has already seen (replay),
* arrives faster than the hello rate limit allows,
* is an answer from a pubkey other than the one that sent the hello.

### Manual signaling (`--manual`)

The offer is `base64url(deflate-raw(json))` inside a link (`#o=<code>`). The
joiner turns it into an answer code, and the host user pastes that code back.
Only STUN is involved, and pasting the answer is itself the approval.

### Data channel (`"setu"`, reliable and ordered)

```
host  → {t:"hello", v:1, auth:"none"|"password", salt, iter, nonce, role}
client→ {t:"auth", proof?}      proof = HMAC(PBKDF2(pw,salt,iter), "setu/v1/auth\n"‖nonce‖fpA‖fpB)
host  → {t:"ready", role, cmd, host, cols, rows}   then the replay buffer
host  → binary PTY output        client → binary keystrokes (dropped for role=view)
client→ {t:"resize", cols, rows} (control only)
host  → {t:"exit", code} | {t:"error", msg}
```

`fpA` and `fpB` are the DTLS fingerprints from both SDPs. They feed the proof
and the 6-digit **SAS** that both sides display.

### Reconnecting

Hosts that support it add `resume` (a random 32-byte token) and `pos` to
`ready`. `pos` is the position, in bytes since the command started, of the
first output byte that follows. The client counts output bytes from there. When
the connection drops (ICE `failed`, the channel closes, or no message for 15 s
while the page is visible), the client signals again with a new nostr key and
`sid`, plus `resume: ResumeID(token)` in the encrypted hello. It then answers
the host's `hello` with `{t:"auth", resume, pos}` in place of a password proof:

```
client ──hello{sid,ts,resume:id}──▶   host admits it past --max-clients, --once/--expire and the lockout,
                                      but it must then present the token
host  → {t:"hello", …}
client→ {t:"auth", resume:token, pos, device_key, device_sig}
host  → {t:"ready", resume:newToken, pos, resumed:true}   then only the output after pos
```

The host stores `SHA-256("setu/v1/resume-hash\n" ‖ token)` and checks it in
constant time. The token is bound to the room (role and personal key) and to
the device key if one was proven. It is replaced by every `ready`, and it
expires `resumeGrace` (10 minutes) after its connection is lost. If the old
connection is still open (the host has not noticed the drop yet), it is closed.
If `pos` has already left the 256 KiB replay buffer, `resumed` is false and the
client clears its screen before the full replay. An invalid token gets
`{t:"error", msg: proto.ErrResume}`, and the client then joins afresh. The
client retries with full-jitter exponential backoff (1 s doubling to 30 s) and
gives up when the grace period ends. Clients ping every 5 s (`{t:"ping"}` →
`{t:"pong"}`), and the host drops a pinging client that stays silent for 90 s.

The web client sends `{t:"visibility", hidden}` whenever its page moves to or
from the background. A hidden client is never dropped for silence: browsers
throttle or freeze background timers, and ICE consent checks still catch a dead
path. If its connection drops, its ticket lasts `backgroundGrace` (1 hour). The
page does not retry while hidden; on return it retries with a fresh window.
To survive the browser discarding and reloading the tab, the client keeps
`{invite, token, at}` in `sessionStorage` (`web/js/keep.mjs`). That storage is
tab-scoped and gone when the tab closes, expires an hour after the page was
last active, and is cleared on exit, error, give-up and **Leave**. A restored
page resumes without `pos`, so the host replays its whole buffer onto the new
terminal.

## Remote desktop

`setu desktop` reuses signaling, the handshake and admission unchanged. The
host opens a second reliable, ordered channel, `"setu-screen"`, used only
host to client, so a large frame never delays control messages. `ready`
carries `kind: "desktop"` and the host's `os` (GOOS); `setu join` refuses such
shares. When exactly one side is a Mac, the web client swaps Ctrl and Cmd
(`web/js/keymap.mjs`), so shortcuts keep the viewer's habits.

```
host  → {t:"screen", displays:[{name,w,h,primary}], display}   on join and on every layout change
host  → binary on "setu-screen": tiles, then a frame end
client→ {t:"ack", frame}                       after drawing that frame
client→ {t:"pointer", x, y, buttons}           control only; buttons = PointerEvent.buttons
client→ {t:"wheel", dx, dy} | {t:"key", key: KeyboardEvent.code, down} | {t:"text", text}
both  → {t:"clipboard", text, more}            control only; parts of at most 8192 characters, 1 MiB in total
client→ {t:"clipboard-get"} | {t:"display", display} | {t:"refresh"} | {t:"quality", quality}
```

Binary messages (little endian) are at most 60 KiB, under every WebRTC
implementation's message size limit:

```
tile       [1, enc (1 JPEG, 2 PNG), x u16, y u16, w u16, h u16, image bytes]
frame end  [2, id u32, cursorX i32, cursorY i32, cursorVisible u8]
```

The hub captures the shared display at most `--fps` times a second, and only
while some viewer has fewer than two unacknowledged frames, so idle viewers
and slow links cost nothing and never build a queue. Each capture is compared
with the previous one in 64x64 tiles; every viewer accumulates the tiles that
changed since its last frame, and they are sent as horizontal runs of up to 8
tiles. A run with at most 256 colours is encoded as a paletted PNG (crisp
text), anything else as JPEG at the viewer's quality; runs whose encoding
would exceed the message limit are split. Distinct runs are encoded once per
capture, in parallel, and shared by every viewer that needs them. Cursor
position rides in the frame end, so pointer movement alone produces a frame
without tiles.

Input is applied to the physical key (`KeyboardEvent.code` mapped to PC
scancodes on Windows, `kVK_*` virtual keycodes on macOS and evdev keycodes on
X11), so the host's own layout
decides the character. `text` uses Unicode injection on Windows and a briefly
remapped spare keycode on X11 for characters the layout lacks. Keys and
buttons a viewer holds are released when it leaves. The host's clipboard is
polled for changes every 250 ms (`GetClipboardSequenceNumber`, the
NSPasteboard change count, XFixes selection events) and sent to control
viewers; text a viewer set is not echoed back. Text is LF on the wire; the
Windows backend converts to and from CRLF. A copy shortcut in the browser
starts a clipboard write at once with a promise for the text, resolved when
the host clipboard changes (or with `clipboard-get` if it does not), because
browsers only allow clipboard writes during a user action.

## Audit log

The host hands every keystroke it writes to the PTY to a per-client
`audit.Input`. That input is attached in the same critical section that
marks the client ready, so nothing reaches the PTY unaudited. A small line
editor model applies backspace, ^U, ^W and bracketed paste, and emits a
`setu.command` record on Enter. On Unix the host reads the PTY's termios
through the master: canonical mode with echo off means a password prompt, and
that line is recorded as a marker. Line editors and TUIs turn canonical mode
off, so their input is still recorded. Records then pass through regex
redaction and an OpenTelemetry `BatchProcessor` (queue 8192, 1 s interval,
gzip OTLP/HTTP). Export failures are reported at most once a minute.

## Service

`setu service run` is a single supervisor per state directory, guarded by a
pid lock file. Every 2 s it checks `config.json` for changes, and it
reconciles at least every 30 s:

1. If the audit settings changed (or failed last time), it stops every
   session, rebuilds the audit pipeline and fails closed if that does not
   work.
2. It computes the desired set (enabled, unexpired, valid profiles), keyed by
   a hash of each profile.
3. It stops the sessions that are no longer desired or whose hash changed,
   and starts the missing ones.

Each profile runs `host.Session` in-process, in its own goroutine. When the
command exits, the supervisor restarts it with exponential backoff (1 s up to
1 min, reset after a minute of uptime). A context deadline at `expires_at`
stops serving the moment the keys expire.

## Implementation steps

1. Crypto primitives with Go↔JS test vectors (`secure`, `web/js/crypto.mjs`).
2. A minimal nostr client with BIP-340 signing, plus an in-memory test relay.
3. A pion wrapper and non-trickle offer/answer.
4. The host: PTY, fan-out, replay, resize policy (the latest control client
   wins), admission control (max clients, pending limit, lockout, `--once`,
   `--expire`, `--approve`), and a local mirror with the `~.` escape.
5. The client `Dial` and attach, then the CLI.
6. The web client: a vendored xterm.js and noble-curves, strict CSP, a mobile
   key bar and a compose box.
7. Tests:
   * unit tests,
   * a Go e2e test (real PTY and WebRTC over the in-memory relay),
   * a JS parity test,
   * a Playwright browser e2e test.
8. CI, the GitHub Pages deploy and cross-platform releases.

## Future work

* Optional TOTP as a second factor for long-lived links.
* Session recording (asciicast) for auditing what agents did.
* Trickle ICE, for faster connects on networks where STUN is slow.
