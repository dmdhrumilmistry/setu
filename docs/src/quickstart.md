# Quick start

This takes about two minutes: share a terminal from your computer and open it on
your phone.

## 0. Install

```sh
curl -fsSL https://dmdhrumilmistry.github.io/setu/install.sh | sh
```

On Windows, in PowerShell: `irm https://raw.githubusercontent.com/dmdhrumilmistry/setu/main/install.ps1 | iex`. See [Install](install.md) for other ways.

## 1. Share

On the computer you want to share from:

```sh
setu share --password
```

setu asks you to choose a session password, then prints the invite:

```
setu v1.0.0 - sharing /bin/bash peer-to-peer

CONTROL link - anyone holding it can type into this terminal. Keep it secret:
  browser:  https://dmdhrumilmistry.github.io/setu/#k=Pqv4WrBl...
  terminal: setu join 'k=Pqv4WrBl...'
  [QR code]

Security: password required, max 2 client(s). Compare the verification code shown on both sides.

This terminal is attached to the session. Type <Enter> ~ . to end sharing.
Press Enter to start...
```

Press Enter. Your terminal is now attached to the shared session, so you see
everything the other side does and can type along.

To share one program instead of your shell, put it after `--`:

```sh
setu share --password -- claude
setu share --password -- htop
```

## 2. Join from your phone

Scan the QR code with your phone's camera (or send yourself the browser link
over a channel you trust) and open it.

1. Enter the session password.
2. Compare the **verification code** on your phone with the one the host
   terminal prints for the new client. They must be the same.
3. Type. On a phone, use the key bar for Esc, Tab, Ctrl, arrows and ^C, and the
   pencil button to compose longer text.

## 3. Join from another terminal (optional)

```sh
setu join 'k=Pqv4WrBl...'
```

Quote the link: it contains characters your shell would otherwise interpret.
Type `<Enter> ~ .` to detach.

## 4. End the share

On the host, type `<Enter> ~ .` (Enter, then tilde, then period), or press
Ctrl+C in a headless share. When the shared program exits, the share ends too.

<div class="callout warn">

**Treat the link like a password.** Anyone with a control link (and the
password, if you set one) gets a shell on your machine. Do not paste links into
chats, issue trackers or AI prompts.

</div>

## Next steps

* [Sharing a terminal](sharing.md): view-only links, single-use links, expiry,
  approvals and background shares.
* [Claude Code from your phone](claude-code.md): a safe setup for agents.
* [Always-on service](service.md): a link that keeps working after reboots.
