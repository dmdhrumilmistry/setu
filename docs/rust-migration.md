# Plan: migrating setu from Go to Rust

Status: **stopped after Phase 0; setu stays in Go.** Phase 0 did not meet
the approved gate on terminal throughput, so the owner chose option 1
(see [Decision](#decision)). The plan and the spikes (`rust/spikes`) stay
on file in case desktop performance or CPU use becomes the bottleneck.

## 1. Why, and what to expect

The goal is performance. The first measurement (November 2026, 4 vCPU Linux,
264 MiB of terminal output through a real PTY and WebRTC to the Go client)
gives the baseline and an important caveat:

| Measurement | Result |
| --- | --- |
| Terminal throughput, current Go | 13 MiB/s |
| CPU in setu's replay buffer (reallocated and copied on every 32 KiB chunk, under the session lock) | ~24% of all CPU |
| CPU in the WebRTC stack (pion SCTP + DTLS) | ~22% |
| Same Go code with a fixed replay buffer (prototype, not merged) | 36–38 MiB/s, but fast output then overruns the channel and clients get dropped as "too slow" until backpressure exists |

So the largest cost found so far is an algorithm in setu, not the language,
and a Rust port that copied the design would keep it. What Rust can
plausibly improve:

* **Desktop sharing:** screen diffing and JPEG/PNG encoding use Go's
  standard library encoders, which are much slower than SIMD encoders.
  This is the likeliest real win.
* **Memory and tail latency:** no garbage collector (the profile shows GC
  scanning and `madvise` among the top costs under load).
* **Binary size and startup.**

What it will not automatically improve: interactive latency (dominated by the
network), and WebRTC transport cost (a Rust stack is not known to beat pion
until measured).

Therefore every phase below has a **performance gate** measured against Go
*with* the buffer and backpressure fixes, not against today's slower code.
If Phase 0 shows Rust cannot beat that baseline on the terminal and desktop
paths, the recommendation is to stop and keep the Go fixes.

## Phase 0 results

Measured November 2026 on a 4-vCPU Linux container; spike code and the
commands to rerun it are in `rust/spikes`. Throughput numbers vary run to
run by up to ±30%, so ranges are given.

### Go fixes (committed on this branch, not yet released)

| | Before | After |
| --- | --- | --- |
| Terminal throughput, end to end (`BenchmarkTerminalThroughput`) | 12.3 MB/s | 32.7 MB/s |
| Replay buffer per 32 KiB chunk | realloc + 256 KiB copy | 0 allocations |
| Fast output | client dropped as "too slow" | command slowed to the client's pace (bounded backpressure) |

### WebRTC (a 256 MiB transfer on setu's `setu` channel settings)

| Stack | Host sends (setu's main direction) | Receives from pion | Sender CPU for 256 MiB | Interop with pion, fingerprints | Static builds |
| --- | --- | --- | --- | --- | --- |
| pion (today) | 41–68 MiB/s | 41–68 MiB/s | 6.1–7.9 s | (baseline) | yes, no C |
| webrtc-rs 0.21 | 22–44 MiB/s | 49 MiB/s | 1.5–2.2 s | yes, both directions | musl amd64/arm64 and Windows with `cargo zigbuild` (C via `ring`) |
| str0m 0.26 | 18–60 MiB/s | 79 MiB/s | 1.3–1.7 s | yes, both directions | same, with `cargo zigbuild` (C via aws-lc, pulled in by `rcgen` even with its `rust-crypto` backend) |

* Both Rust stacks interoperate with pion in setu's host and client roles,
  and both expose the SDP, so the DTLS-fingerprint binding (auth proofs,
  device signatures, verification codes) carries over unchanged.
* **CPU:** both use 4–5× less CPU than pion to move the same data.
* **Throughput:** with default settings both are *slower* than pion as the
  sender, the direction a host uses most. SCTP tuning was not attempted.
* Neither is C-free today; static musl and Windows binaries build with Zig
  as the C cross compiler. macOS was not built here (it would build on
  macOS runners).
* Not yet verified for either: TURN, browser interop (Chrome, Firefox,
  Safari), and the selected candidate pair for audit and network rules
  (webrtc-rs has `get_stats`).

### Desktop encoding (one full 1080p frame, same pixels and settings)

| Path | Go (stdlib) | Rust | |
| --- | --- | --- | --- |
| JPEG (photos, video, gradients), q70 4:2:0 | 47.5 ms, 251 KB | `jpeg-encoder` (pure Rust) 15.6 ms, 255 KB; `turbojpeg` (libjpeg-turbo, C) 4.6 ms, 250 KB | **Rust 3–10× faster** |
| PNG (text, UI: ≤256 colours) | 26.7 ms, 133 KB | `png` 17 ms but 307 KB; 112 ms at 174 KB | **Go better** (size) |

Also found in Go: `encodeRect` encodes an area, and if the result exceeds a
message it splits and encodes again, so the benchmark's full-frame cost
(62 ms text, 147 ms photo) is 2–3× the raw encoder cost. That is fixable in
Go.

### Against the gate

The approved gate was: *if Rust does not beat Go with the fixes, stop.*

| Criterion | Result |
| --- | --- |
| Terminal throughput | **Not met.** Go with the fixes reaches 32.7 MB/s end to end, near pion's transport ceiling; both Rust stacks send slower than pion with defaults. |
| CPU per byte on the transport | **Met.** 4–5× less. |
| Desktop JPEG encoding | **Met.** 3× (pure Rust) to 10× (libjpeg-turbo). |
| Desktop PNG encoding | **Not met.** |
| Static, dependency-free builds | **Met with a toolchain change** (Zig as C cross compiler; not C-free). |

The result is mixed, so the decision goes back to the project owner. The
options:

1. **Stop the full migration; keep Go.** Ship the terminal fix (done), fix
   `encodeRect`'s double encoding in Go, and revisit if desktop
   performance becomes the bottleneck.
2. **Continue the full migration** accepting lower peak throughput (still
   well above what a terminal needs) for lower CPU and faster desktop JPEG.
   Recommended stack: **webrtc-rs** (pion's model and API shape, which makes
   the port closer to a translation, and a TURN client inherited from
   pion), with TURN, browser interop and SCTP tuning as the first Phase 1
   tasks. str0m stays the fallback if its CPU and receive advantages
   matter more than the porting effort of a sans-IO design.
3. **A narrower Rust component**: only the desktop encoder in Rust,
   called from Go. This needs cgo, which ends today's `CGO_ENABLED=0`
   builds, so it carries most of the build cost of option 2 for one
   component.

## Decision

**Option 1: keep Go.** Done as part of the decision:

* The terminal fix (replay buffer and bounded backpressure): 12.3 to
  32.7 MB/s end to end.
* The desktop encoder no longer encodes a large area whole and then again
  in halves: large areas are cut into tile-aligned pieces before encoding,
  which also spreads them over the encoding workers. A full 1080p refresh
  went from about 71 to 13 ms (text) and 148 to 22 ms (photo), for 2–3%
  more bytes in per-piece image headers (`BenchmarkEncodeFrame`). That puts
  Go's whole-frame JPEG time in the same range as the pure-Rust encoder
  measured above.

Reasons to reopen the migration later: CPU use on hosts becomes a
complaint (the Rust transports used 4–5× less), or desktop sharing needs
libjpeg-turbo-class encoding speed at high resolutions and frame rates.
Phase 0's measurements and spikes are the starting point.

## 2. Compatibility contract (must not break)

Installed copies update themselves, so the first Rust release replaces Go
binaries in place, under running services, on every platform. It must be a
drop-in replacement:

| Surface | Requirement |
| --- | --- |
| Wire protocol | Signaling (nostr events, HKDF room ids, AES-GCM sealing and AAD binding), data channel messages (`internal/proto`), auth proofs, device-key signatures, resume tokens, SAS codes: byte-for-byte identical. The web client (`web/js`) does not change. |
| Invite links and codes | Same fragment format and deflate+base64url codes (`internal/link`). |
| State files | `config.json`, `devices.json`, `access.json`, `history.jsonl`, `device.key`, background session dirs and the locks: same paths, formats, strict parsing, permission checks and lock protocol, so Go and Rust processes can run side by side during cutover. |
| CLI | Every command, flag, default, exit code and message meaning. Go's flag package accepts `-flag` and `--flag` and `--flag=false`; clap does not by default, so a compatibility parser is required. Service unit files and scripts already call `setu service run …`. |
| Autostart | systemd user unit, launchd agent and logon task: same names, same binary path and arguments. |
| Releases | Same asset names (`setu-vX.Y.Z-os-arch[.exe]`), `SHA256SUMS` and the ed25519 `SHA256SUMS.sig` verified against the same release keys, so Go installs update into Rust and Rust can roll back to Go (`setu upgrade --version`). |
| Managed policy | Same registry key, macOS managed preferences domain and `policy.json` paths, same keys, fail-closed parsing; `deploy/mdm` templates unchanged. |
| Audit | Same OTLP/HTTP records and attribute names (`setu.*`), same redaction and the same fail-closed `--audit-required` behaviour; collectors' queries keep working. |
| Platforms | linux, darwin and windows on amd64 and arm64, as static binaries with no runtime dependencies (today: `CGO_ENABLED=0`). |

## 3. Strategy

**Incremental and interoperability-tested, not big-bang.** The protocol is
specified and the two ends are separable, so the Rust code can be proven
against the Go code at every step:

* A Cargo workspace under `rust/` in this repository, crates mirroring the
  Go packages. The Go code keeps shipping, unchanged, until cutover.
* **Conformance tests across implementations**: Go client ↔ Rust host, Rust
  client ↔ Go host, and the existing headless Chromium web-client test
  against the Rust host. The Go e2e suite becomes the conformance suite.
* Shared test vectors (keys, sealed messages, proofs, links) generated by the
  Go code and checked by both.
* Cutover only when every Go test has a passing Rust counterpart and the
  performance gates hold.

## 4. Technology choices

Marked **(spike)** where the choice must be validated in Phase 0 before
committing; the others are established crates.

| Need | Candidate | Note |
| --- | --- | --- |
| Async runtime | `tokio` | |
| WebRTC data channels | **(spike)** `str0m` (pure Rust, sans-IO), `webrtc` (webrtc-rs, a pion port), or `datachannel` (libdatachannel bindings, C++) | The critical decision. Requirements: data channels with DTLS 1.2, ICE incl. TURN and srflx, selected-candidate details (for audit and network rules), remote DTLS fingerprint access (SAS and auth binding), static cross-compilation for all 6 targets, maintenance health, throughput at least pion's. A C++ dependency complicates static builds and the audit surface. |
| Crypto | RustCrypto: `aes-gcm`, `hkdf`, `hmac`, `sha2`, `pbkdf2`, `p256` (device keys), `k256` (BIP-340 Schnorr for nostr), `ed25519-dalek` (release signatures) | Constant-time comparisons via `subtle`. |
| Nostr client | Own minimal NIP-01 client over `tokio-tungstenite` + `rustls`, as today | Keeps the small, audited surface rather than a full SDK. |
| PTY | `portable-pty` (Unix PTY and ConPTY) **(spike: echo-off password detection, Job Objects, resize)** | |
| Desktop capture/input | `x11rb` (MIT-SHM, XTEST), `objc2` + CoreGraphics bindings, `windows` crate (GDI, `SendInput`) | No cgo-equivalent C toolchain needed except where a framework requires it. |
| Image encoding | **(spike)** `jpeg-encoder`, `zune-jpeg`/`png` crates vs libjpeg-turbo bindings | The main expected performance win; must stay byte-compatible with the web client's decoder (it decodes standard JPEG/PNG, so any conforming encoder works). |
| Audio | ADPCM codec ported as is | |
| Audit | `opentelemetry`, `opentelemetry_sdk` (logs), `opentelemetry-otlp` (HTTP/protobuf, gzip) | Bounded batch queue semantics must match. |
| Dashboard | `axum` + `rust-embed`; the same `static/` files | Same security headers and checks. |
| CLI | `clap` plus a compatibility layer for Go-style single-dash flags | |
| Config/JSON | `serde` with `deny_unknown_fields` | Matches strict Go parsing. |
| QR codes | `qrcode` | |
| Self-update | Own verifier (SHA256SUMS + ed25519) + atomic swap (`self-replace` on Windows) | |

## 5. Phases

Effort is a rough estimate for one experienced engineer; it is the part of
this plan most likely to be wrong.

| Phase | Scope | Exit criteria | Effort |
| --- | --- | --- | --- |
| 0. Baseline and spikes (**done**) | Merge the Go buffer fix and real backpressure (users benefit now; honest baseline). Add throughput and desktop-encoding benchmarks to CI. Spike WebRTC crate choice (data channel ↔ pion interop, TURN, fingerprints, static builds for 6 targets) and image encoder. | Benchmarks in CI; written decision on the WebRTC crate with interop and throughput numbers; **go/no-go on the whole migration**. See [Phase 0 results](#phase-0-results). | 2–3 weeks |
| 1. Core | `secure`, `link`, `proto`, `nostr` crates; shared test vectors with Go. | All vectors pass both ways; fuzzers for link/proto parsing. | 2–3 weeks |
| 2. Client | `setu join` in Rust (signaling, WebRTC, auth, device keys, resume, raw terminal). | Rust client passes the conformance suite against the Go host on all 3 OSes. | 2–3 weeks |
| 3. Terminal host | `host` crate: PTY, replay buffer, admission, auth layers, approval, resume, idle timeout, uploads, recording, notifications; `setu share` incl. background shares. | Go client and web client pass every e2e test against the Rust host; terminal throughput gate passed. | 5–7 weeks |
| 4. Service | Supervisor, profiles, devices and pairing, access lists, policy, audit, history, control socket, autostart per OS, `setu doctor`, self-update. | State files interchangeable with Go (mixed-version test); policy deploy tests pass; update Go→Rust→Go round trip works. | 6–8 weeks |
| 5. Desktop | Capture, diff, encoding, input, clipboard, audio on X11, macOS and Windows. | Desktop e2e on all 3 OSes; desktop encoding gate passed. | 6–10 weeks |
| 6. Dashboard and the rest | `setu dashboard`, remaining commands, docs generator inputs (CLI flag check against the Rust binary). | Dashboard tests ported; docs check passes against the Rust CLI. | 2–3 weeks |
| 7. Hardening and cutover | Security review of the whole Rust tree, fuzzing, release pipeline for Rust (cross builds, smoke tests, signing, provenance), pre-releases, staged rollout. | See §7. | 3–4 weeks |

Total: roughly **7–10 months** of one engineer, plus review time.

## 6. Testing

* Port every Go unit test; keep the Go e2e suite running against the Rust
  binaries until cutover, then port it.
* Cross-implementation matrix in CI on Linux, macOS and Windows.
* Mixed-version tests: a Go CLI editing state while a Rust supervisor runs,
  and the reverse.
* Fuzz the parsers that read untrusted input: links, signaling messages, data
  channel messages, JSON state files, policy files, SDP handling.
* Performance gates in CI: terminal throughput, connection setup time,
  desktop frames per second and encode time, memory under load.

## 7. Release and cutover

1. Rust builds published as **pre-releases** (`vX.Y.Z-rc.N`), opt-in with
   `setu upgrade --version`; automatic updates never install pre-releases.
2. Dogfood on all three OSes, including a service running for weeks and
   desktop sharing.
3. First stable Rust release: same asset names and signing, so existing Go
   installs update into it. `setu upgrade --version <last Go release>` stays
   available as the rollback path.
4. Keep the Go tree in the repository, building and testing, for one or two
   releases after cutover; then remove it.

The release workflow, `tools/nextversion` (which looks at `cmd/` and
`internal/`) and `AGENTS.md` change in the same cutover PR.

## 8. Risks

| Risk | Mitigation |
| --- | --- |
| Rust WebRTC stack less mature than pion (ICE edge cases, TURN, interop with every browser) | Phase 0 spike with real interop tests; browser e2e against the Rust host on every PR; go/no-go before Phase 1. |
| Security regressions in a remote-shell tool | Port tests first; independent security review before cutover; fuzzing; no `unsafe` outside the OS bindings, each block justified. |
| Auto-update ships a broken binary everywhere | Pre-releases, staged rollout, the self-test the updater already runs, rollback to Go. |
| Windows and macOS specifics (ConPTY, Job Objects, launchd, GDI, CoreGraphics) | Port per OS with CI on each; reuse the Go code as the reference. |
| Two codebases during migration (fixes needed twice) | Freeze features in Go except security fixes; keep the migration short by ordering phases so each lands usable code. |
| Expected speedup does not materialise | Performance gates per phase; Phase 0 go/no-go. |

## 9. Decisions needed

1. Approve the **Phase 0 go/no-go gate**: if Rust does not beat Go with the
   fixes, stop and keep Go.
2. Approve doing the Go buffer and backpressure fix first (Phase 0).
3. Accept a feature freeze in Go (security fixes excepted) during migration.
4. Choose who reviews security before cutover.
