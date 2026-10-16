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

## Platforms

| Host | Capture | Input | Clipboard |
| --- | --- | --- | --- |
| Windows 10 / 11, Server 2016+ | GDI, every monitor | `SendInput` (layout independent) | text |
| Linux and the BSDs, X11 session | MIT-SHM (falls back to `GetImage`), RandR monitors | XTEST | text, `CLIPBOARD` selection |
| Linux, Wayland session | not supported yet: log in with an X11 session ("on Xorg") | | |
| macOS | not supported yet | | |

Run `setu desktop` inside your graphical session: as yourself, on the desktop
you want to share (on Linux, `DISPLAY` must be set). It needs no extra
packages, drivers or administrator rights. Any modern browser can join.

## Using it

The toolbar at the bottom of the page:

| Control | What it does |
| --- | --- |
| **Fit** / **1:1** | Fit the screen to the window, or show it at actual size (scroll to pan). |
| **Display** | Share another monitor (control links only; shown when there is more than one). |
| **Sharp / Balanced / Fast** | Image quality. *Fast* suits slow links and phones; *Sharp* suits reading small text. Your choice is remembered. |
| **Send keys...** | Key combinations the browser or your own computer would otherwise catch: Windows/Super, Alt+Tab, Alt+F4, Ctrl+Shift+Esc, Print Screen, Esc. |
| **Keyboard** | Opens the on-screen keyboard (touch devices). |
| **Clipboard** | Text to and from the remote clipboard (see below). |
| **Fullscreen** | In Chrome and Edge, fullscreen also captures Esc, Alt+Tab and the Windows key for the remote computer. |

**Mouse.** Move, click, drag, right-click and scroll as usual. Back and
forward buttons work too.

**Touch.** Tap to click, drag with one finger to drag, press and hold to
right-click, and drag with two fingers to scroll.

**Keyboard.** Keys are sent as physical keys, so the remote computer's
keyboard layout applies, just as if you typed on it. On a Mac, Cmd acts as
Ctrl on the remote computer, so Cmd+C and Cmd+V copy and paste there.

**Clipboard.**

* Press Ctrl+V (Cmd+V) on the remote screen: your local clipboard text goes to
  the remote clipboard first, then the paste happens there.
* Copy something on the remote computer: the text arrives in your browser and
  is put on your local clipboard (or, if the browser does not allow that, a
  prompt offers a Copy button).
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
| `Wayland sessions are not supported yet` | Log out and pick an X11 session ("Ubuntu on Xorg", "Plasma (X11)") on the login screen. |
| `no X11 display: DISPLAY is not set` | Run `setu desktop` from a terminal inside your desktop session, not over plain SSH. |
| "The remote screen cannot be captured right now" | Windows is showing the lock screen or a UAC prompt (the secure desktop). Sharing resumes when it goes away. |
| Clicks do nothing in one window | On Windows, input cannot reach windows running as administrator unless setu runs as administrator too. |
| Blurry text | Choose **Sharp**, or **1:1** to stop scaling. |
| Laggy on a slow link | Choose **Fast**, or lower `--fps`. |
