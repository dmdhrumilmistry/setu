//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"image"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procEnumDisplayMonitors           = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW               = user32.NewProc("GetMonitorInfoW")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procGetCursorInfo                 = user32.NewProc("GetCursorInfo")
	procSendInput                     = user32.NewProc("SendInput")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procOpenClipboard                 = user32.NewProc("OpenClipboard")
	procCloseClipboard                = user32.NewProc("CloseClipboard")
	procEmptyClipboard                = user32.NewProc("EmptyClipboard")
	procGetClipboardData              = user32.NewProc("GetClipboardData")
	procSetClipboardData              = user32.NewProc("SetClipboardData")
	procGetClipboardSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	procCreateCompatibleDC            = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection              = gdi32.NewProc("CreateDIBSection")
	procSelectObject                  = gdi32.NewProc("SelectObject")
	procBitBlt                        = gdi32.NewProc("BitBlt")
	procDeleteObject                  = gdi32.NewProc("DeleteObject")
	procDeleteDC                      = gdi32.NewProc("DeleteDC")
	procGlobalAlloc                   = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                    = kernel32.NewProc("GlobalFree")
	procGlobalLock                    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock                  = kernel32.NewProc("GlobalUnlock")
)

const (
	srccopy              = 0x00CC0020
	dibRGBColors         = 0
	cfUnicodeText        = 13
	gmemMoveable         = 0x0002
	monitorInfoPrimary   = 1
	cursorShowing        = 1
	smXVirtualScreen     = 76
	smYVirtualScreen     = 77
	smCXVirtualScreen    = 78
	smCYVirtualScreen    = 79
	inputMouse           = 0
	inputKeyboard        = 1
	mouseMove            = 0x0001
	mouseLeftDown        = 0x0002
	mouseLeftUp          = 0x0004
	mouseRightDown       = 0x0008
	mouseRightUp         = 0x0010
	mouseMiddleDown      = 0x0020
	mouseMiddleUp        = 0x0040
	mouseXDown           = 0x0080
	mouseXUp             = 0x0100
	mouseWheel           = 0x0800
	mouseHWheel          = 0x1000
	mouseVirtualDesk     = 0x4000
	mouseAbsolute        = 0x8000
	wheelDelta           = 120
	keyExtended          = 0x0001
	keyUp                = 0x0002
	keyUnicode           = 0x0004
	keyScancode          = 0x0008
	vkReturn             = 0x0D
	vkTab                = 0x09
	vkPause              = 0x13
	dpiPerMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type cursorInfo struct {
	Size   uint32
	Flags  uint32
	Cursor windows.Handle
	X, Y   int32
}

type mouseInput struct {
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type keybdInput struct {
	Vk, Scan  uint16
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// INPUT is a tagged union; keyboard input is padded to the mouse size.
type mouseINPUT struct {
	Type uint32
	Mi   mouseInput
}

type keybdINPUT struct {
	Type uint32
	Ki   keybdInput
	_    [8]byte
}

type winScreen struct {
	mu   sync.Mutex
	dc   uintptr // memory DC holding the capture bitmap
	bmp  uintptr
	bits unsafe.Pointer
	w, h int
}

func open(OpenOptions) (Screen, error) {
	// Without DPI awareness Windows scales coordinates and captures for us,
	// which blurs the picture and misplaces clicks.
	if procSetProcessDpiAwarenessContext.Find() == nil {
		procSetProcessDpiAwarenessContext.Call(dpiPerMonitorAwareV2)
	} else if procSetProcessDPIAware.Find() == nil {
		procSetProcessDPIAware.Call()
	}
	s := &winScreen{}
	if _, err := s.Displays(); err != nil {
		return nil, err
	}
	return s, nil
}

// Callbacks can never be freed and their number is limited, so one is
// created for the process; enumMu guards what it collects into.
var (
	enumMu    sync.Mutex
	enumOut   []Display
	monitorCB = syscall.NewCallback(func(hmon, hdc, lprc, data uintptr) uintptr {
		mi := monitorInfoEx{Size: uint32(unsafe.Sizeof(monitorInfoEx{}))}
		if r, _, _ := procGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			enumOut = append(enumOut, Display{
				Name:    windows.UTF16ToString(mi.Device[:]),
				X:       int(mi.Monitor.Left),
				Y:       int(mi.Monitor.Top),
				W:       int(mi.Monitor.Right - mi.Monitor.Left),
				H:       int(mi.Monitor.Bottom - mi.Monitor.Top),
				Primary: mi.Flags&monitorInfoPrimary != 0,
			})
		}
		return 1
	})
)

func (s *winScreen) Displays() ([]Display, error) {
	enumMu.Lock()
	enumOut = nil
	r, _, err := procEnumDisplayMonitors.Call(0, 0, monitorCB, 0)
	out := enumOut
	enumMu.Unlock()
	if r == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("no display attached (is this a service session without a desktop?)")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		return out[i].X < out[j].X || (out[i].X == out[j].X && out[i].Y < out[j].Y)
	})
	return out, nil
}

// ensureBitmap (re)creates the top-down 32-bit DIB section for a w x h capture.
func (s *winScreen) ensureBitmap(screenDC uintptr, w, h int) error {
	if s.dc != 0 && s.w == w && s.h == h {
		return nil
	}
	s.freeBitmap()
	dc, _, err := procCreateCompatibleDC.Call(screenDC)
	if dc == 0 {
		return fmt.Errorf("CreateCompatibleDC: %w", err)
	}
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	var bits unsafe.Pointer
	bmp, _, err := procCreateDIBSection.Call(screenDC, uintptr(unsafe.Pointer(&bi)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 {
		procDeleteDC.Call(dc)
		return fmt.Errorf("CreateDIBSection: %w", err)
	}
	procSelectObject.Call(dc, bmp)
	s.dc, s.bmp, s.bits, s.w, s.h = dc, bmp, bits, w, h
	return nil
}

func (s *winScreen) freeBitmap() {
	if s.dc != 0 {
		procDeleteDC.Call(s.dc)
		procDeleteObject.Call(s.bmp)
		s.dc, s.bmp, s.bits = 0, 0, nil
	}
}

func (s *winScreen) Capture(d Display, img *image.RGBA) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return errors.New("no access to the screen")
	}
	defer procReleaseDC.Call(0, screenDC)
	if err := s.ensureBitmap(screenDC, d.W, d.H); err != nil {
		return err
	}
	if r, _, err := procBitBlt.Call(s.dc, 0, 0, uintptr(d.W), uintptr(d.H), screenDC, uintptr(d.X), uintptr(d.Y), srccopy); r == 0 {
		return fmt.Errorf("BitBlt: %w", err)
	}
	src := unsafe.Slice((*byte)(s.bits), d.W*d.H*4)
	dst := img.Pix
	for i := 0; i+3 < len(src) && i+3 < len(dst); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0xff
	}
	return nil
}

func (s *winScreen) Cursor() (int, int, bool) {
	ci := cursorInfo{}
	ci.Size = uint32(unsafe.Sizeof(ci))
	if r, _, _ := procGetCursorInfo.Call(uintptr(unsafe.Pointer(&ci))); r == 0 {
		return 0, 0, false
	}
	return int(ci.X), int(ci.Y), ci.Flags&cursorShowing != 0
}

func sendMouse(mi mouseInput) error {
	in := mouseINPUT{Type: inputMouse, Mi: mi}
	if n, _, err := procSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in)); n != 1 {
		return fmt.Errorf("SendInput: %w (input to elevated windows or the secure desktop is blocked)", err)
	}
	return nil
}

func sendKeys(ki ...keybdInput) error {
	if len(ki) == 0 {
		return nil
	}
	ins := make([]keybdINPUT, len(ki))
	for i, k := range ki {
		ins[i] = keybdINPUT{Type: inputKeyboard, Ki: k}
	}
	if n, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&ins[0])), unsafe.Sizeof(ins[0])); int(n) != len(ins) {
		return fmt.Errorf("SendInput: %w (input to elevated windows or the secure desktop is blocked)", err)
	}
	return nil
}

func metric(i uintptr) int32 {
	r, _, _ := procGetSystemMetrics.Call(i)
	return int32(r)
}

func (s *winScreen) MoveMouse(x, y int) error {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := max(metric(smCXVirtualScreen), 2), max(metric(smCYVirtualScreen), 2)
	return sendMouse(mouseInput{
		Dx:    (int32(x) - vx) * 65535 / (vw - 1),
		Dy:    (int32(y) - vy) * 65535 / (vh - 1),
		Flags: mouseMove | mouseAbsolute | mouseVirtualDesk,
	})
}

func (s *winScreen) Button(button int, down bool) error {
	var flags, data uint32
	switch button {
	case ButtonLeft:
		flags = pick(down, mouseLeftDown, mouseLeftUp)
	case ButtonRight:
		flags = pick(down, mouseRightDown, mouseRightUp)
	case ButtonMiddle:
		flags = pick(down, mouseMiddleDown, mouseMiddleUp)
	case ButtonBack, ButtonForward:
		flags = pick(down, mouseXDown, mouseXUp)
		data = pick(button == ButtonBack, 1, 2)
	default:
		return fmt.Errorf("unknown button %d", button)
	}
	return sendMouse(mouseInput{Flags: flags, MouseData: data})
}

func pick(c bool, a, b uint32) uint32 {
	if c {
		return a
	}
	return b
}

func (s *winScreen) Scroll(dx, dy int) error {
	if dy != 0 {
		if err := sendMouse(mouseInput{Flags: mouseWheel, MouseData: uint32(int32(-dy * wheelDelta))}); err != nil {
			return err
		}
	}
	if dx != 0 {
		return sendMouse(mouseInput{Flags: mouseHWheel, MouseData: uint32(int32(dx * wheelDelta))})
	}
	return nil
}

func (s *winScreen) Key(code string, down bool) error {
	k, ok := lookupKey(code)
	if !ok {
		return fmt.Errorf("unknown key %q", code)
	}
	up := pick(down, 0, keyUp)
	if k.scan == 0 {
		if code == "Pause" {
			return sendKeys(keybdInput{Vk: vkPause, Flags: up})
		}
		return fmt.Errorf("key %q is not available on Windows", code)
	}
	flags := keyScancode | up
	if k.scan&0xff00 == 0xe000 {
		flags |= keyExtended
	}
	return sendKeys(keybdInput{Scan: k.scan & 0xff, Flags: flags})
}

func (s *winScreen) Type(text string) error {
	var ins []keybdInput
	for _, r := range text {
		switch r {
		case '\r':
			continue
		case '\n':
			ins = append(ins, keybdInput{Vk: vkReturn}, keybdInput{Vk: vkReturn, Flags: keyUp})
			continue
		case '\t':
			ins = append(ins, keybdInput{Vk: vkTab}, keybdInput{Vk: vkTab, Flags: keyUp})
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			ins = append(ins, keybdInput{Scan: u, Flags: keyUnicode}, keybdInput{Scan: u, Flags: keyUnicode | keyUp})
		}
	}
	return sendKeys(ins...)
}

// openClipboard retries: another program may hold the clipboard briefly.
func openClipboard() error {
	var err error
	for range 10 {
		var r uintptr
		if r, _, err = procOpenClipboard.Call(0); r != 0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("OpenClipboard: %w", err)
}

func (s *winScreen) Clipboard() (string, error) {
	// The clipboard is opened per OS thread: stay on one until it is closed.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openClipboard(); err != nil {
		return "", err
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil // no text on the clipboard
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		return "", fmt.Errorf("GlobalLock: %w", err)
	}
	defer procGlobalUnlock.Call(h)
	// Windows text uses CRLF; clients and other hosts use LF.
	return strings.ReplaceAll(windows.UTF16PtrToString((*uint16)(ptr(p))), "\r\n", "\n"), nil
}

func (s *winScreen) SetClipboard(text string) error {
	// The clipboard is opened per OS thread: stay on one until it is closed.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Windows programs expect CRLF line endings; text from Mac and Linux has LF.
	u, err := windows.UTF16FromString(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n"))
	if err != nil {
		return errors.New("clipboard text contains a NUL character")
	}
	if err := openClipboard(); err != nil {
		return err
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	size := uintptr(len(u) * 2)
	h, _, err := procGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return fmt.Errorf("GlobalAlloc: %w", err)
	}
	p, _, err := procGlobalLock.Call(h)
	if p == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("GlobalLock: %w", err)
	}
	copy(unsafe.Slice((*uint16)(ptr(p)), len(u)), u)
	procGlobalUnlock.Call(h)
	if r, _, err := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return fmt.Errorf("SetClipboardData: %w", err)
	}
	return nil // the clipboard owns h now
}

// ptr converts a pointer returned by a system call (GlobalLock).
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func (s *winScreen) ClipboardSeq() uint64 {
	r, _, _ := procGetClipboardSequenceNumber.Call()
	return uint64(r)
}

func (s *winScreen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeBitmap()
	return nil
}
