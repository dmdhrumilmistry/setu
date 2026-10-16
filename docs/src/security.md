# Security model

setu gives another device **a live shell on your machine**, often one running an
AI agent with access to your files and credentials. Treat every invite link like
an SSH private key.

## What protects you

| Layer | How |
| --- | --- |
| Invite secret | 256 random bits per share, carried only in the URL fragment (`#k=...`), which browsers never send to the page's web server. The web page removes it from the address bar and history as soon as it loads. |
| Signaling privacy | Every relay message is AES-256-GCM encrypted with a key derived (HKDF-SHA256) from the secret. Relays see a random room id, throwaway public keys, message sizes and timing, nothing else. |
| Signaling integrity | Each message is bound to its room, sender and recipient; nostr events are signed. The host drops stale (more than 2 minutes), replayed and rate-limited messages, and answers from any key other than the one that asked to join. |
| Transport | WebRTC DTLS 1.2+ end to end between the two peers. Nothing else carries terminal bytes. STUN only learns your public address. |
| Verification code | Both sides show a 6-digit code derived from both DTLS certificates. Different codes mean someone is in the middle. |
| Password | A second factor. The host sends a salt, a PBKDF2-SHA256 work factor (200,000 iterations; clients refuse less than 100,000) and a fresh nonce; the client answers with an HMAC bound to both certificates. The password never crosses the wire and a proof cannot be replayed. 5 failures lock the share for 10 minutes; 20 disable the invite. |
| Roles | View-only links are separate secrets. The host decides each client's role from which secret decrypted the request, never from what the client claims. |
| Remote desktop | `setu desktop` uses the same links, passwords, approval and audit log. View-only clients cannot send input, receive the clipboard or switch displays, enforced by the host. Keys and buttons a client holds are released when it leaves. Clipboard contents and keystrokes are never logged, only sizes and counts. The `DisableDesktop` policy turns it off. |
| Admission | `--max-clients`, `--once`, `--expire`, `--approve`. Handshakes must finish within 2 minutes, and at most 3 can be pending. |
| Reconnects | A client whose connection drops (a phone switching between Wi-Fi and mobile data, a laptop waking up) resumes the same session automatically. When it becomes ready the host hands it a random 256-bit resume token over the encrypted data channel; the client keeps it in memory only, and the host stores only a hash. The token stands in for the password and approval on reconnect, is bound to the same invite (role and personal key) and to the client's device key, works once (every reconnect gets a new one) and expires 10 minutes after the connection is lost. A wrong token counts as a failed login. After `--once` or `--expire`, the host keeps listening only for clients holding a live token, and only until the last one expires. Reconnect attempts back off exponentially (with jitter) up to 30 seconds apart. |
| Hygiene | `SETU_PASSWORD`, `SETU_TURN_PASSWORD` and audit collector headers are removed from the environment before the shared command starts. Link files and session directories are owner-only. Slow clients are disconnected after 8 MiB of backlog. |
| Web client | No third-party code at runtime: libraries are vendored and checksum-pinned, a strict Content Security Policy allows only same-origin scripts, and the page refuses to run in a frame. |
| Device keys | Each client signs every connection with a P-256 device key (a non-extractable WebCrypto key in browsers, an owner-only file for `setu join`). The signature covers the host's nonce and both DTLS fingerprints, so it cannot be replayed. With `--approve`, hosts can trust device fingerprints (`--trust-device`, or answering "always"); trust lives in memory for the share. Unattended shares admit only trusted devices. A bad device signature counts as a failed login. |
| Personal keys | With the [service](service.md), each person can have their own key; identity comes from which key decrypted the request. |
| Audit log | Connections, admission decisions and submitted commands go to your collector over TLS, off the machine within about a second. See [Audit logging](audit.md). |
| Managed policy | Administrators can enforce requirements on every device; a broken or tampered policy fails closed. See [MDM and managed policy](mdm.md). |

## What does not protect you

* **Anyone with a control link (and the password, if set) gets a shell.** Do
  not paste links into chats, issue trackers or AI prompts. Use a password for
  anything long-lived, and `--once` or `--expire` for one-off sharing.
* **The hosted web page is code you trust.** Whoever controls the GitHub Pages
  site could ship JavaScript that leaks secrets. If that matters, self-host
  `web/`, pin a fork, or use `setu join` from a terminal.
* **A malicious host can send terminal escape sequences** to a `setu join`
  client, as with ssh. Join only hosts you trust.
* **Metadata.** Relays and STUN servers learn your IP address and when you
  connect. The host learns your network address and, unless you use
  `--anonymous`, your device details.
* **Availability.** Relays can drop messages. That causes a failed connection,
  never a compromise. Use `--relay` to pick relays, or `--manual` to use none.
* **The audit log is evidence, not prevention.** Commands are rebuilt from
  keystrokes; the agent's own actions are not keystrokes; redaction is pattern
  based; and on Windows password prompts rely on redaction alone.
* **A desktop control link is your whole computer.** Anyone holding it (and
  the password) can do anything you can do at the keyboard. Use `--password`,
  and `--once` or `--expire` for one-off help.
* **The agent itself.** setu shares the terminal exactly as it is. Run agents
  with the permissions you would grant them locally.

## Reporting a vulnerability

Please open a private security advisory on GitHub (**Security > Report a
vulnerability**) rather than a public issue.
