// Package desktop shares a computer's screen, mouse, keyboard and clipboard.
//
// A Screen is the platform backend (GDI and SendInput on Windows, X11 with
// MIT-SHM and XTEST on Linux and the BSDs). A Hub captures the screen once
// for every viewer, sends each viewer only the tiles that changed since the
// last frame it acknowledged, and so adapts to each viewer's network and
// decoder speed without buffering.
package desktop

import (
	"errors"
	"image"
)

// Display is one monitor, in virtual desktop coordinates.
type Display struct {
	Name    string
	X, Y    int
	W, H    int
	Primary bool
}

// Rect is the display's area on the virtual desktop.
func (d Display) Rect() image.Rectangle { return image.Rect(d.X, d.Y, d.X+d.W, d.Y+d.H) }

// Mouse buttons, as bits of a PointerEvent.buttons mask.
const (
	ButtonLeft    = 1
	ButtonRight   = 2
	ButtonMiddle  = 4
	ButtonBack    = 8
	ButtonForward = 16
)

// Screen is a platform backend. Coordinates are virtual desktop pixels.
// Methods may be called from several goroutines.
type Screen interface {
	Displays() ([]Display, error)
	// Capture copies the area of d into img (RGBA, d.W x d.H).
	Capture(d Display, img *image.RGBA) error
	// Cursor returns the pointer position and whether it is visible.
	Cursor() (x, y int, visible bool)

	MoveMouse(x, y int) error
	Button(button int, down bool) error
	// Scroll scrolls by whole notches; positive dy scrolls down, positive
	// dx scrolls right.
	Scroll(dx, dy int) error
	// Key presses or releases a physical key named by its KeyboardEvent.code.
	Key(code string, down bool) error
	// Type enters text independently of the keyboard layout.
	Type(text string) error

	Clipboard() (string, error)
	SetClipboard(text string) error
	// ClipboardSeq changes whenever the clipboard may have changed. It
	// returns 0 when the backend cannot tell.
	ClipboardSeq() uint64

	Close() error
}

// ErrUnsupported is returned by Open on platforms without a backend.
var ErrUnsupported = errors.New("remote desktop is not supported on this platform")

// Open connects to this machine's desktop.
func Open() (Screen, error) { return open() }

// key is a physical key: its Linux evdev code (X11 keycode = evdev + 8) and
// its PC set-1 scancode (0xE0xx for extended keys; 0 = not available).
type key struct {
	evdev uint16
	scan  uint16
}

// keys maps KeyboardEvent.code values to physical keys.
var keys = map[string]key{
	"Escape": {1, 0x01}, "Digit1": {2, 0x02}, "Digit2": {3, 0x03}, "Digit3": {4, 0x04}, "Digit4": {5, 0x05},
	"Digit5": {6, 0x06}, "Digit6": {7, 0x07}, "Digit7": {8, 0x08}, "Digit8": {9, 0x09}, "Digit9": {10, 0x0a},
	"Digit0": {11, 0x0b}, "Minus": {12, 0x0c}, "Equal": {13, 0x0d}, "Backspace": {14, 0x0e}, "Tab": {15, 0x0f},
	"KeyQ": {16, 0x10}, "KeyW": {17, 0x11}, "KeyE": {18, 0x12}, "KeyR": {19, 0x13}, "KeyT": {20, 0x14},
	"KeyY": {21, 0x15}, "KeyU": {22, 0x16}, "KeyI": {23, 0x17}, "KeyO": {24, 0x18}, "KeyP": {25, 0x19},
	"BracketLeft": {26, 0x1a}, "BracketRight": {27, 0x1b}, "Enter": {28, 0x1c}, "ControlLeft": {29, 0x1d},
	"KeyA": {30, 0x1e}, "KeyS": {31, 0x1f}, "KeyD": {32, 0x20}, "KeyF": {33, 0x21}, "KeyG": {34, 0x22},
	"KeyH": {35, 0x23}, "KeyJ": {36, 0x24}, "KeyK": {37, 0x25}, "KeyL": {38, 0x26}, "Semicolon": {39, 0x27},
	"Quote": {40, 0x28}, "Backquote": {41, 0x29}, "ShiftLeft": {42, 0x2a}, "Backslash": {43, 0x2b},
	"KeyZ": {44, 0x2c}, "KeyX": {45, 0x2d}, "KeyC": {46, 0x2e}, "KeyV": {47, 0x2f}, "KeyB": {48, 0x30},
	"KeyN": {49, 0x31}, "KeyM": {50, 0x32}, "Comma": {51, 0x33}, "Period": {52, 0x34}, "Slash": {53, 0x35},
	"ShiftRight": {54, 0x36}, "NumpadMultiply": {55, 0x37}, "AltLeft": {56, 0x38}, "Space": {57, 0x39},
	"CapsLock": {58, 0x3a}, "F1": {59, 0x3b}, "F2": {60, 0x3c}, "F3": {61, 0x3d}, "F4": {62, 0x3e},
	"F5": {63, 0x3f}, "F6": {64, 0x40}, "F7": {65, 0x41}, "F8": {66, 0x42}, "F9": {67, 0x43}, "F10": {68, 0x44},
	"NumLock": {69, 0xe045}, "ScrollLock": {70, 0x46}, "Numpad7": {71, 0x47}, "Numpad8": {72, 0x48},
	"Numpad9": {73, 0x49}, "NumpadSubtract": {74, 0x4a}, "Numpad4": {75, 0x4b}, "Numpad5": {76, 0x4c},
	"Numpad6": {77, 0x4d}, "NumpadAdd": {78, 0x4e}, "Numpad1": {79, 0x4f}, "Numpad2": {80, 0x50},
	"Numpad3": {81, 0x51}, "Numpad0": {82, 0x52}, "NumpadDecimal": {83, 0x53}, "IntlBackslash": {86, 0x56},
	"F11": {87, 0x57}, "F12": {88, 0x58}, "IntlRo": {89, 0x73}, "NumpadEnter": {96, 0xe01c},
	"ControlRight": {97, 0xe01d}, "NumpadDivide": {98, 0xe035}, "PrintScreen": {99, 0xe037},
	"AltRight": {100, 0xe038}, "Home": {102, 0xe047}, "ArrowUp": {103, 0xe048}, "PageUp": {104, 0xe049},
	"ArrowLeft": {105, 0xe04b}, "ArrowRight": {106, 0xe04d}, "End": {107, 0xe04f}, "ArrowDown": {108, 0xe050},
	"PageDown": {109, 0xe051}, "Insert": {110, 0xe052}, "Delete": {111, 0xe053}, "NumpadEqual": {117, 0x59},
	"Pause": {119, 0}, "IntlYen": {124, 0x7d}, "MetaLeft": {125, 0xe05b}, "MetaRight": {126, 0xe05c},
	"ContextMenu": {127, 0xe05d},
}

// lookupKey returns the physical key for a KeyboardEvent.code.
func lookupKey(code string) (key, bool) {
	k, ok := keys[code]
	return k, ok
}
