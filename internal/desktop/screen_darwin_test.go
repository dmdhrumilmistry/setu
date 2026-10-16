//go:build darwin

package desktop

import (
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMacBindings exercises the CoreGraphics and AppKit bindings directly,
// so CI checks them even where macOS withholds the screen recording
// permission that Open requires.
func TestMacBindings(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	s := &macScreen{}
	ds, err := s.Displays()
	if err != nil {
		t.Skipf("no display: %v", err)
	}
	t.Logf("displays: %+v (scale %.2f)", ds, s.displays[0].scale)
	d := ds[0]
	if d.W <= 0 || d.H <= 0 || !d.Primary {
		t.Fatalf("bad primary display %+v", d)
	}
	if x, y, ok := s.Cursor(); ok {
		if p := s.toPoint(x, y); p.X < s.displays[0].origin.X-1 {
			t.Errorf("cursor %d,%d maps to point %+v", x, y, p)
		}
	}

	saved, _ := s.Clipboard()
	defer s.SetClipboard(saved)
	before := s.ClipboardSeq()
	const text = "setu mac clipboard: héllo 👋\nline 2"
	if err := s.SetClipboard(text); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Clipboard(); err != nil || got != text {
		t.Fatalf("clipboard round trip: %q, %v", got, err)
	}
	if s.ClipboardSeq() == before {
		t.Error("clipboard change count did not change")
	}

	if cgPreflightScreenCaptureAccess() {
		img := image.NewRGBA(image.Rect(0, 0, d.W, d.H))
		if err := s.Capture(d, img); err != nil {
			t.Fatalf("capture: %v", err)
		}
	} else {
		t.Log("screen recording not permitted here; capture not exercised")
	}
}

// TestMacInput reports what macOS allows here and, where it allows posting
// events, checks that injected input really reaches the system: the
// cursor moves, and the HID state shows keys and buttons going down and up.
func TestMacInput(t *testing.T) {
	if err := load(); err != nil {
		t.Fatal(err)
	}
	ver, _ := unix.Sysctl("kern.osproductversion")
	opt := func(f func() bool) string {
		if f == nil {
			return "n/a"
		}
		return fmt.Sprint(f())
	}
	t.Logf("macOS %s: screen recording %s, post events %s, accessibility %v, secure input %s",
		ver, opt(cgPreflightScreenCaptureAccess), opt(cgPreflightPostEventAccess), axIsProcessTrusted(), opt(isSecureEventInputEnabled))
	if os.Getenv("SETU_DESKTOP_INPUT_TEST") == "" {
		t.Skip("set SETU_DESKTOP_INPUT_TEST=1 to inject input")
	}
	s := &macScreen{}
	ds, err := s.Displays()
	if err != nil {
		t.Skipf("no display: %v", err)
	}
	if err := s.InputStatus(); err != nil {
		t.Skipf("input not permitted on this machine: %v", err)
	}
	d := ds[0]
	x, y := d.X+d.W/3, d.Y+d.H/3
	if err := s.MoveMouse(x, y); err != nil {
		t.Fatalf("move: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if cx, cy, ok := s.Cursor(); !ok || abs(cx-x) > 2 || abs(cy-y) > 2 {
		t.Fatalf("cursor at %d,%d (ok=%v) after moving to %d,%d", cx, cy, ok, x, y)
	}

	shift := keys["ShiftLeft"].mac
	for _, down := range []bool{true, false} {
		if err := s.Key("ShiftLeft", down); err != nil {
			t.Fatalf("shift %v: %v", down, err)
		}
		time.Sleep(50 * time.Millisecond)
		if got := cgEventSourceKeyState(cgEventSourceStateHID, shift); got != down {
			t.Fatalf("after shift %v the system sees it %v", down, got)
		}
	}
	// Right button down and up at a harmless spot (opens and closes a menu at most).
	for _, down := range []bool{true, false} {
		if err := s.Button(ButtonRight, down); err != nil {
			t.Fatalf("button %v: %v", down, err)
		}
		time.Sleep(50 * time.Millisecond)
		if got := cgEventSourceButtonState(cgEventSourceStateHID, 1); got != down {
			t.Fatalf("after right button %v the system sees it %v", down, got)
		}
	}
	_ = s.Key("Escape", true)
	_ = s.Key("Escape", false)

	// A click right after a move lands at the new position, although macOS
	// may not have moved the cursor yet.
	var at []cgPoint
	s.mu.Lock()
	s.onPost = func(ev uintptr) { at = append(at, cgEventGetLocation(ev)) }
	s.mu.Unlock()
	x2, y2 := d.X+d.W/2, d.Y+d.H/2
	if err := s.MoveMouse(x2, y2); err != nil {
		t.Fatal(err)
	}
	_ = s.Button(ButtonRight, true)
	_ = s.Button(ButtonRight, false)
	_ = s.Key("Escape", true)
	_ = s.Key("Escape", false)
	s.mu.Lock()
	s.onPost = nil
	s.mu.Unlock()
	want := s.toPoint(x2, y2)
	if len(at) < 3 || at[1] != want || at[2] != want {
		t.Fatalf("click events at %v, want both at %v", at, want)
	}
	if err := s.Scroll(0, 1); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	t.Log("mouse, buttons, keys and scrolling reach the system")
}
