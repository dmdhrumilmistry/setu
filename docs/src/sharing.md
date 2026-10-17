# Sharing a terminal

`setu share` runs a command in a pseudo terminal and lets other devices join
it. Everything after `--` is the command; without one, setu shares your shell.

```sh
setu share [flags] [--] [command [args...]]
```

## Choose who can join

### Passwords

```sh
setu share --password -- claude
```

setu prompts for a password (8 characters or more). Joiners must enter it. The
password never crosses the network: the host sends a challenge and the joiner
answers with a proof bound to that exact encrypted connection. After 5 wrong
passwords the share refuses new joiners for 10 minutes, and after 20 the invite
is disabled for good.

Each device is asked **once**. A joiner whose browser (or `setu join`) has a
device key, which is the default, is remembered by that key after it gives the
password, so joining again later, from a new tab or after the session could
not be resumed, does not ask again. The device key never leaves the device,
so the link alone is still not enough for anyone else. Pass
`--password-every-join` to ask on every new join anyway. Reconnecting after a
dropped connection never asks either way.

For scripts, set `SETU_PASSWORD` instead of being prompted. setu removes it from
the environment before starting the shared command.

### View-only links

```sh
setu share --view-link -- htop
```

Prints a second, independent link whose holders can watch but not type. The
role is decided by which link was used, never by anything the client claims.
View-only clients' keystrokes and resizes are dropped.

### Single use, expiry and limits

| Flag | Effect |
| --- | --- |
| `--once` | The invite stops working after the first successful join. |
| `--expire 30m` | Stop accepting new joiners after this long. Connected clients stay connected. |
| `--max-clients 1` | At most this many connected clients (default 2). |
| `--approve` | Ask on the host terminal before admitting each joiner. See [Approve joiners and trust devices](#approve-joiners-and-trust-devices). |

Combine them for one-off help sessions:

```sh
setu share --password --once --expire 15m --max-clients 1 -- bash
```

## Approve joiners and trust devices

With `--approve`, every joiner waits until you allow them on the host
terminal:

```
[setu] client #2 (role=control) wants to join: shared-control-link (client #2 from 203.0.113.7:51234, device Pixel 8 (Android 15, setu-web), device key 3f2a-91c0-77de-0b4e-a1c9, verification code 058 186)
Allow? [y] once, [a] always for this device, [N] no (60s)
```

* `y` admits this connection only.
* `a` admits it **and trusts the device** for the rest of this share: the
  next time that device joins (after a dropped connection, a phone that went
  to sleep, a page reload) it is approved automatically, without a prompt.
* Anything else, or no answer within 60 seconds, denies it.

### Device keys

Every client has a **device key**: a P-256 key pair created on first use. The
browser keeps it as a non-extractable WebCrypto key (no script, including the
web client, can read the private key); `setu join` keeps it in an owner-only
file. When joining, the client signs a message bound to this exact encrypted
connection, so the host knows which device is connecting and a signature
cannot be replayed. A device is shown by its fingerprint, such as
`3f2a-91c0-77de-0b4e-a1c9`:

* in the browser, on the web client's home page under **This device**;
* in a terminal, with `setu device` (it is also printed by `setu join`).

Trust is held **in memory** by the running share and ends with it. Answering
`a` never writes anything to disk.

### Pre-approve devices

Give the fingerprints of your own devices up front and they are let in
without asking:

```sh
setu share --approve --trust-device 3f2a-91c0-77de-0b4e-a1c9 -- claude
```

`--trust-device` can be repeated or comma separated. Devices not on the list
are still asked about on the host terminal.

### Unattended: only known devices

Without a terminal to answer prompts (background shares, `--headless` under
a service manager), `--approve` with `--trust-device` becomes an allow list:
trusted devices are admitted automatically and **every other device is
refused**.

```sh
setu share -d --password --approve --trust-device 3f2a-91c0-77de-0b4e-a1c9 -- claude
```

Combined with a password, a joiner then needs the link, the password and the
private key of a trusted device.

### In the browser

The web client's home page shows **This device** with its fingerprint and a
**Remember this device** option (on by default):

* remembered: the key is stored in this browser, so hosts that trust it
  recognise it on every visit;
* not remembered (or private browsing): a new one-time key is used for each
  join, so hosts can approve it once but never recognise it again.

**Forget this device** deletes the stored key; hosts that trusted it will ask
again. When a host admits the browser automatically, the status bar shows
**trusted device**.

## Verify who you are talking to

When a client connects, the host terminal prints a line such as:

```
[setu] client #1 connected via nostr, role=control: shared-control-link (client #1 from 203.0.113.7:51234, device alice-laptop (linux/amd64, setu-cli), verification code 058 186)
```

The joiner sees the same 6-digit verification code. The code is derived from
both ends' DTLS certificates: if the codes differ, someone is intercepting the
connection, so disconnect.

## Run in the background

```sh
setu share -d --password -- claude
```

`-d` (or `--background`) prints the links and QR code and gives you your prompt
back. The share keeps running after you close the terminal.

```sh
setu ps                 # list background shares
setu link <id>          # show the links and QR code again
setu logs <id>          # who connected, with verification codes
setu stop <id>          # end one share
setu stop --all         # end all of them
```

Each background share keeps its state, including its links, in a private
directory (`~/.config/setu/sessions/<id>/` on Linux, `~/Library/Application
Support/setu/sessions/<id>/` on macOS, `%AppData%\setu\sessions\<id>\` on
Windows) that is deleted when the share ends. The password reaches the
background process through a pipe, never the command line or environment.

`--manual` and `--approve` need someone at the terminal, so they cannot be
combined with `-d`. For a share that survives reboots, use the
[Always-on service](service.md).

## Headless and scripted shares

| Flag | Effect |
| --- | --- |
| `--headless` | Do not attach this terminal; just serve clients (for tmux, nohup or CI). |
| `--qr=false` | Do not print the QR code. |
| `--link-file PATH` | Also write the invite link(s) to `PATH` with mode 0600. |
| `--verbose` | Log signaling and connection details. |

## No relays: manual mode

```sh
setu share --manual
```

Manual mode sends nothing to nostr relays. setu prints an *offer code* (and a
link containing it). Send it to the joiner, who opens the link or runs
`setu join <offer-code>` and gets an *answer code* back. Paste the answer code
into the host terminal and press Enter. Pasting the answer is itself the
approval. Only STUN is used to find a network path.

## Strict networks: STUN and TURN

Peer-to-peer connections need both sides to reach each other. setu uses
Google's public STUN servers to discover public addresses. When both sides are
behind strict NATs or firewalls that block UDP, add a TURN relay:

```sh
export SETU_TURN_PASSWORD='...'
setu share --turn turn:turn.example.com:3478 --turn-user alice -- claude
```

| Flag | Effect |
| --- | --- |
| `--stun URL` | Use these STUN servers instead of Google's (repeatable). |
| `--turn URL` | Add a TURN server for networks that block peer-to-peer traffic. |
| `--turn-user NAME` | TURN username; the credential comes from `SETU_TURN_PASSWORD`. |

TURN relays see only DTLS-encrypted bytes.

## Choose relays

By default setu uses four public nostr relays: `wss://relay.damus.io`,
`wss://nos.lol`, `wss://relay.primal.net` and `wss://nostr.mom`. They only
carry a few small, encrypted, ephemeral messages per join. To use your own:

```sh
setu share --relay wss://relay.example.com -- claude
```

Custom relays are embedded in the invite link, so joiners use them too.

## Self-host the web client

The browser client is a static page published from this repository to GitHub
Pages. You can host the `web/` directory anywhere that serves static files over
HTTPS and point links at it:

```sh
setu share --web-url https://setu.example.com/ -- claude
setu share --web-url= -- claude      # terminal-only links, no browser URL
```

## Record a session

`--record FILE` records everything the shared terminal shows, as an
[asciicast v2](https://docs.asciinema.org/manual/asciicast/v2/) file you can
replay later, for example to see exactly what an agent did:

```sh
setu share --record ~/agent-run.cast -- claude
asciinema play ~/agent-run.cast      # or drop it on the asciinema web player
```

* The file is new (setu never replaces one) and readable only by you. Keep it
  safe: it holds whatever appeared on screen, which can include secrets.
* Output and window size changes are recorded, keystrokes are not (commands
  are in the [audit log](audit.md)).
* Recording never slows the terminal: if the disk cannot keep up, output is
  left out of the recording and a marker in the file says so.
* For always-on profiles, `setu service add --record-dir DIR` writes a new
  file per session run, named after the profile, the time and the session id.

## Audit what happens

Add `--audit-endpoint https://collector.example.com:4318` to send an audit log
of connections and typed commands to your OpenTelemetry collector. See
[Audit logging](audit.md).

## All flags

See the [CLI reference](cli.md#setu-share).
