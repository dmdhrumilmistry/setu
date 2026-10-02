# setu

**Share your terminal peer-to-peer, from anywhere, without running a server.**
Built for driving Claude Code and other AI agents on your machine from your
phone or another computer.

```
$ setu share --password -- claude

CONTROL link — anyone holding it can type into this terminal. Keep it secret:
  browser:  https://dmdhrumilmistry.github.io/setu/#k=Pqv4WrBl…
  terminal: setu join 'k=Pqv4WrBl…'
  ▄▄▄▄▄▄▄ ▄ ▄▄ ▄▄▄▄▄▄▄
  █ ▄▄▄ █ ▀█▄█ █ ▄▄▄ █      ← scan with your phone
  …
```

Open the link or scan the QR code and you are in the same terminal session.

* **No central server.** The terminal stream is a direct WebRTC connection.
  Peers find each other through public [nostr](https://nostr.com) relays,
  which only ever see encrypted messages, and Google's STUN servers. You can
  also use `--manual` to copy and paste the connection details with no relays
  at all.
* **Browser or CLI.** There is a static web client hosted on GitHub Pages
  (xterm.js, a mobile key bar, a compose box for long prompts), and
  `setu join` for terminals.
* **Security-first.** The link is a 256-bit key that is never sent to any
  server. setu adds end-to-end DTLS, a verification code, and optional
  passwords, view-only links, single-use links, expiry and approval
  prompts. See [SECURITY.md](SECURITY.md).

## Install

```sh
go install github.com/dmdhrumilmistry/setu/cmd/setu@latest   # Go 1.26+
```

Release binaries for Linux, macOS and Windows are on the
[releases page](https://github.com/dmdhrumilmistry/setu/releases).
Sharing (hosting) works on Linux and macOS (use WSL on Windows). Joining
works everywhere.

## Usage

### Share

```sh
setu share                                 # share $SHELL
setu share -- claude                       # share Claude Code
setu share --password --once -- claude     # password + single-use link
setu share --view-link -- claude           # also print a read-only link for spectators
setu share --approve -- bash               # confirm each joiner on this terminal (y/N)
setu share --expire 30m --max-clients 1    # stop accepting new joins after 30 minutes
setu share --headless --link-file ~/.setu-link -- claude   # no local attach (e.g. under tmux/nohup)
setu share --manual                        # no relays: copy/paste offer & answer
```

By default the host terminal is attached to the session too: you see what the
remote side does and can type alongside it. Type **`<Enter> ~ .`** on the host
to end the share.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--password` | off | Prompt for a session password (or set `SETU_PASSWORD`). |
| `--view-link` | off | Also create a read-only invite. |
| `--once` | off | The invite stops working after the first successful join. |
| `--expire` | `0` | Stop accepting new clients after this duration. |
| `--max-clients` | `2` | Maximum concurrent clients. |
| `--approve` | off | Ask y/N on the host for every joiner. |
| `--manual` | off | Copy/paste signaling instead of nostr. |
| `--relay` | 4 public relays | Nostr relay URL(s) to use. |
| `--stun` | Google STUN | STUN server URL(s). |
| `--turn`, `--turn-user` | — | TURN relay for strict NATs (credential in `SETU_TURN_PASSWORD`). |
| `--web-url` | GitHub Pages | Base URL of the web client (set your own if you self-host). |
| `--headless` | off | Don't attach the local terminal. |
| `--qr` | on | Print a QR code of the browser link. |
| `--link-file` | — | Write invite link(s) to a 0600 file. |

### Join

* **Browser:** open the link. On a phone, use the key bar for Esc / Tab /
  ⇧Tab / Ctrl / arrows, and ✎ to compose a multi-line prompt. Multi-line text
  is sent as a bracketed paste, so Claude Code treats it as one prompt.
* **Terminal:** `setu join '<link or k=…>'`. Type `<Enter> ~ .` to detach.
* **Manual mode:**
  1. Open the offer link, or run `setu join <offer-code>`.
  2. Send the answer code it prints back to the host.
  3. The host pastes that code into `setu share --manual`.

Check that the **verification code** shown on both sides is the same.

## How it works

```
 setu share ──(encrypted hello/offer/answer via nostr relays)── browser / setu join
      │                                                              │
      └──────────── WebRTC data channel (DTLS, peer-to-peer) ────────┘
```

1. `setu share` starts your command in a PTY and creates a random secret. The
   secret is split, using HKDF, into a public room id and an AES-GCM key.
2. The joiner publishes an encrypted *hello* to the room. The host replies with
   an encrypted WebRTC offer, and the joiner sends back an encrypted answer.
   These are all ephemeral nostr events.
3. The peers connect directly, using STUN for NAT traversal. Over the data
   channel the host sends a challenge. Once the password is verified (if one is
   set), the terminal stream starts with a replay of recent output.

The full protocol is described in [docs/DESIGN.md](docs/DESIGN.md).

## Hosting the web client

The `pages` workflow publishes `web/` to GitHub Pages. To turn it on, go to
**Settings → Pages → Source** and choose **GitHub Actions**. If you use a
fork, or self-host the page anywhere that serves static files over HTTPS, pass
`--web-url https://you.example/setu/` to `setu share`.

## Development

```sh
go test -race ./...                       # unit + e2e tests (real PTY + WebRTC + in-memory relay)
node --test web/test/crypto.test.mjs      # JS crypto parity with Go test vectors
go build -o setu ./cmd/setu && SETU_LOCAL_RELAY=1 node web/test/browser-e2e.mjs   # headless Chromium e2e
web/vendor/update.sh                      # re-vendor xterm.js / noble-curves
```
