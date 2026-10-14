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
| `--approve` | Ask `y/N` on the host terminal for every joiner, showing their verification code. |

Combine them for one-off help sessions:

```sh
setu share --password --once --expire 15m --max-clients 1 -- bash
```

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

## Audit what happens

Add `--audit-endpoint https://collector.example.com:4318` to send an audit log
of connections and typed commands to your OpenTelemetry collector. See
[Audit logging](audit.md).

## All flags

See the [CLI reference](cli.md#setu-share).
