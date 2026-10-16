# Troubleshooting and FAQ

## Connecting

### The browser stays on "connecting" or the join times out

The two peers could not reach each other.

1. Make sure the share is still running on the host (`setu ps` for background
   shares).
2. Check that the link is complete. Links copied from some apps get cut at
   `#` or `&`; scanning the QR code avoids that.
3. Try `--verbose` on both sides to see which step fails: relays, offer and
   answer, or the WebRTC connection.
4. If the host logs "no nostr relay reachable", the network blocks the relays;
   try `--relay` with a relay you can reach, or `--manual`.
5. If signaling works but the connection never opens, both sides are probably
   behind strict NATs or a firewall that blocks UDP. Add a TURN server on the
   host: `setu share --turn turn:turn.example.com:3478 --turn-user NAME` with
   `SETU_TURN_PASSWORD` set.

### "WebRTC connection failed"

Same cause as above: no network path. TURN is the fix for strict networks.

### "connection lost; reconnecting…"

The network path between the two sides broke: the phone changed networks,
went out of coverage or slept, or the host's network dropped. The browser and
`setu join` reconnect by themselves, without asking for the password again,
and the terminal picks up where it stopped. Missed output is replayed, up to
the last 256 KiB. Reconnecting works for 10 minutes after the drop. If it
fails (the share ended or restarted, or the host was offline the whole
time), press **Reconnect** to join again normally, or run `setu join` again.
Shares started with `--manual` cannot reconnect, because they have no
signaling channel. Neither can hosts running a setu version from before
reconnects existed.

A connection that only stalls is noticed within about 15 seconds while the
page is visible. When you come back to a backgrounded tab it is checked within
5 seconds.

### Switching tabs or apps

Moving to another tab or app does not end the session. The page tells the host
that it is in the background, so the host stops expecting its pings: browsers
slow down or freeze background tabs. If the browser does drop the connection
meanwhile, the page waits until you return and then reconnects. The host keeps
the session resumable for an hour in that case, not 10 minutes. On phones,
browsers often discard background tabs and reload them when you come back.
With **Keep a session going** on (the home page, on by default), the reloaded
tab resumes the same session. Press **Leave** to end a session and make the
tab forget it.

### "session full" or "invite no longer valid"

The share reached `--max-clients`, was `--once` and someone already joined, or
it expired (`--expire`), or too many wrong passwords disabled it. Start a new
share.

### "host is temporarily locked after failed logins"

Five wrong passwords lock the share for 10 minutes. Twenty disable the invite.

## Security

### The verification codes do not match

Disconnect immediately. Different codes mean the two ends are not talking to
each other directly: someone is intercepting the connection. Start a new share
with a new link and share it over a channel you trust.

### I leaked a link

* For `setu share`: end the share (`<Enter> ~ .`, or `setu stop <id>`) and
  start a new one; the old link dies with it.
* For the service: `setu service rotate <profile>` invalidates every link at
  once, or `setu service key remove <profile> <person>` revokes one person's.

## Service

### The service does not serve a profile

Run `setu service status` and `setu service list`, then read the log
(`journalctl --user -u setu` on Linux, `service.log` in the state directory on
macOS and Windows). Common causes:

* the profile is `disabled` or `expired` (run `setu service rotate <profile>`);
* auditing is configured but the collector is unreachable (the service fails
  closed until it is back);
* a [managed policy](mdm.md) blocks the profile (the log says which setting);
* `config.json` is readable by other users (`chmod 600` it).

### "setu service is already running"

Only one supervisor may serve a state directory. Stop the other one, or use
`SETU_SERVICE_DIR` for a second, independent set of profiles.

### The service stops when I log out (Linux)

systemd user services stop at logout. Run `loginctl enable-linger $USER` once.

### `claude` is not found by the service

The service resolves commands to absolute paths when you add a profile. If you
installed the tool later or moved it, remove and re-add the profile. On Linux
and macOS, reinstall the service (`setu service install`) from a shell whose
`PATH` contains the tool.

## Audit and policy

### "audit collector rejected the startup probe"

`--audit-required` (or the service default) checks the collector before
sharing. Check the URL, the `OTEL_EXPORTER_OTLP_HEADERS` token, and TLS. Plain
`http://` is refused for remote collectors unless you pass `--audit-insecure`.

### "blocked by your organization's policy (...)"

A managed policy forbids that setting. `setu policy` shows the policy and where
it came from; the key in parentheses tells you which rule applied.

### "managed policy ...: writable by group or others" or "unknown setting"

The policy exists but is unsafe or malformed, so setu refuses to run. Ask your
administrator to fix it; it must be owned by root (or Administrators) and use
only known settings.

## FAQ

**Do I need to run a server?** No. Relays and STUN are public infrastructure,
and you can choose your own or use `--manual` to need neither.

**Can the relays or GitHub see my terminal?** No. Relays carry only encrypted
connection setup messages, and the terminal stream goes directly between the
two peers over DTLS. GitHub Pages only serves the static web page; the secret
in the link's `#fragment` is never sent to it.

**Does it work on Windows?** Yes, for both sharing (Windows 10 1809+) and
joining.

**Can several people join at once?** Yes, up to `--max-clients`. The latest
control client to resize the window sets the terminal size.

**Can I use it without the browser client?** Yes: `setu join` works from any
terminal, and `--web-url=` prints terminal-only links.
