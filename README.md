<p align="center">
  <img src="docs/assets/logo.svg" width="96" height="96" alt="setu logo: a bridge">
</p>

<h1 align="center">setu</h1>

<p align="center">
  <strong>Your terminal, on any device. No server in the middle.</strong><br>
  Drive Claude Code (or any shell) on your computer from your phone, peer-to-peer and end-to-end encrypted.
</p>

<p align="center">
  <a href="https://github.com/dmdhrumilmistry/setu/actions/workflows/ci.yml"><img src="https://github.com/dmdhrumilmistry/setu/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/dmdhrumilmistry/setu/releases"><img src="https://img.shields.io/github/v/release/dmdhrumilmistry/setu?sort=semver" alt="Latest release"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/dmdhrumilmistry/setu" alt="Go version">
  <img src="https://img.shields.io/badge/platforms-Linux%20%7C%20macOS%20%7C%20Windows-0b1220" alt="Linux, macOS and Windows">
  <a href="https://dmdhrumilmistry.github.io/setu/docs/"><img src="https://img.shields.io/badge/docs-read-38bdf8" alt="Documentation"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#always-on-access-from-your-phone">Always-on access</a> ·
  <a href="#secure-by-default">Security</a> ·
  <a href="https://dmdhrumilmistry.github.io/setu/docs/">Documentation</a>
</p>

---

*Setu* (सेतु) means *bridge*. It connects a browser or another terminal
straight to a terminal on your computer, so you can start a long Claude Code
task at your desk and keep steering it from the couch, the train or another
machine.

```
$ setu share -- claude

CONTROL link - anyone holding it can type into this terminal. Keep it secret:
  browser:  https://dmdhrumilmistry.github.io/setu/#k=Pqv4WrBl…
  ▄▄▄▄▄▄▄ ▄ ▄▄ ▄▄▄▄▄▄▄
  █ ▄▄▄ █ ▀█▄█ █ ▄▄▄ █      ← scan with your phone
  …
```

## Why setu

|  |  |
| --- | --- |
| 🔒 **Private by design** | Your keystrokes travel over a direct, DTLS-encrypted WebRTC connection. The public relays that introduce the two ends only ever see ciphertext. There is no setu server and no account. |
| 📱 **Made for phones** | Open the link or scan the QR code: a full terminal in the browser, with a key bar (Esc, Tab, Ctrl, arrows), a compose box for long prompts and touch scrolling. Nothing to install on the phone. |
| 🔁 **Survives real life** | Switching between Wi-Fi and mobile data, locking the phone or reloading the tab resumes the same session, without the password again. |
| 🗝️ **Devices, not passwords** | Each browser has its own non-extractable device key. Pair a device once and it gets in by itself from then on; a leaked link alone is useless. |
| ♾️ **Always on** | `setu service` keeps your links working across reboots, starts at login and keeps itself up to date. |
| 🖥️ **Remote desktop too** | `setu desktop` shares the whole screen, mouse, keyboard and clipboard in the browser. |
| 🏢 **Team and fleet ready** | Personal links per person, an OpenTelemetry audit log of who typed what, and MDM policy for Intune, Jamf, Kandji or Group Policy. |

## Quick start

**1. Install** (Linux, macOS or Windows)

```sh
go install github.com/dmdhrumilmistry/setu/cmd/setu@latest
```

Or download a binary from the [releases page](https://github.com/dmdhrumilmistry/setu/releases).
setu updates itself afterwards (`setu upgrade`).

**2. Share**

```sh
setu share --password -- claude      # or any command; no command shares your shell
```

**3. Join.** Scan the QR code with your phone (or open the link anywhere), type
the password once, and check that the **verification code** matches on both
screens. That's it.

Type `<Enter> ~ .` on your computer to end the share, or use `setu share -d …`
to run it in the background and get your prompt back (`setu ps`, `setu stop`).

## Always-on access from your phone

Want your computer reachable whenever it is on, without starting anything?

```sh
setu service add claude -- claude
```

This one command creates a permanent link, starts the service, sets it to start
at login, and prints a **pairing QR code**. Scan it with your phone: the phone
is now paired and joins with a tap from then on. To add another device later:

```sh
setu service pair claude              # a one-time code / QR for one more device
setu service device list claude       # which devices can get in
setu service device remove claude <fingerprint>   # cut one off immediately
setu doctor                           # check everything; each problem comes with its fix
```

Your computer shows a notification every time someone connects, so access you
did not expect does not go unnoticed.

## Secure by default

| Default | What it means for you |
| --- | --- |
| The link *is* the key | A 256-bit secret after `#`, which browsers never send to any server. |
| Paired devices only (service) | A new device needs a single-use, 15-minute pairing code; the link alone gets nobody in. |
| Password once per device | With `--password`, each device is asked once and then recognised by its device key. |
| Verification code | Both ends show the same 6-digit code, so a man in the middle would show up. |
| Lockout | 5 wrong passwords or codes lock new joins for 10 minutes; 20 disable the link. |
| Expiry | Service links expire after 90 days and unused devices after 90 days. |
| Alerts | A desktop notification for every connection and pairing. |
| Safe updates | Updates are checksum-verified, test-run, swapped in atomically, and applied only when nobody is connected. |
| Nothing third party in the page | All browser code is vendored and pinned; you can self-host it. |

Read [SECURITY.md](SECURITY.md) for the full threat model.

## What can I do with it?

```sh
setu share --view-link -- claude          # also a read-only link for people who should only watch
setu share --approve -- bash              # confirm every joiner on this terminal
setu share --once --expire 15m -- bash    # a link that works once, for 15 minutes
setu share --record run.cast -- claude    # record the session to replay later
setu share --manual                       # no relays at all: copy and paste the connection details
setu join 'https://…/#k=…'                # join from another terminal instead of a browser
setu desktop --password                   # share the whole desktop
setu service key add claude alice         # Alice's own link; the audit log names her
setu share --audit-endpoint https://otel.example.com:4318 -- claude   # audit log to your collector
```

| Guide | |
| --- | --- |
| [Quick start](https://dmdhrumilmistry.github.io/setu/docs/quickstart.html) | Your first share in two minutes |
| [Claude Code](https://dmdhrumilmistry.github.io/setu/docs/claude-code.html) | Driving Claude Code from a phone |
| [Sharing](https://dmdhrumilmistry.github.io/setu/docs/sharing.html) and [Joining](https://dmdhrumilmistry.github.io/setu/docs/joining.html) | Passwords, approval, background shares, strict networks |
| [Always-on service](https://dmdhrumilmistry.github.io/setu/docs/service.html) | Profiles, pairing, devices, personal links |
| [Remote desktop](https://dmdhrumilmistry.github.io/setu/docs/desktop.html) | Screen, mouse, keyboard and clipboard |
| [Audit log](https://dmdhrumilmistry.github.io/setu/docs/audit.html) | Who connected and what they typed, over OpenTelemetry |
| [MDM and policy](https://dmdhrumilmistry.github.io/setu/docs/mdm.html) | Fleet-wide rules and updates for organizations |
| [CLI reference](https://dmdhrumilmistry.github.io/setu/docs/cli.html) | Every command and flag |

## How it works

```mermaid
sequenceDiagram
    participant H as Your computer (setu share)
    participant R as Public nostr relays
    participant P as Phone (browser)
    P->>R: encrypted "hello" to the room derived from the link
    R->>H: (relays see only ciphertext)
    H->>R: encrypted WebRTC offer
    R->>P: offer
    P->>R: encrypted answer
    R->>H: answer
    P-->>H: direct WebRTC connection (DTLS), STUN finds the path
    H->>P: challenge; P proves its device key (and password or pairing code once)
    H->>P: your terminal, live
```

1. The link's secret is split (HKDF) into a public room id and an AES-GCM key.
2. The two ends exchange an encrypted WebRTC offer and answer through public
   nostr relays, as short-lived events.
3. They connect directly. The host challenges the joiner, and the terminal
   stream starts with a replay of recent output.

The full protocol is in [docs/DESIGN.md](docs/DESIGN.md).

## Platforms

Sharing and joining work on Linux, macOS and Windows (amd64 and arm64). On
Windows, hosting uses ConPTY (Windows 10 1809 / Server 2019 or newer) and
shares PowerShell by default; npm shims like `claude.cmd` work. Remote desktop
needs an X11 session on Linux (Wayland is not supported yet).

## Organizations

A managed policy, deployed with Intune, Jamf, Kandji, VaanarSena, Group Policy
or configuration management, can turn features off, require passwords, device
pairing and personal keys, cap clients and lifetimes, allow-list commands,
relays and web clients, force audit logging, and pin or turn off updates.
setu fails closed: a policy that is malformed or not admin-owned stops it.
Ready-made templates are in [deploy/mdm](deploy/mdm).

## Hosting the web client

The `pages` workflow publishes `web/` to GitHub Pages (**Settings → Pages →
Source → GitHub Actions**). To use your own copy, serve `web/` from any HTTPS
static host and pass `--web-url https://you.example/setu/`.

## Development

```sh
go test -race ./...                       # unit + e2e tests (real PTY + WebRTC + in-memory relay)
node --test web/test/crypto.test.mjs      # JS crypto parity with Go test vectors
go build -o setu ./cmd/setu && SETU_LOCAL_RELAY=1 node web/test/browser-e2e.mjs   # headless Chromium e2e
web/vendor/update.sh                      # re-vendor xterm.js / noble-curves
cd tools/docsgen && go run .              # regenerate docs/*.html after editing docs/src
cd tools/docsgen && go run . -check       # what CI runs: stale output, broken links, undocumented CLI flags
go run ./tools/nextversion                # the release the next green commit on main would cut
```

Releases are automatic: once CI passes on `main`, any change to the setu
binary is tagged (the version follows the commit messages), built for every
platform, signed, attested and published, and installed copies update
themselves. Add `[skip release]` to a commit message to hold one back.

The docs site is Markdown under [`docs/src`](docs/src), rendered to the
committed HTML in `docs/` by [`tools/docsgen`](tools/docsgen). Preview it with
any static server from the repository root (`python -m http.server`, then
http://localhost:8000/docs/). Contributors and coding agents: see
[AGENTS.md](AGENTS.md).
