# Always-on service

`setu share` links last as long as the share. The setu **service** keeps
shares available all the time: it starts at login, serves every enabled
*profile*, restarts the shared program when it exits, and keeps each profile's
links the same across reboots.

## Concepts

**Profile.** A command plus its persistent invite keys and settings, for
example "`claude` in `~/projects/app`". Each profile has a name.

**Personal key.** A link that belongs to one named person. The service knows
who connected from which key decrypted their request, so the
[audit log](audit.md) names them. Revoking one person's key affects nobody else.

**Shared link.** A link anyone can hold; audit records can only say
"someone with the shared link". Use personal keys when you need to know who did
what.

## Create a profile

```sh
setu service add --person alice --person bob --no-shared-key \
  --dir ~/projects/app claude -- claude
```

setu resolves `claude` to an absolute path, prints each person's link and QR
code once, prints a one-time **pairing code**, and starts the service (it also
registers it to start at login, so there is nothing else to set up). Send each
person only their own link.

By default a profile admits only **paired devices**: open the link on your
phone or laptop, enter the pairing code once, and from then on that device is
recognised by its device key and never asks for anything again. See
[Devices and pairing](#devices-and-pairing).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--person NAME` | none | Create a personal control link for NAME (repeatable). |
| `--no-shared-key` | off | No shared control link; only people with personal keys can type. |
| `--view-link` | off | Also create a shared read-only link. |
| `--valid-for 720h` | `2160h` (90 days) | Keys stop working after this long; `0` means never. |
| `--max-clients N` | 1 | Concurrent clients. |
| `--dir PATH` | the service's (usually your home) | Working directory for the command. |
| `--auth MODE` | `device` | How new devices get in: `device` (pair once with a one-time code), `password` (a 12+ character password, asked once per device) or `none` (the link alone; not recommended). |
| `--pair-for 15m` | `15m` | With `--auth device`: how long the first pairing code works. |
| `--password-every-join` | off | With `--auth password`: ask for the password on every join instead of once per device. |
| `--no-password` | off | Same as `--auth none`. |
| `--password-stdin` | off | `--auth password`, reading the password from stdin (or set `SETU_PASSWORD`). |
| `--no-install` | off | Do not start the service or register it to start at login. |
| `--relay`, `--stun`, `--turn`, `--turn-user`, `--web-url` | as for `setu share` | Networking. |

Profile names use 1 to 32 lowercase letters, digits, `-` or `_`. The service
stores only PBKDF2 verifiers of passwords and pairing codes, never the secrets
themselves.

## Devices and pairing

A link is something you can lose; a device key is not something anyone can
copy. Every browser and `setu join` signs each connection with its own P-256
device key (in the browser a non-extractable WebCrypto key in IndexedDB, which
even the page cannot read), so the service can tell your devices apart from
someone who merely obtained the link.

```sh
setu service pair claude                   # a new code for one more device (valid 15 minutes)
setu service pair --for 1h claude alice    # ...showing only Alice's link
setu service device list claude            # fingerprint, name, link, how, added, last seen
setu service device add --name tablet claude 3f2a-91c0-77de-0b4e-a1c9   # admit a device without a code
setu service device remove claude 3f2a-91c0-77de-0b4e-a1c9               # disconnected within seconds
setu service device remove --all claude
setu service auth claude password          # switch to a password (asked once per device)
setu service auth claude device            # back to paired devices; known devices keep working
```

* A pairing code is 12 random characters (60 bits), works for **one** device,
  expires (15 minutes by default), and is checked with the same
  challenge-response as a password, so it never crosses the network. Wrong
  codes count toward the lockout (5 failures lock the profile for 10 minutes).
* A paired device is tied to the link it paired through: a device paired via
  the view link cannot use the control link, and removing a person's key
  (`setu service key remove`) also removes their devices.
* Removing a device disconnects it at once and invalidates its reconnect token.
* A device's fingerprint is shown on the web client's home page, and by
  `setu device` on the command line, so you can add one without a code.
* In `password` mode, a device that gives the password is remembered the same
  way (`--password-every-join` turns that off). Changing the password
  (`setu service rotate --password`, `setu service auth NAME password`) forgets
  every device remembered by the old one.

Devices live in `devices.json` next to `config.json` (owner-only). Changes
apply to the running service immediately, without restarting the command.

## Always-on remote desktop

A profile can share the whole desktop instead of a command, so you can reach
a machine any time it is logged in:

```sh
setu service add --desktop --person alice office-pc   # prompts for a password, prints Alice's link
setu service install
```

It gets everything a terminal profile has (password, personal keys, expiry,
`rotate`, audit log) and works like [`setu desktop`](desktop.md) in the
browser. `--display N` picks a monitor and `--fps N` caps the frame rate. What
each system needs:

| System | Before it works |
| --- | --- |
| Windows | The service runs in your session after you log in. A locked session cannot be seen or controlled (Windows allows no program to), so keep it logged in and unlocked. |
| macOS | The service runs the setu binary itself, not your terminal, so allow **setu** under Screen & System Audio Recording and Accessibility (macOS asks when it starts). The permissions belong to that exact binary: allow them again after updating setu. |
| Linux, BSD | Run `service add --desktop` in a terminal inside your X11 session: it records that session's `DISPLAY` and `XAUTHORITY`, which the service manager does not pass on. Wayland is not supported yet. |

If the desktop is not available yet (just after boot, before you log in), the
service retries with a growing delay, like a command that exits.

Long-lived links are a good place for `--idle-timeout 30m`: a control
client that sends no input for that long is disconnected (after a warning),
so a browser tab left open does not stay connected to your machine.

## Start it at login

`setu service add` does this for you the first time (unless you pass
`--no-install`). To do it by hand:

```sh
setu service install
```

| OS | What install does | Logs |
| --- | --- | --- |
| Linux | systemd **user** unit `~/.config/systemd/user/setu.service`, enabled and started | `journalctl --user -u setu -f` |
| macOS | launchd agent `~/Library/LaunchAgents/io.github.dmdhrumilmistry.setu.plist` | `service.log` in the state directory |
| Windows | Task Scheduler task `setu`, at logon, least privilege, no time limit, hidden window | `service.log` in the state directory |

On Linux, user services stop when you log out. To keep them running and start
them at boot, run `loginctl enable-linger $USER` once.

On Linux and macOS the service runs with the `PATH` of the shell you installed
it from, so tools like `claude` are found; on Windows it uses your normal user
environment. `setu service install --no-start` registers it without
starting it now; `setu service uninstall` removes it and stops all profiles.

Prefer your own supervisor? Run `setu service run` in the foreground under it.

## Manage profiles

The running service picks up every change within a few seconds; you never need
to restart it.

```sh
setu service list                          # profiles, state, expiry
setu service status                        # autostart, supervisor, audit, policy
setu service link claude                   # all links again (+ QR)
setu service link claude alice             # only Alice's link

setu service key add claude carol          # give Carol her own control link
setu service key add --view claude dave    # Dave can watch only
setu service key list claude
setu service key remove claude alice       # revoke Alice; nobody else is affected

setu service rotate claude                 # new keys for everyone; old links stop working
setu service rotate --password claude      # ...and a new password
setu service disable claude                # stop serving, keep the profile
setu service enable claude
setu service remove claude                 # delete the profile and its keys
```

When keys expire the profile stops being served and `setu service list` shows
it as `expired`; `setu service rotate` issues fresh keys with a new expiry.

## Audit the service

```sh
export OTEL_EXPORTER_OTLP_HEADERS='Authorization=Bearer%20<ingest-token>'
setu service audit --endpoint https://otel.example.com:4318
```

setu sends a test record first and saves the settings only if the collector
accepts it. For the service, auditing is **required** by default: if the
collector becomes unreachable when the service starts, it serves nothing until
the collector is back. Credentials are stored in the service configuration
(an owner-only file), never on the command line. Use `--headers-stdin` to type
them instead of using the environment. `setu service audit` with no flags shows
the current settings; `--off` disables auditing. See [Audit logging](audit.md).

## Where things live

| | Linux | macOS | Windows |
| --- | --- | --- | --- |
| State directory | `~/.config/setu/service/` | `~/Library/Application Support/setu/service/` | `%LOCALAPPDATA%\setu\service\` |

The directory holds `config.json` (profiles, keys, password verifiers and audit
settings), `devices.json` (the devices each profile admits and pending pairing
code verifiers), `supervisor.pid` (the single-instance lock) and `service.log`.
`config.json` contains secrets: setu refuses to load it if other users can read
it (`chmod 600` fixes that). On Windows it lives in non-roaming
`%LOCALAPPDATA%`. Set `SETU_SERVICE_DIR` to use another directory.

## Safety rules the service applies

* Only paired devices get in by default; pairing codes are single use and
  expire. Passwords, if you choose them, must be 12+ characters.
* Keys expire (90 days by default).
* At most one client by default.
* `--once` and `--approve` are not available: nobody is at the host to answer.
* Commands are pinned to absolute paths, so a directory added to `PATH` later
  cannot substitute another program.
* Only one supervisor can serve a state directory at a time.
* An [MDM policy](mdm.md) can tighten all of this; the service re-checks every
  profile against it before serving.
