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
