# Joining a session

You can join a share from any modern browser, including phones, or from a
terminal with `setu join`.

## From a browser

Open the invite link (or scan the QR code). The page:

1. connects to the host through the relays named in the link,
2. asks for the session password if the host set one,
3. shows the **verification code**: compare it with the code the host
   terminal prints for your connection,
4. opens the terminal.

The secret in the link sits after `#`, which browsers never send to the web
server. The page removes it from the address bar and history as soon as it
loads, and it loads no third-party code.

### On a phone

Below the terminal there is a key bar:

| Key | Sends |
| --- | --- |
| Esc, Tab, Shift+Tab | the key |
| Ctrl | makes the next key a control key (Ctrl, then `r` sends Ctrl+R) |
| ^C, ^D | interrupt, end of input |
| arrows | cursor keys |
| Enter | Enter |
| pencil | opens a compose box |

The **compose box** is for longer text such as prompts. Multi-line text is sent
as a single bracketed paste, so programs like Claude Code treat it as one
message. "press Enter after" submits it right away; Ctrl+Enter (Cmd+Enter on a
Mac) sends from the keyboard.

### Pasting an invite

You can also open the web client directly and paste an invite link, a bare
`k=...` fragment, or a manual-mode offer code into the box on its home screen.

### Uploading files

With a control link you can send files to the shared computer: press
**Upload** (in the terminal's key bar, or the remote desktop toolbar), or drop
files onto the terminal or the remote screen. They are saved in
`~/Downloads/setu` on that computer (the host can choose another folder with
`--upload-dir`, or turn uploads off with `--no-upload`), and the browser shows
where, with a button to copy the path.

* A file never replaces an existing one: a second `notes.txt` is saved as
  `notes (1).txt`.
* Names are cleaned: folders, control characters and names Windows reserves
  are removed, so a file cannot land outside that folder.
* Files are saved readable only by the host's user and never executable;
  an interrupted upload leaves nothing behind. Files can be up to 2 GiB.
* The host's log, and its audit log, record each file with its size and
  SHA-256.
* View-only links cannot upload. Organizations can turn uploads off with the
  `DisableFileTransfer` [policy](mdm.md#settings).

## From a terminal

```sh
setu join 'https://dmdhrumilmistry.github.io/setu/#k=...'
setu join 'k=...'
```

Always quote the link. If the host set a password, setu prompts for it (or reads
`SETU_PASSWORD`). Type `<Enter> ~ .` to detach; `~~` sends a literal tilde at
the start of a line.

| Flag | Effect |
| --- | --- |
| `--timeout 3m` | Give up connecting after this long. |
| `--relay URL` | Override the relays in the link. |
| `--anonymous` | Do not send device details, and use a one-time device key (see below). |
| `--no-remember` | Use a one-time device key, so hosts cannot recognise this device next time. |
| `--verbose` | Log signaling and connection details. |

## Get approved automatically

If the host uses `--approve`, you wait until they let you in. Each setu client
has a **device key** that hosts can trust, so you are let in without waiting:

1. Find your device fingerprint: on the web client's home page under
   **This device**, or with `setu device` in a terminal.
2. Send it to the host. They add `--trust-device <fingerprint>`, or answer
   `a` (always) the next time they approve you.

In the browser, keep **Remember this device** ticked so the same key is used
next time. The private key never leaves your browser and cannot be read by any
script. When a host admits you automatically the status bar shows
**trusted device**. See
[Approve joiners and trust devices](sharing.md#approve-joiners-and-trust-devices).

## Manual mode

If the host used `--manual`, they send you an offer code or link instead:

1. open the offer link, or run `setu join <offer-code>`;
2. send the **answer code** it prints back to the host over any channel;
3. the host pastes it, and the connection opens.

## What the host learns about you

Like any peer-to-peer connection, the host sees your network address. If the
host keeps an [audit log](audit.md), it records:

* your network origin: the address of the connection, your public address as
  your router presents it, and (terminal clients only) your local addresses;
* the verification code and the encryption fingerprint of your connection;
* device details your client reports: app and version, operating system,
  hostname and OS user (terminal), user agent, model, language, timezone and
  screen size (browser), and a random device id kept by your setu installation
  or browser so the host can recognise a returning device;
* the commands you type, unless the host turned command recording off.

`setu join --anonymous` sends no device details. Device details are
self-reported, so hosts never use them for access decisions.

## Common problems

See [Troubleshooting](troubleshooting.md) for connections that never open,
wrong passwords and mismatched verification codes.
