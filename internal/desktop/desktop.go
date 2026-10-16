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

// InputChecker is implemented by backends that can tell, before any input
// is sent, why remote control would not work (macOS permissions).
type InputChecker interface {
	InputStatus() error
}

// ErrUnsupported is returned by Open on platforms without a backend.
var ErrUnsupported = errors.New("remote desktop is not supported on this platform")

// Open connects to this machine's desktop.
func Open() (Screen, error) { return open() }

// key is a physical key: its Linux evdev code (X11 keycode = evdev + 8), its
// PC set-1 scancode (0xE0xx for extended keys; 0 = not available) and its
// macOS virtual keycode (kVK_*). Keys a Mac keyboard lacks use the standard
// Mac equivalents: Insert is Help, PrintScreen/ScrollLock/Pause are F13-F15
// and NumLock is the keypad's Clear.
type key struct {
	evdev uint16
	scan  uint16
	mac   uint16
}

// keys maps KeyboardEvent.code values to physical keys.
var keys = map[string]key{
	"Escape": {1, 0x01, 0x35}, "Digit1": {2, 0x02, 0x12}, "Digit2": {3, 0x03, 0x13}, "Digit3": {4, 0x04, 0x14},
	"Digit4": {5, 0x05, 0x15}, "Digit5": {6, 0x06, 0x17}, "Digit6": {7, 0x07, 0x16}, "Digit7": {8, 0x08, 0x1a},
	"Digit8": {9, 0x09, 0x1c}, "Digit9": {10, 0x0a, 0x19}, "Digit0": {11, 0x0b, 0x1d}, "Minus": {12, 0x0c, 0x1b},
	"Equal": {13, 0x0d, 0x18}, "Backspace": {14, 0x0e, 0x33}, "Tab": {15, 0x0f, 0x30},
	"KeyQ": {16, 0x10, 0x0c}, "KeyW": {17, 0x11, 0x0d}, "KeyE": {18, 0x12, 0x0e}, "KeyR": {19, 0x13, 0x0f},
	"KeyT": {20, 0x14, 0x11}, "KeyY": {21, 0x15, 0x10}, "KeyU": {22, 0x16, 0x20}, "KeyI": {23, 0x17, 0x22},
	"KeyO": {24, 0x18, 0x1f}, "KeyP": {25, 0x19, 0x23}, "BracketLeft": {26, 0x1a, 0x21}, "BracketRight": {27, 0x1b, 0x1e},
	"Enter": {28, 0x1c, 0x24}, "ControlLeft": {29, 0x1d, 0x3b},
	"KeyA": {30, 0x1e, 0x00}, "KeyS": {31, 0x1f, 0x01}, "KeyD": {32, 0x20, 0x02}, "KeyF": {33, 0x21, 0x03},
	"KeyG": {34, 0x22, 0x05}, "KeyH": {35, 0x23, 0x04}, "KeyJ": {36, 0x24, 0x26}, "KeyK": {37, 0x25, 0x28},
	"KeyL": {38, 0x26, 0x25}, "Semicolon": {39, 0x27, 0x29}, "Quote": {40, 0x28, 0x27}, "Backquote": {41, 0x29, 0x32},
	"ShiftLeft": {42, 0x2a, 0x38}, "Backslash": {43, 0x2b, 0x2a},
	"KeyZ": {44, 0x2c, 0x06}, "KeyX": {45, 0x2d, 0x07}, "KeyC": {46, 0x2e, 0x08}, "KeyV": {47, 0x2f, 0x09},
	"KeyB": {48, 0x30, 0x0b}, "KeyN": {49, 0x31, 0x2d}, "KeyM": {50, 0x32, 0x2e}, "Comma": {51, 0x33, 0x2b},
	"Period": {52, 0x34, 0x2f}, "Slash": {53, 0x35, 0x2c}, "ShiftRight": {54, 0x36, 0x3c},
	"NumpadMultiply": {55, 0x37, 0x43}, "AltLeft": {56, 0x38, 0x3a}, "Space": {57, 0x39, 0x31}, "CapsLock": {58, 0x3a, 0x39},
	"F1": {59, 0x3b, 0x7a}, "F2": {60, 0x3c, 0x78}, "F3": {61, 0x3d, 0x63}, "F4": {62, 0x3e, 0x76}, "F5": {63, 0x3f, 0x60},
	"F6": {64, 0x40, 0x61}, "F7": {65, 0x41, 0x62}, "F8": {66, 0x42, 0x64}, "F9": {67, 0x43, 0x65}, "F10": {68, 0x44, 0x6d},
	"NumLock": {69, 0xe045, 0x47}, "ScrollLock": {70, 0x46, 0x6b}, "Numpad7": {71, 0x47, 0x59}, "Numpad8": {72, 0x48, 0x5b},
	"Numpad9": {73, 0x49, 0x5c}, "NumpadSubtract": {74, 0x4a, 0x4e}, "Numpad4": {75, 0x4b, 0x56}, "Numpad5": {76, 0x4c, 0x57},
	"Numpad6": {77, 0x4d, 0x58}, "NumpadAdd": {78, 0x4e, 0x45}, "Numpad1": {79, 0x4f, 0x53}, "Numpad2": {80, 0x50, 0x54},
	"Numpad3": {81, 0x51, 0x55}, "Numpad0": {82, 0x52, 0x52}, "NumpadDecimal": {83, 0x53, 0x41}, "IntlBackslash": {86, 0x56, 0x0a},
	"F11": {87, 0x57, 0x67}, "F12": {88, 0x58, 0x6f}, "IntlRo": {89, 0x73, 0x5e}, "NumpadEnter": {96, 0xe01c, 0x4c},
	"ControlRight": {97, 0xe01d, 0x3e}, "NumpadDivide": {98, 0xe035, 0x4b}, "PrintScreen": {99, 0xe037, 0x69},
	"AltRight": {100, 0xe038, 0x3d}, "Home": {102, 0xe047, 0x73}, "ArrowUp": {103, 0xe048, 0x7e}, "PageUp": {104, 0xe049, 0x74},
	"ArrowLeft": {105, 0xe04b, 0x7b}, "ArrowRight": {106, 0xe04d, 0x7c}, "End": {107, 0xe04f, 0x77}, "ArrowDown": {108, 0xe050, 0x7d},
	"PageDown": {109, 0xe051, 0x79}, "Insert": {110, 0xe052, 0x72}, "Delete": {111, 0xe053, 0x75}, "NumpadEqual": {117, 0x59, 0x51},
	"Pause": {119, 0, 0x71}, "IntlYen": {124, 0x7d, 0x5d}, "MetaLeft": {125, 0xe05b, 0x37}, "MetaRight": {126, 0xe05c, 0x36},
	"ContextMenu": {127, 0xe05d, 0x6e},
}

// lookupKey returns the physical key for a KeyboardEvent.code.
func lookupKey(code string) (key, bool) {
	k, ok := keys[code]
	return k, ok
}
