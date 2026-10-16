# Remote desktop

`setu desktop` shares this computer's whole desktop with a browser: the
screen, the mouse, the keyboard and the clipboard. It uses the same
peer-to-peer connection, invite links, passwords, approval and audit log as
[`setu share`](sharing.md), so there is still no server of yours in the middle.

```sh
setu desktop --password              # prints a link and a QR code; open it anywhere
setu desktop --password --view-link  # also a watch-only link
setu desktop -d --password           # keep serving in the background
```

Open the printed link on another computer, a tablet or a phone, enter the
password, and compare the verification code with the one the host prints.

For a desktop you can reach at any time, create an
[always-on remote desktop](service.md#always-on-remote-desktop) profile with
`setu service add --desktop`.

## Platforms

| Host | Capture | Input | Clipboard |
| --- | --- | --- | --- |
| Windows 10 / 11, Server 2016+ | GDI, every monitor | `SendInput` (layout independent) | text |
| macOS 12+ | CoreGraphics, every display, full Retina resolution | CoreGraphics events | text |
| Linux and the BSDs, X11 session | MIT-SHM (falls back to `GetImage`), RandR monitors | XTEST | text, `CLIPBOARD` selection |
| Linux, Wayland session | not supported yet: log in with an X11 session ("on Xorg") | | |

Run `setu desktop` inside your graphical session: as yourself, on the desktop
you want to share (on Linux, `DISPLAY` must be set). It needs no extra
packages, drivers or administrator rights. Any modern browser can join.

### macOS permissions

macOS grants two permissions, and it grants them to the app you start setu
from (Terminal, iTerm2, VS Code, ...), not to setu itself:

| Permission | Needed for | Where (System Settings > Privacy & Security) |
| --- | --- | --- |
| Screen Recording | seeing the screen | **Screen & System Audio Recording** on macOS 15 and later, **Screen Recording** on macOS 12 to 14 |
| Accessibility | controlling the mouse and keyboard | **Accessibility** (on every version) |

Input Monitoring is not needed: it is for reading other apps' keystrokes,
not for sending them.

What happens the first time:

1. `setu desktop` asks macOS for both. macOS shows its prompts and adds your
   terminal app to both lists (switched off).
2. Switch your terminal app on in both lists. macOS may ask you to quit and
   reopen the app; do so, and start `setu desktop` again.
3. Until Screen Recording is allowed, `setu desktop` stops with a message
   saying where to allow it. Until Accessibility is allowed, the screen is
   shared but the host prints a warning, and the browser says remote control
   is not available yet, with the same instructions. setu checks again every
   few seconds, so control can start working without a restart.

Good to know:

* On macOS 15 and later, macOS asks again from time to time (about monthly)
  whether the app may keep recording the screen. Answer **Allow**.
* If you start setu from a different app later (say iTerm2 instead of
  Terminal), that app needs the permissions too.
* Over plain SSH there is no app macOS can ask, so capture fails. Start
  `setu desktop` from a terminal on the Mac itself (`-d` keeps it running in
  the background after you close the window).
* While a password field has focus, or **Secure Keyboard Entry** is on in
  Terminal or iTerm2, macOS withholds typed keys from every other program.
  The browser says so; click elsewhere on the Mac, or turn Secure Keyboard
  Entry off in the terminal's menu.
* macOS never lets any app see or control the login window or the lock
  screen.

## Using it

The toolbar at the bottom of the page:

| Control | What it does |
| --- | --- |
| **Fit** / **1:1** | Fit the screen to the window, or show it at actual size (scroll to pan). |
| **Display** | Share another monitor (control links only; shown when there is more than one). |
| **Sharp / Balanced / Fast** | Image quality. *Fast* suits slow links and phones; *Sharp* suits reading small text. Your choice is remembered. |
| **Send keys...** | Key combinations the browser or your own computer would otherwise catch, for the remote computer's system: Cmd+Tab, Spotlight, Force Quit, screenshot and lock on a Mac; the Windows key, Alt+Tab, Alt+F4, Task Manager and lock on Windows; Super, Alt+Tab, a terminal and lock on Linux. |
| **Cmd as Ctrl** / **Ctrl as Cmd** | Shown when exactly one side is a Mac (see Keyboard below). On by default; turn it off to send the keys unchanged. |
| **Keyboard** | Opens the on-screen keyboard (touch devices). |
| **Clipboard** | Text to and from the remote clipboard (see below). |
| **Upload** | Send files to the remote computer; you can also drop them on the screen. See [Uploading files](joining.md#uploading-files). |
| **Fullscreen** | In Chrome and Edge, fullscreen also captures Esc, Alt+Tab and the Windows key for the remote computer. |

**Mouse.** Move, click, drag, right-click and scroll as usual. Back and
forward buttons work too.

**Touch.** Tap to click, drag with one finger to drag, press and hold to
right-click, and drag with two fingers to scroll.

**Keyboard.** Keys are sent as physical keys, so the remote computer's
keyboard layout applies, just as if you typed on it. Shortcuts use your own
habits whatever the remote system is, because when exactly one side is a Mac,
Ctrl and Cmd are swapped:

| You use | Remote computer | Your keys there |
| --- | --- | --- |
| Mac | Windows or Linux | Cmd acts as Ctrl (Cmd+C copies, Cmd+V pastes), Ctrl acts as the Windows/Super key |
| Windows or Linux | Mac | Ctrl acts as Cmd (Ctrl+C copies, Ctrl+V pastes), the Windows key acts as Ctrl |
| Mac | Mac | unchanged |
| Windows or Linux | Windows or Linux | unchanged |

Option and Alt are the same key everywhere. Keys a Mac keyboard does not have
arrive as their Mac equivalents (Insert is Help, Print Screen is F13).

**Clipboard.**

* Press Ctrl+V (Cmd+V on a Mac) on the remote screen: your local clipboard
  text goes to the remote clipboard first, then the paste happens there.
* Press Ctrl+C or Ctrl+X (Cmd on a Mac) on the remote screen: what the remote
  app copies lands on your local clipboard. This works in Safari and Firefox
  too, which only let a page write the clipboard during a keystroke or click.
* Copy something on the remote computer another way (a menu, the mouse): the
  text arrives in your browser and is put on your local clipboard where the
  browser allows it, or a prompt offers a Copy button.
* Line endings are converted between Windows (CRLF) and Mac and Linux (LF).
* The **Clipboard** panel can send text, fetch the remote clipboard, and
  **Type it**: enter the text as keystrokes, for places where pasting does not
  work, such as login prompts.

Clipboard sync is text only, up to 1 MiB, and only for control links.

## Security and logging

Everything from [Security model](security.md) applies, plus:

* **A control link is full control of your computer**, as you. Use
  `--password`, and `--once` or `--expire` for one-off help sessions. With
  `--approve`, the host terminal asks before anyone joins.
* **View-only links** (`--view-link`) can watch the screen but never send
  input, never receive your clipboard, and cannot switch displays. The host
  enforces this from which link was used, not from what the browser claims.
* Keys and mouse buttons a client is holding are released when it
  disconnects, so a dropped connection cannot leave a key stuck down.
* Organizations can turn remote desktop off with the `DisableDesktop`
  [managed policy](mdm.md#settings). When a policy restricts
  `AllowedCommands`, remote desktop is refused unless the list includes
  `@desktop`.

The host log (and `setu logs <id>` for background shares) records who
connected, display switches, clipboard transfers and a summary of each
client's input. With [audit logging](audit.md) on, the same appears as
`setu.desktop.*` records. Clipboard contents and keystrokes are never
recorded, only their sizes and counts, because they often hold passwords.

## How it works

The host captures the shared display up to `--fps` times a second (20 by
default), compares it with the previous capture in 64x64 tiles, and sends only
the tiles that changed: lossless PNG for text and UI, JPEG for photos and
video. Each viewer acknowledges a frame once it has drawn it, and the host
keeps at most two frames in flight per viewer, so a slow network or device
gets fewer, fresher frames instead of growing lag. The screen is captured once
for all viewers, encoded tiles are shared between viewers who need the same
ones, and nothing is captured while nobody is watching. See
[Design and protocol](DESIGN.md#remote-desktop) for the wire format.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `macOS has not given setu permission to record the screen` | Allow your terminal app under Screen & System Audio Recording (see [macOS permissions](#macos-permissions)), then quit and reopen it. |
| On a Mac: the screen shows, but clicks and keys do nothing ("Remote control is not available yet") | Allow your terminal app under Accessibility, then quit and reopen it. If it is already on, switch it off and on again: macOS keeps the permission per app build, and an updated terminal app can lose it. |
| On a Mac: clicks work, typing does not ("macOS is blocking typed input") | A password field or Secure Keyboard Entry is active on the Mac. |
| `Wayland sessions are not supported yet` | Log out and pick an X11 session ("Ubuntu on Xorg", "Plasma (X11)") on the login screen. |
| `no X11 display: DISPLAY is not set` | Run `setu desktop` from a terminal inside your desktop session, not over plain SSH. |
| "The remote screen cannot be captured right now" | Windows is showing the lock screen or a UAC prompt (the secure desktop). Sharing resumes when it goes away. |
| Clicks do nothing in one window | On Windows, input cannot reach windows running as administrator unless setu runs as administrator too. |
| Blurry text | Choose **Sharp**, or **1:1** to stop scaling. |
| Laggy on a slow link | Choose **Fast**, or lower `--fps`. |
