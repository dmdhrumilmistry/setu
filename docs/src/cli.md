# CLI reference

```
setu share [flags] [--] [command [args...]]   share a command (default: your shell)
setu join  [flags] <link | code>              join from another terminal
setu desktop [flags]                          share this computer's screen, mouse, keyboard and clipboard
setu ps                                       list background shares
setu link <id>                                show a background share's links (+ QR)
setu logs <id>                                show a background share's log
setu stop <id> | --all                        stop background shares
setu service ...                              always-on shares with persistent links
setu policy                                   show the managed (MDM) policy in force
setu device [show | reset]                    this device's key fingerprint (for auto-approval)
setu version                                  version, commit and platform
```

Flags take one or two dashes (`-once` and `--once` are the same). Boolean flags
can be turned off explicitly, for example `--qr=false`.

## setu share

```
setu share [flags] [--] [command [args...]]
```

**Access**

| Flag | Default | Meaning |
| --- | --- | --- |
| `--password` | off | Require a password (prompted, or from `SETU_PASSWORD`). Each device is asked once, then remembered by its device key for the rest of the share. |
| `--password-every-join` | off | With `--password`: ask on every new join instead of once per device. Reconnecting never asks. |
| `--password-stdin` | off | Read the password from the first line of stdin. |
| `--view-link` | off | Also create a read-only invite. |
| `--once` | off | The invite stops working after the first successful join. |
| `--expire DURATION` | `0` (never) | Stop accepting new clients after this long, for example `30m`. |
| `--idle-timeout DURATION` | `0` (never) | Disconnect a control client after this long without input (typing, mouse, clipboard, uploads), for example `15m`. Watching is not input: view-only clients are never disconnected, and a control client that only watches gets a warning a minute before. |
| `--max-clients N` | 2 | Maximum concurrent clients. |
| `--approve` | off | Ask before admitting each client: `y` once, `a` always for that device, `N` no. |
| `--trust-device FP` | none | Device key fingerprint approved without asking (repeatable or comma separated). With `--approve` and no terminal (`-d`, `--headless`), only these devices may join. |

**Running**

| Flag | Default | Meaning |
| --- | --- | --- |
| `-d`, `--background` | off | Serve in the background and return to the shell (see `setu ps`). |
| `--headless` | off | Do not attach this terminal; just serve clients. |
| `--qr` | on | Print a QR code of the browser link. |
| `--notify MODE` | `auto` | Desktop notification on this computer when someone connects, a device pairs, or logins keep failing: `auto` (when this terminal is not attached, as with `-d` or `--headless`), `on` or `off`. |
| `--link-file PATH` | none | Also write the invite link(s) to PATH (mode 0600). |
| `--record FILE` | none | Record the terminal's output to this new file (asciicast v2, mode 0600). `setu share` only. See [Record a session](sharing.md#record-a-session). |
| `--upload-dir PATH` | `~/Downloads/setu` | Where files that control clients upload are saved (see [Uploading files](joining.md#uploading-files)). |
| `--no-upload` | off | Do not accept file uploads. |
| `--verbose` | off | Verbose logging. |

**Networking**

| Flag | Default | Meaning |
| --- | --- | --- |
| `--manual` | off | Copy/paste signaling: no relays; paste the answer code back here. |
| `--relay URL` | four public relays | Nostr relay URL(s), repeatable or comma separated. |
| `--stun URL` | Google STUN | STUN server URL(s). |
| `--turn URL` | none | TURN server for strict NATs, for example `turn:turn.example.com:3478`. |
| `--turn-user NAME` | none | TURN username (credential in `SETU_TURN_PASSWORD`). |
| `--web-url URL` | `https://dmdhrumilmistry.github.io/setu/` | Base URL of the web client; empty prints terminal join commands only. |

**Audit** (see [Audit logging](audit.md))

| Flag | Default | Meaning |
| --- | --- | --- |
| `--audit` | off | Send an audit log, using the `OTEL_EXPORTER_OTLP_*` endpoint. |
| `--audit-endpoint URL` | none | OTLP/HTTP logs URL of your collector. Implies `--audit`. |
| `--audit-required` | off | Refuse to share unless the collector accepts a test record. |
| `--audit-ca FILE` | none | PEM CA bundle that signs the collector's certificate. |
| `--audit-cert FILE` | none | PEM client certificate for mutual TLS. |
| `--audit-key FILE` | none | PEM client key for mutual TLS. |
| `--audit-insecure` | off | Allow plain `http://` to a collector that is not on this machine. |
| `--audit-input MODE` | `lines` | `lines` (commands) or `off` (connections only). |
| `--audit-redact REGEX` | none | Extra pattern to redact from recorded commands (repeatable). |

While attached, type `<Enter> ~ .` on the host to end the share.

## setu join

```
setu join [flags] <link | k=... | offer code>
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--timeout DURATION` | `3m` | Give up connecting after this long. |
| `--relay URL` | from the link | Override the nostr relays. |
| `--anonymous` | off | Do not send device details to the host, and use a one-time device key. |
| `--no-remember` | off | Use a one-time device key kept only in memory, so hosts cannot approve this device automatically next time. |
| `--verbose` | off | Verbose logging. |

Type `<Enter> ~ .` to detach.

## setu desktop

```
setu desktop [flags]
```

Shares this computer's desktop with a browser (see [Remote desktop](desktop.md)).
It takes the same access, networking, audit and background flags as
`setu share` (`--password`, `--view-link`, `--once`, `--expire`, `--approve`,
`--manual`, `-d` and so on), no command and no `--headless` (it never attaches
the terminal), plus:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--display N` | `-1` (primary) | Display to share, by number (0 = first). Control clients can switch displays from the browser. |
| `--notify MODE` | `auto` | As for `setu share`; since the desktop never attaches the terminal, `auto` means on. |
| `--fps N` | 20 | Maximum frames per second, 1 to 60. |
| `--audio` | off | Also share this computer's sound (what it plays, not the microphone). Viewers turn it on with **Sound** in the browser; nothing is captured until one does. See [Sound](desktop.md#sound). |

Press Ctrl-C to end the share.

## Background shares

| Command | Meaning |
| --- | --- |
| `setu ps` | List background shares (id, pid, uptime, command). Aliases: `list`, `ls`. |
| `setu link <id>` | Print the links and QR code again. |
| `setu logs <id>` | Print the share's log: who joined, verification codes. |
| `setu stop <id>` | Stop one share; `setu stop --all` stops all. |

Ids can be abbreviated to any unique prefix.

## setu service

| Command | Meaning |
| --- | --- |
| `setu service add [flags] <name> [--] command...` | Create a profile, print its links and a pairing code once, and start the service. |
| `setu service pair [--for 15m] <name> [person]` | A one-time code that lets one more device join. |
| `setu service device list <name>` | The devices a profile admits. |
| `setu service device add [--name N] [--person P \| --view] <name> <fingerprint>` | Admit a device without a code. |
| `setu service device remove <name> <fingerprint>` / `--all` | Stop admitting a device; it is disconnected at once. |
| `setu service auth [--password-stdin] [--password-every-join] <name> device\|password\|none` | Change how new devices get in. |
| `setu service set [flags] <name>` | Change settings in place: `--idle-timeout`, `--max-clients`, `--device-idle`, `--valid-for` (from the next rotate), `--notify on\|off`, `--audio on\|off` (desktop profiles). Only the flags you pass change. |
| `setu service list` | List profiles. |
| `setu service link <name> [person]` | Show a profile's links (+ QR), or one person's. |
| `setu service key add [--view] <name> <person>` | Give one person their own link. |
| `setu service key list <name>` | List personal keys. |
| `setu service key remove <name> <person>` | Revoke one person's link. |
| `setu service rotate [--password] <name>` | New keys for everyone; old links stop working. |
| `setu service enable` / `disable <name>` | Serve or stop serving a profile. |
| `setu service remove <name>` | Delete a profile and its keys. |
| `setu service audit [flags]` / `--off` | Configure audit logging for the service. |
| `setu service install [--no-start]` | Start the service at login. |
| `setu service uninstall` | Remove autostart and stop the service. |
| `setu service status` | Autostart, supervisor, audit and policy state. |
| `setu service run [--dir D] [--log-file F]` | Run the supervisor in the foreground. |

### setu service add

| Flag | Default | Meaning |
| --- | --- | --- |
| `--person NAME` | none | Give NAME their own control link (repeatable or comma separated). |
| `--no-shared-key` | off | No shared control link; only people with personal keys can type. |
| `--view-link` | off | Also create a shared read-only link. |
| `--valid-for DURATION` | `2160h` | Keys stop working after this long; `0` means never. |
| `--max-clients N` | 1 | Maximum concurrent clients. |
| `--dir PATH` | the service's | Working directory for the command. |
| `--auth MODE` | `device` | `device` (paired devices only), `password` (asked once per device) or `none`. |
| `--pair-for DURATION` | `15m` | With `--auth device`: how long the first pairing code works. |
| `--password-every-join` | off | With `--auth password`: ask on every join. |
| `--no-password` | off | Same as `--auth none`. |
| `--password-stdin` | off | `--auth password`, reading the password from the first line of stdin. |
| `--no-install` | off | Do not start the service or register it to start at login. |
| `--device-idle DURATION` | `2160h` (90 days) | Forget a device that has not joined for this long, so a lost or retired one loses access; `0` means never. |
| `--no-notify` | off | No desktop notification on this computer when someone connects. |
| `--relay URL` | public relays | Nostr relay URL(s). |
| `--stun URL` | Google STUN | STUN server URL(s). |
| `--turn URL` | none | TURN server (credential from `SETU_TURN_PASSWORD`). |
| `--turn-user NAME` | none | TURN username. |
| `--web-url URL` | GitHub Pages | Base URL of the web client. |
| `--no-upload` | off | Do not accept file uploads from clients (they are saved to `~/Downloads/setu` otherwise). |
| `--idle-timeout DURATION` | `0` (never) | Disconnect control clients after this long without input. |
| `--record-dir DIR` | none | Record each session's terminal output to a new file in DIR (terminal profiles only). |
| `--desktop` | off | Share this computer's desktop instead of a command (no command is given). See [always-on remote desktop](service.md#always-on-remote-desktop). |
| `--display N` | `-1` (primary) | With `--desktop`: display to share, by number. |
| `--fps N` | 20 | With `--desktop`: maximum frames per second, 1 to 60. |
| `--audio` | off | With `--desktop`: also share this computer's sound with viewers who turn it on. |

### setu service rotate

| Flag | Meaning |
| --- | --- |
| `--password` | Also set a new password (prompted). |
| `--password-stdin` | Read the new password from stdin. |

### setu service audit

| Flag | Default | Meaning |
| --- | --- | --- |
| `--endpoint URL` | from `OTEL_EXPORTER_OTLP_*` | OTLP/HTTP logs URL of your collector. |
| `--required` | on | Serve nothing unless the collector accepts records. |
| `--ca FILE`, `--cert FILE`, `--key FILE` | none | Pinned CA; mutual TLS. |
| `--insecure` | off | Allow plain `http://` to a remote collector. |
| `--input MODE` | `lines` | `lines` or `off`. |
| `--redact REGEX` | none | Extra redaction pattern (repeatable). |
| `--headers-stdin` | off | Read collector headers (`Name=value,...`) from stdin instead of `OTEL_EXPORTER_OTLP_HEADERS`. |
| `--no-verify` | off | Save without sending a test record. |
| `--off` | | Stop auditing. |

### setu service install and run

| Command | Flag | Meaning |
| --- | --- | --- |
| `install` | `--no-start` | Register autostart without starting the service now. |
| `run` | `--dir PATH` | Service state directory (default: the per-user one). |
| `run` | `--log-file PATH` | Append the supervisor log to PATH instead of stderr. |

## setu device

```sh
setu device          # print this device's key fingerprint (creates the key on first use)
setu device reset    # replace the key; hosts that trusted the old one will ask again
```

The private key is stored owner-only at `<config dir>/setu/device.key`. Give the
fingerprint to a host to use with `setu share --approve --trust-device`. See
[Approve joiners and trust devices](sharing.md#approve-joiners-and-trust-devices).

## setu doctor

```sh
setu doctor              # check this computer's setu setup; every problem comes with the command that fixes it
setu doctor --json       # the same as JSON, for scripts and MDM compliance checks
setu doctor --offline    # skip the check for a newer release
```

It checks the managed policy, whether setu is up to date, whether the service
runs and starts at login, every profile (how new devices get in, shared links,
key expiry, idle timeouts, device expiry, policy violations), devices that
have not joined for a month, and the permissions of this device's key. It
exits with status 1 if anything fails, 0 otherwise (warnings included).

## setu upgrade

```sh
setu upgrade                    # install the latest release (or the version your organization pins)
setu upgrade --check            # only say whether one is available
setu upgrade --version v1.4.0   # install this release (also to roll back)
setu upgrade auto status        # automatic updates by the service: on by default
setu upgrade auto off           # ...or on
```

| Flag | Meaning |
| --- | --- |
| `--check` | Only report whether an update is available. |
| `--version VERSION` | Install this release instead of the latest. |
| `--force` | Also replace a development build (one built from source, without a release version). |

See [Updates](install.md#updates) for how updates are verified and applied.

## setu policy

Prints where the managed policy came from, its hash and its settings (header
values hidden), or "no managed policy on this device". See
[MDM and managed policy](mdm.md).

## Environment variables

| Variable | Used by | Meaning |
| --- | --- | --- |
| `SETU_PASSWORD` | share, join, service add/rotate | Password instead of a prompt. Removed before the shared command starts. |
| `SETU_PAIRING_CODE` | join | Pairing code instead of a prompt, for a device that is new to a service profile. |
| `SETU_TURN_PASSWORD` | share, service add | TURN credential. |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | share `--audit`, service audit | Collector URL when no endpoint flag is given. |
| `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_LOGS_HEADERS` | share, service audit | Collector headers, `Name=value,Name2=value2`, values percent-encoded. |
| `SETU_SERVICE_DIR` | service | Use another state directory. |
| `SETU_SESSION` | set by setu | Set to `1` inside shared commands, so scripts can tell they are shared. |
