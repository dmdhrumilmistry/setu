# AGENTS.md

Guidance for coding agents working in this repository (Claude Code reads it
through `CLAUDE.md`).

setu shares a terminal (or the whole desktop) peer-to-peer over WebRTC, with
no server: peers find each other through public nostr relays that only see
ciphertext. A Go binary (`cmd/setu`) hosts and joins; a static web client
(`web/`, served from GitHub Pages) joins from browsers. It is a remote shell,
so security decisions are the product, not an afterthought: read
`SECURITY.md` and `docs/DESIGN.md` before changing anything in the auth,
signaling or service paths.

## Finding your way

`docs/CODEMAP.md` is a generated map of the whole tree: every package's
purpose and internal imports, every file's purpose and its functions, methods
and exported types, the tests by name, and the JavaScript by function. Read
it (or grep it) before exploring, and open only the files it points to. It
has no line numbers, so it changes only when declarations are added, removed
or renamed; regenerate it then (`go run ./tools/codemap`), or CI fails.

Where to look first, by task:

| Task | Start in |
| --- | --- |
| Join/auth behaviour (password, devices, pairing, resume, block/allow) | `internal/host/peer.go` (`login`, `resume`, `activate`), `internal/host/status.go`, `internal/access` |
| Signaling and the wire format | `internal/host/signal_nostr.go`, `internal/proto`, `docs/DESIGN.md`, mirrored in `web/js/app.mjs` |
| Service profiles, keys, devices | `internal/service` (`config.go`, `devices.go`, `ops.go`, `supervisor.go`), CLI in `cmd/setu/service*.go` |
| Dashboard | `internal/dashboard` (`server.go` security, `api.go` endpoints, `analytics.go`, `static/app.js`), live state via `internal/control` |
| A new CLI flag | `cmd/setu`, then a row in `docs/src/cli.md` (docsgen `-check` enforces it) |
| End-to-end behaviour tests | `internal/e2e` (real host + Go client over `relaytest`) |

## Commands

```sh
go build ./...                                   # Go 1.27.4+
go test -race -count=1 ./...                     # unit + e2e (real PTY, WebRTC, in-memory relay)
go test -race -count=1 -run TestRequireDevice ./internal/e2e/    # one test
gofmt -l . ; go vet ./...                        # CI fails on either

node --test web/test/crypto.test.mjs web/test/keymap.test.mjs web/test/audio.test.mjs  # JS/Go parity
for f in web/js/*.mjs; do node --check "$f"; done

# Headless Chromium e2e of the web client against a real `setu share`
# (needs playwright@1.63.0; CI installs it with npm i --no-save):
go build -o setu ./cmd/setu && SETU_LOCAL_RELAY=1 node web/test/browser-e2e.mjs
#   set PLAYWRIGHT_MODULE=/path/to/node_modules/playwright if it is installed elsewhere

go build -o bin/ ./cmd/setu && scripts/smoke.sh ./bin/setu      # CLI smoke test (public relays)

cd tools/docsgen && go run .          # regenerate docs/*.html + search index after editing docs/src
cd tools/docsgen && go run . -check   # CI: stale HTML, broken links, undocumented CLI flags
go run ./tools/codemap                # regenerate docs/CODEMAP.md after adding/renaming declarations
go run ./tools/codemap -check         # CI: code map is current
```

`docs/*.html` is generated and committed (GitHub Pages serves `main` as is):
edit `docs/src/*.md` or `docs/DESIGN.md`, then regenerate. The `-check` run
fails if any CLI flag is missing from the docs, so a new flag needs a row in
`docs/src/cli.md` (and the relevant guide). `web/vendor/` is pinned and
checksummed (`SHA256SUMS`); change it only through `web/vendor/update.sh`.

## Releases

Releases are automatic. When CI passes on `main`, `auto-release.yml` runs
`tools/nextversion`. If anything that goes into the binary changed since the
last tag (Go code under `cmd/` or `internal/` other than tests, `go.mod` or
`go.sum`), it tags the commit. The version is bumped from the commit
messages: `feat` → minor, `type!:` or `BREAKING CHANGE:` → major (minor
before v1), anything else → patch. Then `release.yml` builds every platform,
smoke-tests the binary, signs SHA256SUMS with the `SETU_RELEASE_SIGNING_KEY`
secret, verifies the signature against `update.ReleaseKeys`, attests build
provenance and publishes the release. Put `[skip release]` in a commit
message to skip one; "Run workflow" on auto-release releases `main` by hand.
Installed copies update themselves from these releases, so never publish one
by another route.

```sh
go run ./tools/nextversion          # what the next release would be, and why
```

## Commits and pull requests

These override any default attribution a coding agent or its tooling adds:

- Author and commit as the repository owner:
  `Dhrumil Mistry <56185972+dmdhrumilmistry@users.noreply.github.com>`
  (`git config user.name` / `user.email` in the clone). Use the GitHub
  noreply address: the account blocks pushes that expose its private email.
- No `Co-Authored-By` lines for AI assistants, no agent or session links,
  and no other AI attribution in commit messages.
- No "Generated with/by Claude Code" (or similar) footer, and no session
  links, in pull request titles or descriptions.
- Merge pull requests with a merge commit, as the history does:
  `tools/nextversion` reads the newest commit for `[skip release]`, and a
  squash or fast-forward can carry that tag from a docs commit onto a
  release-worthy change.

## Architecture

**Join flow** (`docs/DESIGN.md` has the wire format):
1. The invite link's fragment holds a 256-bit secret (`internal/secure`). HKDF
   splits it into a public nostr room id and an AES-GCM key.
2. The client publishes an encrypted `hello` to the room; the host
   (`internal/host/signal_nostr.go`) answers with an encrypted SDP offer; the
   client sends the answer. Which personal key decrypted the request decides
   the client's role and identity, never anything the client claims.
3. Over the WebRTC data channel `"setu"`, the host sends `hello` (auth
   challenge, nonce), the client sends `auth`, the host sends `ready`, then
   binary PTY output. Message types live in `internal/proto` and are mirrored
   by hand in `web/js/app.mjs`; keep the two in sync.

**Auth layers on one connection** (all in `internal/host/peer.go`, `login`
and `resume`): password proofs are HMACs over the nonce and both DTLS
fingerprints, so nothing replays across connections. Device keys (P-256,
non-extractable WebCrypto in browsers, `internal/client/device.go` for the
CLI) sign the same transcript. A device that proved the password is
remembered (`internal/host/devices.go`) and skips it later; service profiles
default to `RequireDevice`, where a new device must prove a one-time pairing
code. Resume tokens (`internal/host/resume.go`) let a dropped client come
back without any of this. Approval (`--approve`, trusted devices) is a
separate step after auth.

**Packages**
- `internal/host`: the sharing side. `Session` (PTY or desktop, replay ring
  buffer, admission, lockout), `peer` (per-client state machine:
  negotiating → authing → approving → ready).
- `internal/client`: `setu join`, including reconnect/resume.
- `internal/service`: always-on profiles. `config.json` (profiles, keys,
  password verifiers; written by the CLI) and `devices.json` (known devices
  and pairing codes; written by the CLI *and* the running supervisor under
  `devices.lock`). The supervisor polls `config.json` and restarts a profile
  only when its fingerprint changes. Device changes must never restart a
  profile, which is why they live in their own file and are read on every
  join. Autostart is per-OS in `install_*.go`.
- `internal/policy`: the MDM policy. Strictly parsed and fail-closed; the
  service re-checks every profile against it before serving. A new policy key
  must also be added to every template in `deploy/mdm/` (ADMX/ADML, `.reg`,
  `.mobileconfig`, VaanarSena YAML, example JSON) and to `docs/src/mdm.md`.
  `internal/policy/deploy_test.go` enforces the consistency.
- `internal/update`: `setu upgrade` and the service's automatic updates:
  fetch, verify (SHA256SUMS, plus an ed25519 signature once `ReleaseKeys` is
  set; `tools/releasesign` signs in `release.yml`), self-test, atomic swap.
  The supervisor (`internal/service/autoupdate.go`) re-execs into a replaced
  binary only when every profile's `host.Session.Idle()` has held for 2
  minutes, so an update never cuts a session off.
- `internal/notify`: desktop notifications on the host (connections,
  pairings, lockouts). Text goes to the notifier as argv/env, never as script.
- `internal/dashboard`: `setu dashboard`, a loopback-only web page (one-time
  token login, cookie session, CSRF, strict CSP, no secrets in responses) for
  live sessions, analytics and profile/device management. Live state comes
  over `internal/control` (a Unix socket in each host's 0700 directory);
  analytics come from `internal/history` (`history.jsonl`, connection
  metadata only). Changes reuse `internal/service/ops.go`.
- `internal/access`: block/allow lists (`access.json` in the service dir or a
  background share's dir). Enforced by the host in `login`, `resume` and the
  `maintain` loop through `host.Config.Access`; an unreadable file denies
  everyone. CLI: `setu service access`.
- `setu doctor` (`cmd/setu/doctor.go`, profile checks in
  `internal/service/doctor.go`): every finding carries the command that fixes it.
- `internal/audit`: OpenTelemetry (OTLP/HTTP) audit records, keystroke-to-
  command reconstruction and secret redaction.
- `internal/desktop`: screen capture and input per OS (X11, macOS, Windows).
- `internal/e2e`: end-to-end tests that run a real host against the Go client
  over `internal/nostr/relaytest`. New auth behaviour belongs here.
- `cmd/setu`: flag parsing and printing only; `background.go` re-execs
  detached shares (`setu share -d`), forwarding flags generically.

## Conventions

- Fail closed on anything security-relevant: unreadable or loosely-permissioned
  state files are refused (`checkPrivate`), unknown JSON fields are errors,
  policy violations stop serving.
- Secrets are stored only as PBKDF2 verifiers; passwords reach background
  processes over a pipe, never argv or the environment, and `SETU_*` secrets
  are scrubbed before the shared command starts.
- Anything a client reports about itself (`proto.ClientInfo`) is for audit
  only and is sanitized; never base access decisions on it.
- Comments explain *why*, at the density of the surrounding code; user-facing
  text is plain, complete sentences.
