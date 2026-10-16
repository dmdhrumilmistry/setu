//go:build linux || freebsd || netbsd || openbsd || dragonfly

package desktop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// x11Screen talks the X11 protocol directly (no cgo): MIT-SHM or GetImage
// to capture, XTEST to inject input, and the CLIPBOARD selection.
type x11Screen struct {
	c      *xgb.Conn
	root   xproto.Window
	win    xproto.Window // owns our clipboard selection
	width  int
	height int
	maxReq int // largest request, in bytes
	randr  bool
	xtest  bool
	xfixes bool

	capMu sync.Mutex
	shm   *shmBuf

	kmMu    sync.Mutex
	minKey  xproto.Keycode
	maxKey  xproto.Keycode
	symsPer int
	keymap  []xproto.Keysym
	spare   xproto.Keycode // unmapped keycode used to type other characters
	typeMu  sync.Mutex

	clipboard, utf8, targets, textAtom, incr, prop xproto.Atom

	clipMu  sync.Mutex
	owned   bool
	ownText string
	convMu  sync.Mutex
	notify  chan xproto.SelectionNotifyEvent
	clipSeq atomic.Uint64
}

const (
	evdevOffset  = 8 // X11 keycode = evdev code + 8 (evdev/libinput servers)
	maxClipBytes = 1 << 20
	// maxRemaps bounds the characters one Type call may remap (each waits
	// remapDelay), so one message cannot block input for long.
	maxRemaps  = 256
	remapDelay = 8 * time.Millisecond
)

func open() (Screen, error) {
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" {
		return nil, errors.New("Wayland sessions are not supported yet: log in with an X11 session (for example \"Ubuntu on Xorg\")")
	}
	if os.Getenv("DISPLAY") == "" {
		return nil, errors.New("no X11 display: DISPLAY is not set (run setu desktop inside your graphical session)")
	}
	c, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("connect to X11 display %s: %w", os.Getenv("DISPLAY"), err)
	}
	s, err := newX11(c)
	if err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

func newX11(c *xgb.Conn) (*x11Screen, error) {
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	if err := checkPixelFormat(setup, scr); err != nil {
		return nil, err
	}
	s := &x11Screen{c: c, root: scr.Root, width: int(scr.WidthInPixels), height: int(scr.HeightInPixels),
		maxReq: int(setup.MaximumRequestLength) * 4, minKey: setup.MinKeycode, maxKey: setup.MaxKeycode,
		notify: make(chan xproto.SelectionNotifyEvent, 1)}
	s.xtest = xtest.Init(c) == nil
	s.randr = randr.Init(c) == nil
	if s.randr {
		if v, err := randr.QueryVersion(c, 1, 5).Reply(); err != nil || v.MajorVersion < 1 || (v.MajorVersion == 1 && v.MinorVersion < 5) {
			s.randr = false // monitors need RandR 1.5
		}
	}
	if err := s.loadKeymap(); err != nil {
		return nil, err
	}
	for name, a := range map[string]*xproto.Atom{"CLIPBOARD": &s.clipboard, "UTF8_STRING": &s.utf8, "TARGETS": &s.targets,
		"TEXT": &s.textAtom, "INCR": &s.incr, "SETU_SELECTION": &s.prop} {
		r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
		if err != nil {
			return nil, fmt.Errorf("intern atom %s: %w", name, err)
		}
		*a = r.Atom
	}
	win, err := xproto.NewWindowId(c)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateWindowChecked(c, 0, win, s.root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, 0,
		xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange}).Check(); err != nil {
		return nil, fmt.Errorf("create clipboard window: %w", err)
	}
	s.win = win
	if xfixes.Init(c) == nil {
		if _, err := xfixes.QueryVersion(c, 5, 0).Reply(); err == nil {
			s.xfixes = xfixes.SelectSelectionInputChecked(c, win, s.clipboard, xfixes.SelectionEventMaskSetSelectionOwner).Check() == nil
		}
	}
	s.clipSeq.Store(1)
	go s.events()
	return s, nil
}

// checkPixelFormat accepts the 24/32-bit little-endian BGRX layout every
// current X server uses.
func checkPixelFormat(setup *xproto.SetupInfo, scr *xproto.ScreenInfo) error {
	bad := fmt.Errorf("unsupported X11 pixel format (depth %d); setu needs a 24-bit true-colour display", scr.RootDepth)
	if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
		return bad
	}
	bpp := 0
	for _, f := range setup.PixmapFormats {
		if f.Depth == scr.RootDepth {
			bpp = int(f.BitsPerPixel)
		}
	}
	if bpp != 32 {
		return bad
	}
	for _, d := range scr.AllowedDepths {
		for _, v := range d.Visuals {
			if v.VisualId == scr.RootVisual {
				if v.RedMask == 0xff0000 && v.GreenMask == 0xff00 && v.BlueMask == 0xff {
					return nil
				}
				return bad
			}
		}
	}
	return bad
}

func (s *x11Screen) Displays() ([]Display, error) {
	if geo, err := xproto.GetGeometry(s.c, xproto.Drawable(s.root)).Reply(); err == nil {
		s.width, s.height = int(geo.Width), int(geo.Height)
	}
	if s.randr {
		if r, err := randr.GetMonitors(s.c, s.root, true).Reply(); err == nil && len(r.Monitors) > 0 {
			var out []Display
			for _, m := range r.Monitors {
				name := fmt.Sprintf("monitor-%d", len(out)+1)
				if a, err := xproto.GetAtomName(s.c, m.Name).Reply(); err == nil && a.Name != "" {
					name = a.Name
				}
				d := Display{Name: name, X: int(m.X), Y: int(m.Y), W: int(m.Width), H: int(m.Height), Primary: m.Primary}
				if d.W > 0 && d.H > 0 && d.X >= 0 && d.Y >= 0 && d.X+d.W <= s.width && d.Y+d.H <= s.height {
					out = append(out, d)
				}
			}
			if len(out) > 0 {
				if primary(out) == 0 && !out[0].Primary {
					out[0].Primary = true
				}
				return out, nil
			}
		}
	}
	return []Display{{Name: "screen", W: s.width, H: s.height, Primary: true}}, nil
}

func (s *x11Screen) Capture(d Display, img *image.RGBA) error {
	s.capMu.Lock()
	defer s.capMu.Unlock()
	size := d.W * d.H * 4
	if s.shm == nil || len(s.shm.data) < size {
		if s.shm != nil {
			s.shm.free(s.c)
			s.shm = nil
		}
		s.shm, _ = newShm(s.c, size) // nil (GetImage fallback) on remote or BSD servers
	}
	var src []byte
	if s.shm != nil {
		if _, err := s.shm.getImage(s.c, s.root, d); err == nil {
			src = s.shm.data[:size]
		} else {
			s.shm.free(s.c)
			s.shm = nil
		}
	}
	if src == nil {
		r, err := xproto.GetImage(s.c, xproto.ImageFormatZPixmap, xproto.Drawable(s.root),
			int16(d.X), int16(d.Y), uint16(d.W), uint16(d.H), 0xffffffff).Reply()
		if err != nil {
			return fmt.Errorf("GetImage: %w", err)
		}
		src = r.Data
	}
	dst := img.Pix
	for i := 0; i+3 < len(src) && i+3 < len(dst); i += 4 {
		dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0xff
	}
	return nil
}

func (s *x11Screen) Cursor() (int, int, bool) {
	r, err := xproto.QueryPointer(s.c, s.root).Reply()
	if err != nil || !r.SameScreen {
		return 0, 0, false
	}
	return int(r.RootX), int(r.RootY), true
}

var errNoXTest = errors.New("the X server has no XTEST extension, so remote input is unavailable")

func (s *x11Screen) fake(typ, detail byte, x, y int) error {
	if !s.xtest {
		return errNoXTest
	}
	return xtest.FakeInputChecked(s.c, typ, detail, xproto.TimeCurrentTime, s.root, int16(x), int16(y), 0).Check()
}

func (s *x11Screen) MoveMouse(x, y int) error { return s.fake(xproto.MotionNotify, 0, x, y) }

func (s *x11Screen) Button(button int, down bool) error {
	detail := map[int]byte{ButtonLeft: 1, ButtonMiddle: 2, ButtonRight: 3, ButtonBack: 8, ButtonForward: 9}[button]
	if detail == 0 {
		return fmt.Errorf("unknown button %d", button)
	}
	return s.fake(buttonEvent(down), detail, 0, 0)
}

func buttonEvent(down bool) byte {
	if down {
		return xproto.ButtonPress
	}
	return xproto.ButtonRelease
}

func (s *x11Screen) click(b byte, n int) error {
	for range n {
		if err := s.fake(xproto.ButtonPress, b, 0, 0); err != nil {
			return err
		}
		if err := s.fake(xproto.ButtonRelease, b, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

// Scroll uses the core protocol's wheel buttons: 4/5 vertical, 6/7 horizontal.
func (s *x11Screen) Scroll(dx, dy int) error {
	if dy != 0 {
		if err := s.click(byte(map[bool]int{true: 5, false: 4}[dy > 0]), abs(dy)); err != nil {
			return err
		}
	}
	if dx != 0 {
		return s.click(byte(map[bool]int{true: 7, false: 6}[dx > 0]), abs(dx))
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (s *x11Screen) keycode(code string) (byte, error) {
	k, ok := lookupKey(code)
	if !ok {
		return 0, fmt.Errorf("unknown key %q", code)
	}
	kc := int(k.evdev) + evdevOffset
	if kc < int(s.minKey) || kc > int(s.maxKey) {
		return 0, fmt.Errorf("key %q has no X11 keycode", code)
	}
	return byte(kc), nil
}

func (s *x11Screen) Key(code string, down bool) error {
	kc, err := s.keycode(code)
	if err != nil {
		return err
	}
	return s.fake(keyEvent(down), kc, 0, 0)
}

func keyEvent(down bool) byte {
	if down {
		return xproto.KeyPress
	}
	return xproto.KeyRelease
}

func (s *x11Screen) tap(kc byte) error {
	if err := s.fake(xproto.KeyPress, kc, 0, 0); err != nil {
		return err
	}
	return s.fake(xproto.KeyRelease, kc, 0, 0)
}

func (s *x11Screen) loadKeymap() error {
	r, err := xproto.GetKeyboardMapping(s.c, s.minKey, byte(s.maxKey-s.minKey+1)).Reply()
	if err != nil {
		return fmt.Errorf("read keyboard mapping: %w", err)
	}
	s.kmMu.Lock()
	defer s.kmMu.Unlock()
	s.symsPer, s.keymap = int(r.KeysymsPerKeycode), r.Keysyms
	if s.spare == 0 {
		for kc := int(s.maxKey); kc >= int(s.minKey) && s.symsPer > 0; kc-- {
			off := (kc - int(s.minKey)) * s.symsPer
			if off+s.symsPer > len(s.keymap) {
				continue
			}
			empty := true
			for _, sym := range s.keymap[off : off+s.symsPer] {
				empty = empty && sym == 0
			}
			if empty {
				s.spare = xproto.Keycode(kc)
				break
			}
		}
	}
	return nil
}

// findKey returns a keycode producing sym, and whether Shift is needed.
func (s *x11Screen) findKey(sym xproto.Keysym) (byte, bool, bool) {
	s.kmMu.Lock()
	defer s.kmMu.Unlock()
	for level := 0; level < 2 && level < s.symsPer; level++ {
		for i := 0; i*s.symsPer+level < len(s.keymap); i++ {
			kc := int(s.minKey) + i
			if s.keymap[i*s.symsPer+level] == sym && xproto.Keycode(kc) != s.spare {
				return byte(kc), level == 1, true
			}
		}
	}
	return 0, false, false
}

func keysym(r rune) xproto.Keysym {
	if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) {
		return xproto.Keysym(r) // Latin-1 keysyms are the code points
	}
	return xproto.Keysym(0x01000000 | r)
}

// Type types each character with a key that produces it, or else maps the
// character onto a spare keycode for a moment (what xdotool does).
func (s *x11Screen) Type(text string) error {
	if !s.xtest {
		return errNoXTest
	}
	s.typeMu.Lock()
	defer s.typeMu.Unlock()
	shift, _ := s.keycode("ShiftLeft")
	remapped, remaps := false, 0
	defer func() {
		if remapped {
			_ = xproto.ChangeKeyboardMappingChecked(s.c, 1, s.spare, byte(s.symsPer), make([]xproto.Keysym, s.symsPer)).Check()
		}
	}()
	for _, r := range text {
		var err error
		switch r {
		case '\r':
			continue
		case '\n':
			err = s.Key("Enter", true)
			if err == nil {
				err = s.Key("Enter", false)
			}
		case '\t':
			err = s.Key("Tab", true)
			if err == nil {
				err = s.Key("Tab", false)
			}
		default:
			sym := keysym(r)
			if kc, needShift, ok := s.findKey(sym); ok {
				if needShift && shift != 0 {
					if err = s.fake(xproto.KeyPress, shift, 0, 0); err != nil {
						return err
					}
				}
				err = s.tap(kc)
				if needShift && shift != 0 {
					_ = s.fake(xproto.KeyRelease, shift, 0, 0)
				}
				break
			}
			if s.spare == 0 {
				continue // no free keycode: skip characters the layout lacks
			}
			if remaps++; remaps > maxRemaps {
				return fmt.Errorf("more than %d characters the keyboard layout lacks in one message", maxRemaps)
			}
			syms := make([]xproto.Keysym, s.symsPer)
			for i := range syms {
				syms[i] = sym
			}
			if err = xproto.ChangeKeyboardMappingChecked(s.c, 1, s.spare, byte(s.symsPer), syms).Check(); err != nil {
				return err
			}
			remapped = true
			time.Sleep(remapDelay) // let clients see the new mapping
			err = s.tap(byte(s.spare))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *x11Screen) Clipboard() (string, error) {
	s.clipMu.Lock()
	if s.owned {
		t := s.ownText
		s.clipMu.Unlock()
		return t, nil
	}
	s.clipMu.Unlock()
	s.convMu.Lock()
	defer s.convMu.Unlock()
	select {
	case <-s.notify:
	default:
	}
	xproto.ConvertSelection(s.c, s.win, s.clipboard, s.utf8, s.prop, xproto.TimeCurrentTime)
	var ev xproto.SelectionNotifyEvent
	select {
	case ev = <-s.notify:
	case <-time.After(2 * time.Second):
		return "", errors.New("the clipboard owner did not answer")
	}
	if ev.Property == xproto.AtomNone {
		return "", nil // empty, or not text
	}
	r, err := xproto.GetProperty(s.c, true, s.win, s.prop, xproto.GetPropertyTypeAny, 0, maxClipBytes/4).Reply()
	if err != nil {
		return "", err
	}
	if r.Type == s.incr {
		return "", errors.New("clipboard text is too large to transfer")
	}
	return string(r.Value), nil
}

func (s *x11Screen) SetClipboard(text string) error {
	if len(text) > s.maxReq-64 {
		return fmt.Errorf("clipboard text too large for this X server (%d bytes, at most %d)", len(text), s.maxReq-64)
	}
	s.clipMu.Lock()
	s.owned, s.ownText = true, text
	s.clipMu.Unlock()
	xproto.SetSelectionOwner(s.c, s.win, s.clipboard, xproto.TimeCurrentTime)
	r, err := xproto.GetSelectionOwner(s.c, s.clipboard).Reply()
	if err != nil || r.Owner != s.win {
		s.clipMu.Lock()
		s.owned = false
		s.clipMu.Unlock()
		return errors.New("could not take ownership of the clipboard")
	}
	return nil
}

func (s *x11Screen) ClipboardSeq() uint64 {
	if !s.xfixes {
		return 0
	}
	return s.clipSeq.Load()
}

// events serves clipboard requests and tracks keyboard and clipboard
// changes until the connection closes.
func (s *x11Screen) events() {
	for {
		ev, err := s.c.WaitForEvent()
		if ev == nil && err == nil {
			return
		}
		switch e := ev.(type) {
		case xproto.SelectionRequestEvent:
			s.serve(e)
		case xproto.SelectionNotifyEvent:
			select {
			case s.notify <- e:
			default:
			}
		case xproto.SelectionClearEvent:
			s.clipMu.Lock()
			s.owned = false
			s.clipMu.Unlock()
		case xproto.MappingNotifyEvent:
			_ = s.loadKeymap()
		case xfixes.SelectionNotifyEvent:
			s.clipSeq.Add(1)
		}
	}
}

// serve answers another program reading the clipboard we own.
func (s *x11Screen) serve(e xproto.SelectionRequestEvent) {
	prop := e.Property
	if prop == xproto.AtomNone {
		prop = e.Target // obsolete requestors
	}
	s.clipMu.Lock()
	owned, text := s.owned, s.ownText
	s.clipMu.Unlock()
	ok := owned && e.Selection == s.clipboard
	if ok {
		switch e.Target {
		case s.targets:
			atoms := []xproto.Atom{s.targets, s.utf8, s.textAtom, xproto.AtomString}
			b := make([]byte, 4*len(atoms))
			for i, a := range atoms {
				binary.LittleEndian.PutUint32(b[i*4:], uint32(a))
			}
			xproto.ChangeProperty(s.c, xproto.PropModeReplace, e.Requestor, prop, xproto.AtomAtom, 32, uint32(len(atoms)), b)
		case s.utf8, s.textAtom, xproto.AtomString:
			typ := s.utf8
			if e.Target == xproto.AtomString {
				typ = xproto.AtomString
			}
			xproto.ChangeProperty(s.c, xproto.PropModeReplace, e.Requestor, prop, typ, 8, uint32(len(text)), []byte(text))
		default:
			ok = false
		}
	}
	if !ok {
		prop = xproto.AtomNone
	}
	n := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: prop}
	xproto.SendEvent(s.c, false, e.Requestor, 0, string(n.Bytes()))
}

func (s *x11Screen) Close() error {
	s.capMu.Lock()
	if s.shm != nil {
		s.shm.free(s.c)
		s.shm = nil
	}
	s.capMu.Unlock()
	s.c.Close()
	return nil
}
