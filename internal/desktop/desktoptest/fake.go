// Package desktoptest provides an in-memory desktop.Screen for tests.
package desktoptest

import (
	"fmt"
	"image"
	"image/color"
	"sync"

	"github.com/dmdhrumilmistry/setu/internal/desktop"
)

// Screen is a fake desktop: a framebuffer per display that tests paint
// into, and a log of every input event it received.
type Screen struct {
	mu       sync.Mutex
	displays []desktop.Display
	fb       *image.RGBA // the whole virtual desktop
	events   []string
	clip     string
	seq      uint64
	cx, cy   int
}

// New creates a fake desktop with the given displays (side by side if
// their positions are set accordingly).
func New(displays ...desktop.Display) *Screen {
	var r image.Rectangle
	for _, d := range displays {
		r = r.Union(d.Rect())
	}
	return &Screen{displays: displays, fb: image.NewRGBA(r), seq: 1}
}

// Fill paints a rectangle of the virtual desktop.
func (s *Screen) Fill(r image.Rectangle, c color.RGBA) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			s.fb.SetRGBA(x, y, c)
		}
	}
}

// Events returns the input events received so far.
func (s *Screen) Events() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

// SetHostClipboard simulates a program on the host copying text.
func (s *Screen) SetHostClipboard(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clip = text
	s.seq++
}

func (s *Screen) log(f string, a ...any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, fmt.Sprintf(f, a...))
	return nil
}

func (s *Screen) Displays() ([]desktop.Display, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]desktop.Display(nil), s.displays...), nil
}

func (s *Screen) Capture(d desktop.Display, img *image.RGBA) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for y := range d.H {
		src := s.fb.Pix[s.fb.PixOffset(d.X, d.Y+y):]
		copy(img.Pix[y*img.Stride:y*img.Stride+d.W*4], src[:d.W*4])
	}
	return nil
}

func (s *Screen) Cursor() (int, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cx, s.cy, true
}

func (s *Screen) MoveMouse(x, y int) error {
	s.mu.Lock()
	s.cx, s.cy = x, y
	s.mu.Unlock()
	return s.log("move %d,%d", x, y)
}

func (s *Screen) Button(b int, down bool) error { return s.log("button %d %v", b, down) }
func (s *Screen) Scroll(dx, dy int) error       { return s.log("scroll %d,%d", dx, dy) }
func (s *Screen) Key(code string, down bool) error {
	return s.log("key %s %v", code, down)
}
func (s *Screen) Type(text string) error { return s.log("type %q", text) }

func (s *Screen) Clipboard() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clip, nil
}

func (s *Screen) SetClipboard(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clip = text
	s.seq++
	return nil
}

func (s *Screen) ClipboardSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

func (s *Screen) Close() error { return nil }
