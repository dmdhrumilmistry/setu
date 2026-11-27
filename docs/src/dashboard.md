# Dashboard

`setu dashboard` opens a local web page for everything this computer shares
unattended: the [service](service.md)'s profiles and background shares
(`setu share -d`). It shows who is connected right now, connection analytics,
and the profiles and devices that can get in, and it lets you act on them.

```sh
setu dashboard
```

It prints a link and opens it in your default browser. The dashboard runs
until you press Ctrl-C in that terminal; nothing listens while it is not
running.

## What it shows

**Overview.** How many clients are connected now, connections over the last
24 hours, 7, 30 or 90 days (with the change from the period before), time
connected and the median visit, failed logins, lockouts and refused joins. A
chart of connections per day (or hour), who connects most, the busiest hours
of the day, which profiles or shares get used, and the latest activity.
Problems that need you come first: a profile that is not served because it
breaks the [managed policy](mdm.md), keys that expire within a week or have
expired, a share that is locked after failed logins, a service that is not
running.

**Live sessions.** Every running profile and background share, with each
connected client: the name from its personal key (or which shared link it
used), its role, how it got in, its device and device key, its network
address and whether the connection is direct or relayed, its
[verification code](security.md) and how long it has been connected.

**Profiles and devices.** Every service profile with how new devices get in,
who holds links, when the keys expire and its other settings, and the devices
it knows with when each was added and last seen.

**Activity.** The connection history of the period, filterable by kind of
event and by name, address or share.

## What you can do

| Action | Where | What happens |
| --- | --- | --- |
| Disconnect a client | Live sessions | The client is cut off at once, and its resume token stops working, so it must join and log in again. |
| Stop a background share | Live sessions | Same as `setu stop <id>`: the command ends and its links stop working. |
| Enable or disable a profile | Profiles and devices | Same as `setu service enable` / `disable`. Disabling asks first if anyone is connected. |
| Rotate keys | Profiles and devices | Same as `setu service rotate`: every old link stops working. You type the profile's name to confirm. |
| Pair a device | Profiles and devices | Same as `setu service pair`: a one-time code, shown once, that works for one device for 15 minutes (or less, if a policy says so). |
| Remove a device | Profiles and devices | Same as `setu service device remove`: it can no longer join, and is disconnected within seconds if connected. |

Changes go through the same code and the same managed-policy checks as the
command line, and the running service applies them within a few seconds. Each
change is printed in the dashboard's terminal and recorded in the connection
history. Creating or removing profiles, people's keys and how a profile signs
in stay on the command line.

## Security

The dashboard is a control panel for remote shells, so it is reachable only by
you, at this computer, while it runs:

* **Loopback only.** It listens on `127.0.0.1` (or `--addr` with another
  loopback address), on a random port by default, never on a network address.
* **A one-time link.** The link carries a 256-bit token after `#`, which
  browsers never send to a server. The page trades it for a session cookie and
  removes it from the address bar, and the token then stops working, so a link
  left in scrollback or browser history is useless. Opening it a second time
  shows "already used": run `setu dashboard` again. Five wrong tokens turn
  login off for that run. When setu opens your browser, it passes the link
  through an owner-only file rather than a command line, which other users of
  the computer could read.
* **Sessions end.** The session cookie is `HttpOnly` and `SameSite=Strict`, and
  it stops working after 30 minutes without the page open, after 12 hours, when
  you sign out, or when the dashboard stops.
* **Requests are checked.** Every change must carry the session's CSRF token,
  be JSON and come from the dashboard's own origin. Requests that name any
  other host are refused, which defeats DNS rebinding. The page has a strict
  Content Security Policy, cannot be framed, sends no referrer and is never
  cached.
* **No secrets.** The dashboard never shows links, keys, password verifiers or
  resume tokens. Show links with `setu service link NAME` or `setu link ID`.
  The only secret it shows is a pairing code you just asked for, once.

Other users of the same computer can reach the loopback port, but cannot sign
in without the token. The dashboard can do nothing that you cannot already do
with `setu service` and `setu stop`.

## Connection history

The analytics come from a small connection history that the service and
background shares keep: who connected to what, when, from which address and
device, for how long, and failed logins, lockouts, refused joins, pairings and
dashboard changes. It never holds keystrokes, commands or terminal output (the
[audit log](audit.md) is for those, and it lives off the machine).

It is `history.jsonl` in the service's [state directory](service.md#where-things-live),
readable only by you (setu refuses to read or extend it if other users can).
It is capped at about 2 MiB, with one older file (`history.jsonl.1`) kept, so
it holds the last several thousand events. Delete the files to clear it.

## Live state

The service and each background share answer the dashboard over a local
control socket: `control.sock` in their own owner-only directory (if that path
is too long for a socket, a private per-user directory under the system
temporary directory is used instead). On Linux and macOS a connection from
another user is refused even if the directory's permissions were loosened. A
service started by an older setu has no socket: the dashboard then says it
cannot see live sessions, and `setu service install` restarts the service with
the current version.

## Options

| Flag | Default | Meaning |
| --- | --- | --- |
| `--addr ADDR` | `127.0.0.1:0` | Loopback address and port to listen on; port `0` picks a free one. |
| `--no-open` | off | Print the link without opening a browser (for example over SSH, with the port forwarded). |

To use the dashboard of a computer you reach over SSH, forward the same port
number on both ends (the dashboard accepts only requests addressed to its own
address) and open the printed link on your own computer:

```sh
ssh -L 8765:127.0.0.1:8765 my-computer setu dashboard --addr 127.0.0.1:8765 --no-open
```
