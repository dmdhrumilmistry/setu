//go:build darwin

package desktop

import (
	"image"
	"os"
	"testing"
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
	if os.Getenv("SETU_DESKTOP_INPUT_TEST") != "" && axIsProcessTrusted() {
		if err := s.MoveMouse(d.X+d.W/2, d.Y+d.H/2); err != nil {
			t.Fatal(err)
		}
	}
}
