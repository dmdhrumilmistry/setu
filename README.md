# setu

**Share your terminal peer-to-peer, from anywhere, without running a server.**
Built for driving Claude Code and other AI agents on your machine from your
phone or another computer.

**Documentation: https://dmdhrumilmistry.github.io/setu/docs/**

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
go install github.com/dmdhrumilmistry/setu/cmd/setu@latest   # Go 1.27.2+
```

Release binaries for Linux, macOS and Windows are on the
[releases page](https://github.com/dmdhrumilmistry/setu/releases).
Sharing and joining work on Linux, macOS and Windows. On Windows, hosting uses
the ConPTY pseudo console, so it needs Windows 10 1809 / Server 2019 or newer,
and the default shell is PowerShell. `.cmd` shims like npm's `claude` work too.

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
setu share -d --password -- claude         # run in the background and get your shell back
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
| `--approve` | off | Ask on the host for every joiner: `y` once, `a` always for that device, `N` no. |
| `--trust-device` | none | Device key fingerprint (`setu device`, or the web client's home page) approved without asking. With `--approve` in the background, only these devices may join. |
| `--manual` | off | Copy/paste signaling instead of nostr. |
| `--relay` | 4 public relays | Nostr relay URL(s) to use. |
| `--stun` | Google STUN | STUN server URL(s). |
| `--turn`, `--turn-user` | — | TURN relay for strict NATs (credential in `SETU_TURN_PASSWORD`). |
| `--web-url` | GitHub Pages | Base URL of the web client (set your own if you self-host). |
| `--headless` | off | Don't attach the local terminal. |
| `--qr` | on | Print a QR code of the browser link. |
| `--link-file` | — | Write invite link(s) to a 0600 file. |
| `-d`, `--background` | off | Detach: serve in the background and return to the shell. |

### Background shares

`setu share -d …` starts the share as a detached background process. It prints
the links and QR code, then gives you your prompt back. The share keeps running
after you close the terminal (on Windows, after you close the window).

```sh
setu share -d --password -- claude   # prompts for the password, then detaches
setu ps                              # list background shares
setu link <id>                       # print the links / QR code again
setu logs <id>                       # who connected, with verification codes
setu stop <id>                       # end one share (or: setu stop --all)
```

Each background share keeps its state in `~/.config/setu/sessions/<id>/`
(`%AppData%\setu\sessions\<id>\` on Windows), readable only by you. That
state includes the invite links. The directory is removed when the share
ends. The password is passed to the background process through a pipe, never
on the command line or in the environment. `--manual` and `--approve` need an
attached terminal, so they cannot be combined with `-d`.

### Always-on shares (`setu service`)

A *profile* is a command plus a persistent invite key. The setu service runs
in the background, starts at login and serves every enabled profile whose
keys have not expired. If a profile's command exits, the service restarts it,
so the profile's link keeps working across reboots.

```sh
setu service add claude -- claude      # prompts for a password, prints the links once
setu service install                   # start at login: systemd user unit / launchd agent / Task Scheduler
setu service list                      # profiles, state, expiry
setu service link claude               # show the links (+ QR) again
setu service key add claude alice      # Alice's own link: the audit log names her
setu service key list claude
setu service key remove claude alice   # revoke only Alice's link
setu service rotate claude             # new keys for everyone; the old links stop working immediately
setu service disable claude            # stop serving without deleting (enable to resume)
setu service remove claude
setu service status                    # autostart, supervisor and audit state
setu service uninstall
```

The running service picks up changes within a few seconds, so you don't need
to restart it. Because these links live for a long time, profiles are
stricter than `setu share`:

* Give each person their own key (`--person alice` on `add`, or
  `setu service key add`). The service tells people apart by which key
  decrypted their join request, never by anything the client claims, so
  audit records name them reliably. `--no-shared-key` leaves no shared,
  anonymous control link at all. You can also revoke one person without
  affecting anyone else.
* A password (12+ characters) is required unless you pass `--no-password`.
  The service stores only a PBKDF2 verifier, never the password.
* Keys expire after `--valid-for` (default 90 days). `rotate` renews them.
* `--max-clients` defaults to 1. `--once` and `--approve` are not available,
  because nobody is at the host terminal to answer.
* The command is resolved to an absolute path when you add the profile, so a
  directory added to `PATH` later cannot substitute another program.

State lives in `~/.config/setu/service/` (`%LOCALAPPDATA%\setu\service\` on
Windows, which does not roam). `config.json` holds the keys, so it must be
mode 0600. setu refuses to load it otherwise. Only one supervisor can serve a
state directory at a time.

### Audit logging (OpenTelemetry)

setu can send an audit trail to an OpenTelemetry collector as OTLP/HTTP log
records. You can route those records from the collector to Loki,
Elasticsearch, Splunk, Datadog or any other OTLP backend.

```sh
export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer%20<token>'   # credentials: env only, never flags
setu share --audit-endpoint https://otel.example.com:4318 --audit-required -- claude

setu service audit --endpoint https://otel.example.com:4318          # for the service (sends a test record first)
```

| Event (`event.name`) | When |
| --- | --- |
| `setu.session.start` / `setu.session.end` | A share starts or ends (command, password on/off, exit code) |
| `setu.client.connect` / `setu.client.disconnect` | A client joins or leaves (role, verification code, DTLS fingerprint, `client.address`) |
| `setu.client.rejected` / `setu.client.auth_failure` / `setu.client.approval` | Admission decisions |
| `setu.command` | A line someone submitted, attributed to the client (or `via=local` for the host terminal) |
| `setu.input.control` | ^C, ^D or ^Z |

Every record carries `setu.session.id`, and service records also carry
`setu.profile`. Each `setu.command` record stands on its own and answers
three questions:

| | Attributes |
| --- | --- |
| **Who** ran it | `setu.actor` (for example `alice (client #2 from 203.0.113.7:51234, device alice-laptop (linux/amd64, setu-cli), verification code 769 168)`), `setu.actor.name` (the personal key's owner, `host-terminal`, or `shared-control-link`), `setu.client.verification_code`, `setu.client.dtls_fingerprint`, and the account and machine it ran as (`user.name`, `host.name`, `host.id`) |
| **From where** | `client.address`/`client.port` (the selected network path), `network.transport`, `setu.client.candidate_type`, `setu.client.public_addresses` (the client's public IP:port as its NAT presents it, which is still the real origin when the path is a TURN relay) and `setu.client.local_addresses` (LAN addresses; browsers hide these) |
| **On what device** | `setu.client.device.*`: `app`, `version`, `id` (a random id kept per installation or browser, so you can tell a returning device from a new one), `os`, `os_version`, `arch`, `model`, `hostname`, `user`, `language`, `timezone`, `screen`, `mobile`, plus `user_agent.original`. These are **self-reported** (`setu.client.device.self_reported=true`): good for investigation, never used for access decisions. The host strips control characters and caps lengths, so a client cannot inject escape sequences or forge log lines. `setu join --anonymous` sends none of them. |
| **When** | the record timestamp, plus `setu.command.submitted_at` (Enter pressed) and `setu.command.started_at` (first keystroke), in RFC 3339 UTC |
| **What** | the record body and `setu.command.text`, plus `setu.session.command` (the program it was typed into, for example `bash` or `claude`) |

`setu.actor.name` can only name a person if each person has their own key.
With a shared link, every holder looks the same (`shared-control-link`), so
use personal keys (`setu service key add`) when you need to know who typed a
command.

Commands are rebuilt from keystrokes, so they are best effort. setu sees what
was typed, not what the shell finally ran. Lines that used arrow keys, history
or tab completion are flagged `setu.input.edited=true`. Commands that an agent
runs on its own are not keystrokes, so setu does not see them. Claude Code's
own OpenTelemetry export covers those.

Secrets are kept out of the log:

* On Linux and macOS, lines typed while the terminal has echo off (sudo,
  ssh and other password prompts) are replaced with a marker.
* Common token formats (AWS, GitHub, Slack, Anthropic/OpenAI, JWTs, bearer
  tokens, `password=`-style assignments, credentials in URLs, private keys
  and setu invite keys) are redacted. Add your own patterns with
  `--audit-redact <regex>`.
* Use `--audit-input off` to record connections only.

| Flag (`setu share`) | Meaning |
| --- | --- |
| `--audit` | Enable, using `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` / `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `--audit-endpoint` | Collector URL (`/v1/logs` is added to a bare origin). Implies `--audit`. |
| `--audit-required` | Fail closed: refuse to share unless the collector accepts a test record. |
| `--audit-ca`, `--audit-cert`, `--audit-key` | Pin the collector's CA, and use mutual TLS. |
| `--audit-insecure` | Allow plain `http://` to a host other than this machine (refused by default). |
| `--audit-input` | `lines` (default) or `off`. |
| `--audit-redact` | Extra regular expression to redact (repeatable). |

### Managed devices (MDM)

Organizations can deploy a **managed policy** through Intune, Jamf, Kandji,
VaanarSena, Group Policy or configuration management. The policy can:

* disable sharing or the service,
* require passwords and personal keys,
* cap clients and lifetimes,
* restrict which programs, relays and web clients may be used,
* force audit logging to the organization's collector.

setu fails closed: if a policy is present but malformed or not
admin-owned, setu will not run. `setu policy` shows what is in force.

Ready-made artifacts are in [deploy/mdm](deploy/mdm): an ADMX/ADML template,
a `.mobileconfig` profile, a `.reg` file, a Linux installer and a VaanarSena
manifest.

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

### Documentation

The docs site is written in Markdown under [`docs/src`](docs/src) (plus
[`docs/DESIGN.md`](docs/DESIGN.md)) and rendered to static HTML in `docs/` by
[`tools/docsgen`](tools/docsgen). The HTML is committed because GitHub Pages
serves the `main` branch as is. After editing a page:

```sh
cd tools/docsgen && go run .          # regenerate docs/*.html and the search index
cd tools/docsgen && go run . -check   # what CI runs: stale output, broken links, undocumented CLI flags
```

Preview locally with any static server from the repository root, for example
`python -m http.server` and http://localhost:8000/docs/.
