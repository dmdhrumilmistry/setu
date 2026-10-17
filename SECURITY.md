# Security model

setu gives another device **a live shell on your machine**, often one running
an AI agent with access to your files and credentials. Treat every invite link
like an SSH private key.

## What protects you

| Layer | Mechanism |
| --- | --- |
| Invite secret | 256 random bits per share, carried only in the URL **fragment** (`#k=…`), which browsers never send to the page host (GitHub Pages). The web page strips it from the address bar and history as soon as it loads. |
| Signaling privacy | Every relay message is AES-256-GCM encrypted with a key derived (HKDF-SHA256) from the secret. Relays see a random room id, throwaway public keys, sizes and timing, and nothing else. |
| Signaling integrity | Each message's AEAD associated data binds the room, sender and recipient. Nostr events are BIP-340 signed, and their signatures are checked before de-duplication. The host drops messages that are stale (±2 min), replayed (`pubkey`,`sid`) or rate-limited, and drops any answer from a key other than the one that asked to join. |
| Transport | WebRTC DTLS 1.2+ with SCTP, end to end between the two peers. Nothing else carries terminal bytes. Google STUN only learns your public IP:port. |
| MITM detection | Both sides print a 6-digit **verification code** derived from both DTLS certificate fingerprints. If the codes differ, someone is in the middle, so disconnect. |
| Password (`--password`) | A second factor on top of the link. The host announces a salt, a PBKDF2-SHA256 work factor (200k iterations; clients refuse less than 100k) and a fresh 256-bit nonce. The client answers with `HMAC(PBKDF2(pw), nonce ‖ both DTLS fingerprints)`. The password never crosses the wire, and a proof cannot be reused on another connection. The host keeps only the derived key. After 5 failures it locks for 10 minutes, and after 20 the invite is disabled. |
| Roles | `--view-link` creates a separate secret whose holders can only watch. The host decides each client's role from which secret decrypted the request, so a client cannot claim a role. The host drops keystrokes and resizes from view-only clients. |
| Admission | `--max-clients` (default 2), `--once` (single use), `--expire 30m`, and `--approve` (y/N prompt on the host showing the verification code). A handshake that doesn't finish within 2 minutes is dropped, and no more than 3 handshakes can be pending at once. |
| Reconnects | A client whose connection drops (a phone switching between Wi-Fi and mobile data, a laptop waking up) resumes the same session automatically. When it becomes ready the host hands it a random 256-bit resume token over the encrypted data channel; the client keeps it in memory only, and the host stores only a hash. The token stands in for the password and approval on reconnect, is bound to the same invite (role and personal key) and to the client's device key, works once (every reconnect gets a new one) and expires 10 minutes after the connection is lost, or an hour if the client had said its tab was in the background. With "Keep a session going" on (the default), the web client keeps the invite and the current token in `sessionStorage`, so a reload, or a tab the browser discarded in the background, resumes too. That storage belongs to the one tab, is deleted when the tab closes, expires after an hour away, and is wiped when the session ends or you press **Leave**. Because the token is bound to the device key, with "Remember this device" off a reload asks for the password again. A wrong token counts as a failed login. A client disconnected by `--idle-timeout` loses its token, so it must log in again, and reconnecting does not reset its idle clock. After `--once` or `--expire`, the host keeps listening only for clients holding a live token, and only until the last one expires. Reconnect attempts back off exponentially (with jitter) up to 30 seconds apart. |
| Device keys | Every client signs each connection with a P-256 device key: a non-extractable WebCrypto key in the browser (stored in IndexedDB only if "Remember this device" is on), an owner-only file for `setu join`. The signature covers the host's nonce and both DTLS fingerprints, so it cannot be replayed, and a bad signature counts as a failed login. With `--approve`, the host can trust device fingerprints (`--trust-device`, or answering "a" for always); that trust lives in memory for the share. Without a terminal, `--approve --trust-device` admits only trusted devices. |
| Remembered and paired devices | A device that proves the password with a verified device key is remembered (tied to the invite key it used), so it is not asked again: in memory for `setu share`, in the owner-only `devices.json` for the service. Every later join still needs the link *and* a fresh signature by that non-extractable key over the connection's nonce and DTLS fingerprints. `--password-every-join` and the `PasswordEveryJoin` policy turn this off, and changing a service password forgets devices remembered by the old one. Service profiles default to paired devices only: a new device proves a single-use, expiring 60-bit pairing code (`setu service pair`; only a PBKDF2 verifier is stored, wrong codes count toward the lockout). Removing a device disconnects it within seconds and invalidates its resume token. Policies can require pairing (`RequireDevicePairing`), cap code lifetime (`MaxPairingTime`) and the number of devices (`MaxDevicesPerProfile`). |
| Alerts, device expiry, doctor | The host shows a desktop notification whenever someone connects, a device pairs or logins keep failing (on for the service and for unattended shares; the text is passed to the notifier as arguments, never as script). Service devices unused for 90 days are forgotten (`--device-idle`, policy `MaxDeviceIdle`). `setu doctor` reports risky settings with the command that fixes each one, and `--json` output for compliance checks. |
| Updates | `setu upgrade` and the service's automatic updates (on by default; policies `DisableUpdate`, `AutoUpdate`, `UpdateVersion`, `UpdateURL`) download over https, verify the release's `SHA256SUMS` and, in builds that carry the release key, its ed25519 signature (unsigned releases are then refused), run the new binary once, and swap it in with an atomic rename. Running shares keep their binary; the service restarts into the new one only when nobody is connected and no session can be resumed. Development builds are never updated automatically. |
| Hygiene | `SETU_PASSWORD` and `SETU_TURN_PASSWORD` are removed from the environment before the shared command starts. `--link-file` is written with mode 0600. Background shares (`-d`) keep their links in a per-user 0700 directory that is deleted when the share ends, and receive the password over a pipe rather than argv or the environment. On Windows the shared command runs in a kill-on-close Job Object, so it cannot outlive setu. Slow clients are disconnected once 8 MiB are buffered, so they cannot exhaust host memory. |
| Audit log (`--audit-*`, `setu service audit`) | Connections, admission decisions and submitted commands go to your OpenTelemetry collector over OTLP/HTTP. The endpoint must be https unless it is loopback or you pass `--audit-insecure`. Credentials in the URL are refused, and TLS 1.2 is the minimum, with an optional pinned CA and mutual TLS. Collector credentials come only from `OTEL_EXPORTER_OTLP_HEADERS` (or the 0600 service config) and are removed from the environment before the shared command starts. Records are batched and flushed every second, off-box, so a participant who later gains control of the host cannot erase what was already exported. The queue is bounded (8192 records, oldest dropped) and never blocks the terminal. `--audit-required` fails closed: no share starts unless the collector accepted a test record. The service also serves nothing while its audit pipeline cannot be built. Lines typed with terminal echo off (password prompts, Unix) are never recorded, and known secret formats are redacted before export. |
| Managed policy (MDM) | An admin-deployed policy (registry `HKLM\SOFTWARE\Policies\setu`, macOS managed preferences, or a root/Administrators-owned `policy.json`) can disable features, require passwords and personal keys, cap clients and key lifetimes, allow-list commands, relays and web clients, and force audit logging to the organization's collector. It is parsed strictly and fails closed: a policy that exists but is unreadable, malformed, has unknown keys, or could be written by a non-administrator stops setu instead of being ignored. The service re-checks every profile against the policy before serving it, because users can edit their own service config. See [deploy/mdm](deploy/mdm/README.md). |
| Attribution | Every audited command records who (the personal key's owner, the client's network origin including its public address behind NAT or TURN, its self-reported device details, verification code, DTLS fingerprint, and the OS account it ran as), when (submit and first-keystroke times), and what (the command and the program it was typed into). The host derives the person from which personal key decrypted the join request, so a client cannot claim to be someone else. A holder of a *shared* link is only identified by network details, so use personal keys (`setu service key add`, `--no-shared-key`) when you need to know who did what. |
| Service (`setu service`) | Persistent links are long-lived credentials, so profiles admit only paired devices by default (or, with `--auth password`, require a 12+ character password). Only PBKDF2 verifiers (salt, iterations, key) of passwords and pairing codes are stored, never the secrets. Keys expire after 90 days unless configured otherwise, `rotate` invalidates old links immediately, and `max-clients` defaults to 1. The config file must be mode 0600 (setu refuses it otherwise). On Windows it lives in non-roaming `%LOCALAPPDATA%`. Profile commands are pinned to absolute paths. A lock file keeps two supervisors from answering for the same keys. Autostart runs as the user with least privilege: a systemd user unit with `UMask=0077`, a launchd agent with umask 077, or a logon task at `LeastPrivilege`. |
| Session recording | `--record FILE` (and `--record-dir` for service profiles) writes the terminal output as asciicast v2: created with `O_EXCL` (never replacing a file) and mode 0600, written by its own goroutine behind a bounded queue so a slow disk cannot stall the session (at most 8 MiB waits in memory; dropped output is marked in the file, and a write error stops the recording with a warning instead of leaving a silently truncated file). Keystrokes are not recorded; output can still contain secrets, so recordings deserve the same care as the terminal. The path is in the session's audit record. |
| Idle timeout | `--idle-timeout` (shares, desktop, service profiles) disconnects control clients after that long without input (keystrokes, mouse, clipboard, uploads; not window resizes), with a warning a minute before; the disconnect is audited with reason `idle timeout`. It protects against forgotten tabs, not against a modified client that keeps sending harmless messages to stay connected. Off by default, because following an agent's output is legitimate idle time. The `MaxIdleTime` policy caps it: unset becomes the cap, and turning it off or a longer value is refused. |
| File upload | Control clients only, one SCTP data channel per file. The host answers before any data flows, so refused uploads cost nothing. Files go to one folder (`~/Downloads/setu`, `--upload-dir`) under a sanitized base name (no path components, control or bidi characters, Windows-reserved names or leading dots), are created with an exclusive create so they never replace a file, are mode 0600 (never executable), are capped at 2 GiB with at most two in flight per client, must match the announced size exactly, and are deleted if the channel closes early. Each upload is logged and audited with its SHA-256. Uploads are on for control clients by default (a control client can already create files through the shell or desktop); `--no-upload` or the `DisableFileTransfer` policy turns them off, and `AllowedCommands` does not restrict them. |
| Remote desktop (`setu desktop`) | Same signaling, DTLS transport, links, passwords, approval and audit log as terminal shares. Screen updates travel on a second data channel of the same DTLS connection. View-only clients never send input, never receive the clipboard and cannot switch displays; the host enforces this from the link they used. Keys and buttons a client holds are released when it disconnects. Clipboard text is capped at 1 MiB per transfer. Clipboard contents and keystrokes are never logged, only their sizes and counts. Organizations can refuse it with the `DisableDesktop` policy, and a policy with `AllowedCommands` refuses it unless the list includes `@desktop`. |
| Web client | No third-party code at runtime: xterm.js and noble-curves are vendored and checksum-pinned (`web/vendor/SHA256SUMS`, checked in CI). A strict CSP allows only same-origin scripts and `wss:` connections. The page refuses to run inside a frame. |

## What does not protect you

* **Anyone who has a control link (and the password, if set) gets a shell.**
  Don't paste links into chats, issue trackers or AI prompts. Add
  `--password` for anything long-lived, and `--once` or `--expire` for
  one-off use.
* **The hosted web page is code you trust.** Whoever controls the GitHub Pages
  repository could ship JavaScript that leaks secrets. If that matters to you:
  * self-host `web/` (it is static files),
  * pin a fork, or
  * use `setu join` from a terminal instead.
* **A malicious host can send terminal escape sequences** to a `setu join`
  client, the same as with ssh. Join only hosts you trust.
* **Metadata.** Relays and STUN servers learn your IP address and when you
  connect. Relays also see that two pubkeys exchanged a few small messages.
* **Availability.** Relays can drop messages. That causes a failed connection,
  never a compromise. Use `--relay` to choose relays you prefer, or
  `--manual` to use no relays at all.
* **The audit log is evidence, not prevention.** Commands are rebuilt from
  keystrokes, so cursor editing, history and completion can make a recorded
  line differ from what the shell ran (such lines are flagged `edited`).
  Commands that an agent runs by itself are not keystrokes, so setu does not
  see them. Redaction is pattern based and can miss a secret in an unusual
  format. On Windows, ConPTY does not expose the echo state, so password
  prompts rely on redaction alone. Use `--audit-input off` if that is not
  acceptable. A control client has a shell as you and can stop setu, so
  records that were still queued when that happened may be lost.
* **Joining tells the host about your device.** Clients send the host (and
  only the host, inside the encrypted channel) their app version, OS, hostname,
  OS user, timezone, a random per-device id and, in browsers, the user agent
  and screen size, so the host's audit log can say who connected from what.
  `setu join --anonymous` sends none of it. Your network address is visible
  to the host either way, as with any peer-to-peer connection. Device details
  are self-reported and can be faked, so a host should rely on personal keys
  and the verification code for identity, not on them.
* **An always-on profile is an always-open door.** Anyone with its link and
  password can reach your shell whenever the service runs. Prefer short
  `--valid-for` values, rotate keys when a device is lost, and disable
  profiles you are not using.
* **A desktop control link is your whole computer.** Anyone holding it (and
  the password) can do anything you can do at the keyboard and mouse, as you.
  Use `--password`, and `--once` or `--expire` for one-off help sessions.
* **The agent itself.** setu shares the terminal exactly as it is. Run agents
  with the permissions you would grant them locally, for example inside a
  container or a separate user account.

## Reporting a vulnerability

Please open a private security advisory on GitHub (Security → Report a
vulnerability) rather than a public issue.
