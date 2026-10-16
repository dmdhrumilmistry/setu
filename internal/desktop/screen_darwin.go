//go:build darwin

package desktop

import (
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// macScreen uses CoreGraphics for capture and input and AppKit's
// NSPasteboard for the clipboard, called through purego (no cgo).
//
// macOS reports display geometry in points, while captures are in pixels
// (2x on Retina). setu's virtual desktop is in pixels: each display keeps
// its own scale, and input is converted back to points.
type macScreen struct {
	mu       sync.Mutex
	displays []macDisplay
	buttons  int         // pressed mouse buttons (ButtonLeft...)
	pos      image.Point // where setu last moved the pointer (virtual pixels)
	moved    bool
	onPost   func(ev uintptr) // tests: sees every event before it is posted
	flags    uint64           // held modifier flags (kCGEventFlagMask*)
	last     struct {
		at     time.Time
		button int
		x, y   int
		clicks int64
	}

	permMu sync.Mutex
	permAt time.Time // when permOK was last asked
	permOK bool
	asked  bool // the system prompt was shown this run
}

type macDisplay struct {
	Display
	id     uint32
	origin cgPoint // top-left, in points
	scale  float64 // pixels per point
}

type cgPoint struct{ X, Y float64 }
type cgSize struct{ W, H float64 }
type cgRect struct {
	Origin cgPoint
	Size   cgSize
}

const (
	cgEventLeftMouseDown         = 1
	cgEventLeftMouseUp           = 2
	cgEventRightMouseDown        = 3
	cgEventRightMouseUp          = 4
	cgEventMouseMoved            = 5
	cgEventLeftMouseDragged      = 6
	cgEventRightMouseDragged     = 7
	cgEventOtherMouseDown        = 25
	cgEventOtherMouseUp          = 26
	cgEventOtherMouseDragged     = 27
	cgHIDEventTap                = 0
	cgScrollEventUnitLine        = 1
	cgEventScrollWheel           = 22
	cgScrollWheelEventDeltaAxis1 = 11
	cgScrollWheelEventDeltaAxis2 = 12
	cgEventSourceStateHID        = 1 // kCGEventSourceStateHIDSystemState
	cgMouseEventClickState       = 1
	cgMouseEventButtonNumber     = 3
	cgBitmapByteOrderMask        = 0x7000
	cgBitmapByteOrder32Big       = 0x4000
	flagShift                    = 0x20000
	flagControl                  = 0x40000
	flagAlternate                = 0x80000
	flagCommand                  = 0x100000
	doubleClickTime              = 500 * time.Millisecond
	pasteboardString             = "public.utf8-plain-text"
)

var modifierFlags = map[string]uint64{
	"ShiftLeft": flagShift, "ShiftRight": flagShift, "ControlLeft": flagControl, "ControlRight": flagControl,
	"AltLeft": flagAlternate, "AltRight": flagAlternate, "MetaLeft": flagCommand, "MetaRight": flagCommand,
}

var (
	loadOnce sync.Once
	loadErr  error

	cgGetActiveDisplayList          func(max uint32, ids *uint32, count *uint32) int32
	cgMainDisplayID                 func() uint32
	cgDisplayBounds                 func(id uint32) cgRect
	cgDisplayCopyDisplayMode        func(id uint32) uintptr
	cgDisplayModeGetPixelWidth      func(mode uintptr) uintptr
	cgDisplayModeGetPixelHeight     func(mode uintptr) uintptr
	cgDisplayCreateImage            func(id uint32) uintptr
	cgImageGetWidth                 func(img uintptr) uintptr
	cgImageGetHeight                func(img uintptr) uintptr
	cgImageGetBytesPerRow           func(img uintptr) uintptr
	cgImageGetBitsPerPixel          func(img uintptr) uintptr
	cgImageGetBitmapInfo            func(img uintptr) uint32
	cgImageGetDataProvider          func(img uintptr) uintptr
	cgDataProviderCopyData          func(provider uintptr) uintptr
	cfDataGetBytePtr                func(data uintptr) uintptr
	cfDataGetLength                 func(data uintptr) int
	cfRelease                       func(ref uintptr)
	cgEventCreate                   func(source uintptr) uintptr
	cgEventGetLocation              func(event uintptr) cgPoint
	cgEventCreateMouseEvent         func(source uintptr, typ uint32, pos cgPoint, button uint32) uintptr
	cgEventCreateKeyboardEvent      func(source uintptr, key uint16, down bool) uintptr
	cgEventKeyboardSetUnicodeString func(event uintptr, n uintptr, s *uint16)
	cgEventSetType                  func(event uintptr, typ uint32)
	cgEventSetFlags                 func(event uintptr, flags uint64)
	cgEventSetIntegerValueField     func(event uintptr, field uint32, value int64)
	cgEventPost                     func(tap uint32, event uintptr)
	cgEventSourceKeyState           func(state int32, key uint16) bool
	cgEventSourceButtonState        func(state int32, button uint32) bool
	axIsProcessTrusted              func() bool

	// Optional: missing on older macOS versions (nil when absent).
	cgEventCreateScrollWheelEvent2 func(source uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr // macOS 13
	cgPreflightScreenCaptureAccess func() bool                                                                // macOS 10.15
	cgRequestScreenCaptureAccess   func() bool
	cgPreflightPostEventAccess     func() bool // macOS 10.15
	cgRequestPostEventAccess       func() bool
	isSecureEventInputEnabled      func() bool // Carbon
)

func load() error {
	loadOnce.Do(func() {
		lib := func(path string) uintptr {
			h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil && loadErr == nil {
				loadErr = fmt.Errorf("load %s: %w", path, err)
			}
			return h
		}
		cg := lib("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
		cf := lib("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
		as := lib("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
		carbon := lib("/System/Library/Frameworks/Carbon.framework/Carbon")
		lib("/System/Library/Frameworks/AppKit.framework/AppKit") // NSPasteboard
		if loadErr != nil {
			return
		}
		for _, f := range []struct {
			fn       any
			lib      uintptr
			name     string
			optional bool
		}{
			{&cgGetActiveDisplayList, cg, "CGGetActiveDisplayList", false},
			{&cgMainDisplayID, cg, "CGMainDisplayID", false},
			{&cgDisplayBounds, cg, "CGDisplayBounds", false},
			{&cgDisplayCopyDisplayMode, cg, "CGDisplayCopyDisplayMode", false},
			{&cgDisplayModeGetPixelWidth, cg, "CGDisplayModeGetPixelWidth", false},
			{&cgDisplayModeGetPixelHeight, cg, "CGDisplayModeGetPixelHeight", false},
			{&cgDisplayCreateImage, cg, "CGDisplayCreateImage", false},
			{&cgImageGetWidth, cg, "CGImageGetWidth", false},
			{&cgImageGetHeight, cg, "CGImageGetHeight", false},
			{&cgImageGetBytesPerRow, cg, "CGImageGetBytesPerRow", false},
			{&cgImageGetBitsPerPixel, cg, "CGImageGetBitsPerPixel", false},
			{&cgImageGetBitmapInfo, cg, "CGImageGetBitmapInfo", false},
			{&cgImageGetDataProvider, cg, "CGImageGetDataProvider", false},
			{&cgDataProviderCopyData, cg, "CGDataProviderCopyData", false},
			{&cfDataGetBytePtr, cf, "CFDataGetBytePtr", false},
			{&cfDataGetLength, cf, "CFDataGetLength", false},
			{&cfRelease, cf, "CFRelease", false},
			{&cgEventCreate, cg, "CGEventCreate", false},
			{&cgEventGetLocation, cg, "CGEventGetLocation", false},
			{&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent", false},
			{&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent", false},
			{&cgEventKeyboardSetUnicodeString, cg, "CGEventKeyboardSetUnicodeString", false},
			{&cgEventSetType, cg, "CGEventSetType", false},
			{&cgEventSetFlags, cg, "CGEventSetFlags", false},
			{&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField", false},
			{&cgEventPost, cg, "CGEventPost", false},
			{&cgEventSourceKeyState, cg, "CGEventSourceKeyState", false},
			{&cgEventSourceButtonState, cg, "CGEventSourceButtonState", false},
			{&axIsProcessTrusted, as, "AXIsProcessTrusted", false},
			{&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2", true},
			{&cgPreflightScreenCaptureAccess, cg, "CGPreflightScreenCaptureAccess", true},
			{&cgRequestScreenCaptureAccess, cg, "CGRequestScreenCaptureAccess", true},
			{&cgPreflightPostEventAccess, cg, "CGPreflightPostEventAccess", true},
			{&cgRequestPostEventAccess, cg, "CGRequestPostEventAccess", true},
			{&isSecureEventInputEnabled, carbon, "IsSecureEventInputEnabled", true},
		} {
			if _, err := purego.Dlsym(f.lib, f.name); err != nil {
				if f.optional {
					continue
				}
				loadErr = fmt.Errorf("this macOS version lacks %s", f.name)
				return
			}
			purego.RegisterLibFunc(f.fn, f.lib, f.name)
		}
	})
	return loadErr
}

// The app macOS asks about is the one that started setu: Terminal, iTerm2,
// VS Code... (its "responsible process"), not setu itself.
var (
	errScreenPermission = errors.New("macOS has not given setu permission to record the screen: allow the app you started setu from (Terminal, iTerm2, ...) in System Settings > Privacy & Security > Screen & System Audio Recording (Screen Recording before macOS 15), then quit and reopen that app")
	errInputPermission  = errors.New("macOS has not given setu permission to control this Mac: allow the app you started setu from (Terminal, iTerm2, ...) in System Settings > Privacy & Security > Accessibility, then quit and reopen that app")
	errSecureInput      = errors.New("macOS is blocking typed input: a password field or Secure Keyboard Entry (in Terminal or iTerm2) is active on the Mac")
)

func open() (Screen, error) {
	if err := load(); err != nil {
		return nil, err
	}
	if cgPreflightScreenCaptureAccess != nil && !cgPreflightScreenCaptureAccess() {
		cgRequestScreenCaptureAccess() // shows the system prompt the first time
		return nil, errScreenPermission
	}
	s := &macScreen{}
	if _, err := s.Displays(); err != nil {
		return nil, err
	}
	s.InputStatus() // asks for the control permission now, so the prompt shows at start
	return s, nil
}

// postAllowed reports whether macOS lets this process post input events:
// the "post event" permission (listed under Accessibility) since macOS
// 10.15, the Accessibility permission before.
func postAllowed() bool {
	if cgPreflightPostEventAccess != nil {
		return cgPreflightPostEventAccess()
	}
	return axIsProcessTrusted()
}

// InputStatus reports whether remote control works, asking macOS for the
// permission (once per run) when it does not. The answer is cached for two
// seconds, so a permission granted while setu runs is noticed quickly
// without asking macOS on every mouse move.
func (s *macScreen) InputStatus() error {
	s.permMu.Lock()
	defer s.permMu.Unlock()
	if time.Since(s.permAt) > 2*time.Second {
		s.permOK, s.permAt = postAllowed(), time.Now()
		if !s.permOK && !s.asked {
			s.asked = true
			if cgRequestPostEventAccess != nil {
				cgRequestPostEventAccess() // adds the app to the Accessibility list and shows the prompt
			}
		}
	}
	if !s.permOK {
		return errInputPermission
	}
	return nil
}
func (s *macScreen) Displays() ([]Display, error) {
	var ids [16]uint32
	var n uint32
	if r := cgGetActiveDisplayList(uint32(len(ids)), &ids[0], &n); r != 0 || n == 0 {
		return nil, fmt.Errorf("no display found (CGGetActiveDisplayList error %d)", r)
	}
	main := cgMainDisplayID()
	var out []macDisplay
	for i, id := range ids[:n] {
		b := cgDisplayBounds(id)
		pw, ph := b.Size.W, b.Size.H
		if mode := cgDisplayCopyDisplayMode(id); mode != 0 {
			pw, ph = float64(cgDisplayModeGetPixelWidth(mode)), float64(cgDisplayModeGetPixelHeight(mode))
			cfRelease(mode)
		}
		scale := 1.0
		if b.Size.W > 0 {
			scale = pw / b.Size.W
		}
		out = append(out, macDisplay{
			Display: Display{
				Name: fmt.Sprintf("display-%d", i+1), Primary: id == main,
				X: int(math.Round(b.Origin.X * scale)), Y: int(math.Round(b.Origin.Y * scale)),
				W: int(pw), H: int(ph),
			},
			id: id, origin: b.Origin, scale: scale,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Primary && !out[j].Primary })
	s.mu.Lock()
	s.displays = out
	s.mu.Unlock()
	ds := make([]Display, len(out))
	for i, d := range out {
		ds[i] = d.Display
	}
	return ds, nil
}

func (s *macScreen) find(d Display) (macDisplay, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.displays {
		if m.Display == d {
			return m, true
		}
	}
	return macDisplay{}, false
}

func (s *macScreen) Capture(d Display, img *image.RGBA) error {
	m, ok := s.find(d)
	if !ok {
		return errors.New("display is gone")
	}
	cgImg := cgDisplayCreateImage(m.id)
	if cgImg == 0 {
		return errScreenPermission
	}
	defer cfRelease(cgImg)
	if bpp := cgImageGetBitsPerPixel(cgImg); bpp != 32 {
		return fmt.Errorf("unsupported pixel format (%d bits per pixel)", bpp)
	}
	data := cgDataProviderCopyData(cgImageGetDataProvider(cgImg))
	if data == 0 {
		return errors.New("could not read the captured pixels")
	}
	defer cfRelease(data)
	w, h := min(int(cgImageGetWidth(cgImg)), d.W), min(int(cgImageGetHeight(cgImg)), d.H)
	stride := int(cgImageGetBytesPerRow(cgImg))
	src := unsafe.Slice((*byte)(ptr(cfDataGetBytePtr(data))), cfDataGetLength(data))
	argb := cgImageGetBitmapInfo(cgImg)&cgBitmapByteOrderMask == cgBitmapByteOrder32Big
	for y := range h {
		row, dst := src[y*stride:], img.Pix[y*img.Stride:]
		for x := range w {
			p := row[x*4 : x*4+4]
			if argb { // A R G B
				dst[x*4], dst[x*4+1], dst[x*4+2] = p[1], p[2], p[3]
			} else { // B G R A (32-bit little endian, the usual layout)
				dst[x*4], dst[x*4+1], dst[x*4+2] = p[2], p[1], p[0]
			}
			dst[x*4+3] = 0xff
		}
	}
	return nil
}

// ptr converts an address returned by a C function.
func ptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// toPoint converts virtual desktop pixels to global points.
func (s *macScreen) toPoint(x, y int) cgPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.displays {
		if image.Pt(x, y).In(m.Rect()) {
			return cgPoint{m.origin.X + float64(x-m.X)/m.scale, m.origin.Y + float64(y-m.Y)/m.scale}
		}
	}
	return cgPoint{float64(x), float64(y)}
}

func (s *macScreen) Cursor() (int, int, bool) {
	ev := cgEventCreate(0)
	if ev == 0 {
		return 0, 0, false
	}
	p := cgEventGetLocation(ev)
	cfRelease(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.displays {
		if p.X >= m.origin.X && p.Y >= m.origin.Y && p.X < m.origin.X+float64(m.W)/m.scale && p.Y < m.origin.Y+float64(m.H)/m.scale {
			return m.X + int((p.X-m.origin.X)*m.scale), m.Y + int((p.Y-m.origin.Y)*m.scale), true
		}
	}
	return 0, 0, false
}

func (s *macScreen) post(ev uintptr) error {
	if ev == 0 {
		return errors.New("could not create the input event")
	}
	defer cfRelease(ev)
	if err := s.InputStatus(); err != nil {
		return err // macOS would drop the event silently
	}
	s.mu.Lock()
	flags, hook := s.flags, s.onPost
	s.mu.Unlock()
	cgEventSetFlags(ev, flags)
	if hook != nil {
		hook(ev)
	}
	cgEventPost(cgHIDEventTap, ev)
	return nil
}

func (s *macScreen) MoveMouse(x, y int) error {
	p := s.toPoint(x, y)
	s.mu.Lock()
	b := s.buttons
	s.pos, s.moved = image.Pt(x, y), true
	s.mu.Unlock()
	// With a button held, macOS expects "dragged" events, or drags do nothing.
	typ, button := uint32(cgEventMouseMoved), uint32(0)
	switch {
	case b&ButtonLeft != 0:
		typ = cgEventLeftMouseDragged
	case b&ButtonRight != 0:
		typ, button = cgEventRightMouseDragged, 1
	case b != 0:
		typ, button = cgEventOtherMouseDragged, 2
	}
	return s.post(cgEventCreateMouseEvent(0, typ, p, button))
}

func (s *macScreen) Button(button int, down bool) error {
	var typ, num uint32
	switch button {
	case ButtonLeft:
		typ, num = uint32(pickType(down, cgEventLeftMouseDown, cgEventLeftMouseUp)), 0
	case ButtonRight:
		typ, num = uint32(pickType(down, cgEventRightMouseDown, cgEventRightMouseUp)), 1
	case ButtonMiddle, ButtonBack, ButtonForward:
		typ = uint32(pickType(down, cgEventOtherMouseDown, cgEventOtherMouseUp))
		num = map[int]uint32{ButtonMiddle: 2, ButtonBack: 3, ButtonForward: 4}[button]
	default:
		return fmt.Errorf("unknown button %d", button)
	}
	// Click where setu last moved the pointer: macOS applies posted events
	// asynchronously, so asking it for the cursor right after a move can
	// still return the old position, and the click would land there.
	s.mu.Lock()
	x, y, moved := s.pos.X, s.pos.Y, s.moved
	s.mu.Unlock()
	if !moved {
		x, y, _ = s.Cursor()
	}
	p := s.toPoint(x, y)
	s.mu.Lock()
	if down {
		s.buttons |= button
		// Clicks in quick succession at the same place are double (triple...)
		// clicks; macOS only treats them so when the event says so.
		l := &s.last
		if l.button == button && time.Since(l.at) < doubleClickTime && abs(l.x-x) < 5 && abs(l.y-y) < 5 {
			l.clicks++
		} else {
			l.clicks = 1
		}
		l.at, l.button, l.x, l.y = time.Now(), button, x, y
	} else {
		s.buttons &^= button
	}
	clicks := s.last.clicks
	s.mu.Unlock()
	ev := cgEventCreateMouseEvent(0, typ, p, min(num, 2))
	if ev != 0 {
		cgEventSetIntegerValueField(ev, cgMouseEventClickState, clicks)
		cgEventSetIntegerValueField(ev, cgMouseEventButtonNumber, int64(num))
	}
	return s.post(ev)
}

func pickType(c bool, a, b int) int {
	if c {
		return a
	}
	return b
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (s *macScreen) Scroll(dx, dy int) error {
	// CoreGraphics: positive wheel 1 scrolls up, positive wheel 2 scrolls left.
	if cgEventCreateScrollWheelEvent2 != nil {
		return s.post(cgEventCreateScrollWheelEvent2(0, cgScrollEventUnitLine, 2, int32(-dy), int32(-dx), 0))
	}
	// Before macOS 13 only the variadic CGEventCreateScrollWheelEvent exists,
	// which cannot be called without cgo: build the same event by hand.
	ev := cgEventCreate(0)
	if ev != 0 {
		cgEventSetType(ev, cgEventScrollWheel)
		cgEventSetIntegerValueField(ev, cgScrollWheelEventDeltaAxis1, int64(-dy))
		cgEventSetIntegerValueField(ev, cgScrollWheelEventDeltaAxis2, int64(-dx))
	}
	return s.post(ev)
}

func (s *macScreen) Key(code string, down bool) error {
	k, ok := lookupKey(code)
	if !ok {
		return fmt.Errorf("unknown key %q", code)
	}
	if f, mod := modifierFlags[code]; mod {
		s.mu.Lock()
		if down {
			s.flags |= f
		} else {
			s.flags &^= f
		}
		s.mu.Unlock()
	}
	if err := s.post(cgEventCreateKeyboardEvent(0, k.mac, down)); err != nil {
		return err
	}
	if down && secureInput() {
		return errSecureInput
	}
	return nil
}

// secureInput reports whether macOS is withholding keystrokes from every
// program but the focused one (a password field, or Secure Keyboard Entry).
func secureInput() bool {
	return isSecureEventInputEnabled != nil && isSecureEventInputEnabled()
}

func (s *macScreen) Type(text string) error {
	for _, r := range text {
		var err error
		switch r {
		case '\r':
			continue
		case '\n':
			err = s.tap("Enter")
		case '\t':
			err = s.tap("Tab")
		default:
			u := utf16.Encode([]rune{r})
			for _, down := range []bool{true, false} {
				ev := cgEventCreateKeyboardEvent(0, 0, down)
				if ev != 0 {
					cgEventKeyboardSetUnicodeString(ev, uintptr(len(u)), &u[0])
				}
				if err = s.post(ev); err != nil {
					break
				}
			}
		}
		if err != nil {
			return err
		}
	}
	if secureInput() {
		return errSecureInput
	}
	return nil
}

func (s *macScreen) tap(code string) error {
	if err := s.Key(code, true); err != nil {
		return err
	}
	return s.Key(code, false)
}

var (
	selGeneralPasteboard  = objc.RegisterName("generalPasteboard")
	selStringForType      = objc.RegisterName("stringForType:")
	selSetStringForType   = objc.RegisterName("setString:forType:")
	selClearContents      = objc.RegisterName("clearContents")
	selChangeCount        = objc.RegisterName("changeCount")
	selStringWithUTF8     = objc.RegisterName("stringWithUTF8String:")
	selUTF8String         = objc.RegisterName("UTF8String")
	selLengthOfBytesUsing = objc.RegisterName("lengthOfBytesUsingEncoding:")
	selNew                = objc.RegisterName("new")
	selDrain              = objc.RegisterName("drain")
)

const nsUTF8StringEncoding = 4

// withPasteboard runs f with the general pasteboard inside an autorelease
// pool, on one OS thread (pools are per thread).
func withPasteboard[T any](f func(pb objc.ID) T) T {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(selNew)
	defer pool.Send(selDrain)
	return f(objc.ID(objc.GetClass("NSPasteboard")).Send(selGeneralPasteboard))
}

func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	id := objc.ID(objc.GetClass("NSString")).Send(selStringWithUTF8, &b[0])
	runtime.KeepAlive(b)
	return id
}

func (s *macScreen) Clipboard() (string, error) {
	return withPasteboard(func(pb objc.ID) string {
		str := pb.Send(selStringForType, nsString(pasteboardString))
		if str == 0 {
			return ""
		}
		n := objc.Send[uintptr](str, selLengthOfBytesUsing, uintptr(nsUTF8StringEncoding))
		p := objc.Send[uintptr](str, selUTF8String)
		if p == 0 || n == 0 {
			return ""
		}
		return string(unsafe.Slice((*byte)(ptr(p)), n))
	}), nil
}

func (s *macScreen) SetClipboard(text string) error {
	if ok := withPasteboard(func(pb objc.ID) bool {
		pb.Send(selClearContents)
		return objc.Send[bool](pb, selSetStringForType, nsString(text), nsString(pasteboardString))
	}); !ok {
		return errors.New("could not set the clipboard")
	}
	return nil
}

func (s *macScreen) ClipboardSeq() uint64 {
	return withPasteboard(func(pb objc.ID) uint64 {
		return uint64(objc.Send[int](pb, selChangeCount)) + 1 // never 0, which means "cannot tell"
	})
}

func (s *macScreen) Close() error { return nil }
