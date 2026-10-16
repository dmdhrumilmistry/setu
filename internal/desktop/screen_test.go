package desktop_test

import (
	"errors"
	"image"
	"os"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/desktop"
)

// TestRealScreen exercises this machine's backend: Windows, or X11 (CI runs
// it under Xvfb). It skips where there is no desktop to open.
func TestRealScreen(t *testing.T) {
	scr, err := desktop.Open()
	if err != nil {
		t.Skipf("no desktop here: %v", err)
	}
	defer scr.Close()
	ds, err := scr.Displays()
	if err != nil || len(ds) == 0 {
		t.Fatalf("displays %v: %v", ds, err)
	}
	d := ds[0]
	t.Logf("displays: %+v", ds)
	img := image.NewRGBA(image.Rect(0, 0, d.W, d.H))
	if err := scr.Capture(d, img); err != nil {
		t.Skipf("capture unavailable (no interactive desktop?): %v", err)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			t.Fatal("captured pixels are not opaque")
		}
	}

	saved, err := scr.Clipboard()
	if err != nil {
		t.Fatalf("read clipboard: %v", err)
	}
	defer scr.SetClipboard(saved)
	const text = "setu clipboard test: héllo 👋\nsecond line" // LF on the wire, whatever the host uses
	if err := scr.SetClipboard(text); err != nil {
		t.Fatalf("set clipboard: %v", err)
	}
	if got, err := scr.Clipboard(); err != nil || got != text {
		t.Fatalf("clipboard round trip: %q, %v", got, err)
	}

	// Input moves the real pointer, so it only runs where asked (CI).
	if os.Getenv("SETU_DESKTOP_INPUT_TEST") == "" {
		return
	}
	x, y := d.X+d.W/2+3, d.Y+d.H/2+5
	if err := scr.MoveMouse(x, y); err != nil {
		t.Fatalf("move mouse: %v", err)
	}
	// macOS applies posted events asynchronously: give the cursor a moment.
	for i := 0; ; i++ {
		cx, cy, _ := scr.Cursor()
		if abs(cx-x) <= 1 && abs(cy-y) <= 1 {
			break
		}
		if i == 20 {
			t.Fatalf("pointer at %d,%d after moving to %d,%d", cx, cy, x, y)
		}
		time.Sleep(25 * time.Millisecond)
	}
	for _, err := range []error{scr.Key("ShiftLeft", true), scr.Key("ShiftLeft", false), scr.Scroll(0, 1), scr.Type("a")} {
		if err != nil && !errors.Is(err, desktop.ErrUnsupported) {
			t.Fatalf("input: %v", err)
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
