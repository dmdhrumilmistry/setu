# CLI reference

```
setu share [flags] [--] [command [args...]]   share a command (default: your shell)
setu join  [flags] <link | code>              join from another terminal
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
| `--password` | off | Require a password (prompted, or from `SETU_PASSWORD`). |
| `--password-stdin` | off | Read the password from the first line of stdin. |
| `--view-link` | off | Also create a read-only invite. |
| `--once` | off | The invite stops working after the first successful join. |
| `--expire DURATION` | `0` (never) | Stop accepting new clients after this long, for example `30m`. |
| `--max-clients N` | 2 | Maximum concurrent clients. |
| `--approve` | off | Ask before admitting each client: `y` once, `a` always for that device, `N` no. |
| `--trust-device FP` | none | Device key fingerprint approved without asking (repeatable or comma separated). With `--approve` and no terminal (`-d`, `--headless`), only these devices may join. |

**Running**

| Flag | Default | Meaning |
| --- | --- | --- |
| `-d`, `--background` | off | Serve in the background and return to the shell (see `setu ps`). |
| `--headless` | off | Do not attach this terminal; just serve clients. |
| `--qr` | on | Print a QR code of the browser link. |
| `--link-file PATH` | none | Also write the invite link(s) to PATH (mode 0600). |
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
| `setu service add [flags] <name> [--] command...` | Create a profile and print its links once. |
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
| `--no-password` | off | Allow joining with the link alone. |
| `--password-stdin` | off | Read the password from the first line of stdin. |
| `--relay URL` | public relays | Nostr relay URL(s). |
| `--stun URL` | Google STUN | STUN server URL(s). |
| `--turn URL` | none | TURN server (credential from `SETU_TURN_PASSWORD`). |
| `--turn-user NAME` | none | TURN username. |
| `--web-url URL` | GitHub Pages | Base URL of the web client. |

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

## setu policy

Prints where the managed policy came from, its hash and its settings (header
values hidden), or "no managed policy on this device". See
[MDM and managed policy](mdm.md).

## Environment variables

| Variable | Used by | Meaning |
| --- | --- | --- |
| `SETU_PASSWORD` | share, join, service add/rotate | Password instead of a prompt. Removed before the shared command starts. |
| `SETU_TURN_PASSWORD` | share, service add | TURN credential. |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`, `OTEL_EXPORTER_OTLP_ENDPOINT` | share `--audit`, service audit | Collector URL when no endpoint flag is given. |
| `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_LOGS_HEADERS` | share, service audit | Collector headers, `Name=value,Name2=value2`, values percent-encoded. |
| `SETU_SERVICE_DIR` | service | Use another state directory. |
| `SETU_SESSION` | set by setu | Set to `1` inside shared commands, so scripts can tell they are shared. |
