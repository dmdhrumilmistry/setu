package desktop_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/desktop"
	"github.com/dmdhrumilmistry/setu/internal/desktop/desktoptest"
)

type tile struct {
	enc  byte
	r    image.Rectangle
	data []byte
}

type frame struct {
	id     uint32
	tiles  []tile
	cx, cy int
}

// recorder is a Sink that parses what the hub sends.
type recorder struct {
	mu      sync.Mutex
	pending []tile
	frames  []frame
	maxMsg  int
}

func (r *recorder) SendFrame(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxMsg = max(r.maxMsg, len(b))
	switch b[0] {
	case desktop.MsgTile:
		u := func(i int) int { return int(binary.LittleEndian.Uint16(b[i:])) }
		x, y := u(2), u(4)
		r.pending = append(r.pending, tile{enc: b[1], r: image.Rect(x, y, x+u(6), y+u(8)), data: append([]byte(nil), b[10:]...)})
	case desktop.MsgFrameEnd:
		r.frames = append(r.frames, frame{id: binary.LittleEndian.Uint32(b[1:]), tiles: r.pending,
			cx: int(int32(binary.LittleEndian.Uint32(b[5:]))), cy: int(int32(binary.LittleEndian.Uint32(b[9:])))})
		r.pending = nil
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

func (r *recorder) frame(t *testing.T, n int) frame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.frames) >= n {
			f := r.frames[n-1]
			r.mu.Unlock()
			return f
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("frame %d never arrived", n)
	return frame{}
}

func startHub(t *testing.T, scr desktop.Screen, opt desktop.Options) *desktop.Hub {
	t.Helper()
	opt.FPS = 50
	h, err := desktop.NewHub(scr, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	return h
}

func area(ts []tile) int {
	n := 0
	for _, t := range ts {
		n += t.r.Dx() * t.r.Dy()
	}
	return n
}

func decode(t *testing.T, tl tile) image.Image {
	t.Helper()
	var img image.Image
	var err error
	if tl.enc == desktop.EncPNG {
		img, err = png.Decode(bytes.NewReader(tl.data))
	} else {
		img, err = jpeg.Decode(bytes.NewReader(tl.data))
	}
	if err != nil {
		t.Fatalf("decode tile %v: %v", tl.r, err)
	}
	if img.Bounds().Dx() != tl.r.Dx() || img.Bounds().Dy() != tl.r.Dy() {
		t.Fatalf("tile %v decodes to %v", tl.r, img.Bounds())
	}
	return img
}

func TestHubSendsOnlyChangedTiles(t *testing.T) {
	scr := desktoptest.New(desktop.Display{Name: "left", W: 100, H: 50}, desktop.Display{Name: "main", X: 100, W: 300, H: 130, Primary: true})
	h := startHub(t, scr, desktop.Options{Display: -1})
	if ds, cur := h.Displays(); ds[cur].Name != "main" {
		t.Fatalf("shares %q, want the primary display", ds[cur].Name)
	}
	rec := &recorder{}
	v := h.Join(rec, true)

	first := rec.frame(t, 1)
	if got := area(first.tiles); got != 300*130 {
		t.Fatalf("first frame covers %d pixels, want the whole display (%d)", got, 300*130)
	}
	for _, tl := range first.tiles {
		decode(t, tl)
	}
	v.Ack(first.id)

	scr.Fill(image.Rect(100+70, 70, 100+80, 80), color.RGBA{255, 0, 0, 255}) // inside tile (1,1)
	var next frame
	for i := 2; ; i++ {
		next = rec.frame(t, i)
		v.Ack(next.id)
		if len(next.tiles) > 0 {
			break
		}
	}
	if len(next.tiles) != 1 || next.tiles[0].r != image.Rect(64, 64, 128, 128) {
		t.Fatalf("changed tiles %v, want only (64,64)-(128,128)", next.tiles)
	}
	if c := decode(t, next.tiles[0]); !isRed(c.At(10, 10)) {
		t.Fatalf("tile pixel is %v, want red", c.At(10, 10))
	}

	if err := h.SelectDisplay(0); err != nil {
		t.Fatal(err)
	}
	for i := rec.count() + 1; ; i++ {
		f := rec.frame(t, i)
		v.Ack(f.id)
		if area(f.tiles) == 100*50 {
			break
		}
	}
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xe000 && g < 0x2000 && b < 0x2000
}

func TestHubFlowControl(t *testing.T) {
	scr := desktoptest.New(desktop.Display{Name: "d", W: 128, H: 64, Primary: true})
	h := startHub(t, scr, desktop.Options{Display: -1})
	rec := &recorder{}
	h.Join(rec, false)
	for i := range 30 {
		scr.Fill(image.Rect(0, 0, 128, 64), color.RGBA{uint8(i * 8), 0, 0, 255})
		time.Sleep(10 * time.Millisecond)
	}
	if n := rec.count(); n > 2 {
		t.Fatalf("sent %d frames without acknowledgements, want at most 2", n)
	}
}

func TestHubSplitsLargeTiles(t *testing.T) {
	scr := desktoptest.New(desktop.Display{Name: "d", W: 512, H: 128, Primary: true})
	rng := rand.New(rand.NewPCG(1, 2))
	for y := range 128 {
		for x := range 512 {
			scr.Fill(image.Rect(x, y, x+1, y+1), color.RGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), 255})
		}
	}
	h := startHub(t, scr, desktop.Options{Display: -1})
	rec := &recorder{}
	v := h.Join(rec, true)
	v.SetQuality(desktop.QualitySharp)
	f := rec.frame(t, 1)
	if area(f.tiles) != 512*128 {
		t.Fatalf("frame covers %d pixels", area(f.tiles))
	}
	if rec.maxMsg > desktop.MaxMessage {
		t.Fatalf("largest message %d bytes exceeds %d", rec.maxMsg, desktop.MaxMessage)
	}
}

func TestViewerInput(t *testing.T) {
	scr := desktoptest.New(desktop.Display{Name: "a", W: 100, H: 100}, desktop.Display{Name: "b", X: 100, W: 200, H: 100, Primary: true})
	h := startHub(t, scr, desktop.Options{Display: -1})
	ctl := h.Join(&recorder{}, true)
	view := h.Join(&recorder{}, false)

	if err := view.Pointer(1, 1, desktop.ButtonLeft); err == nil {
		t.Fatal("view-only viewer moved the mouse")
	}
	if err := view.Key("KeyA", true); err == nil {
		t.Fatal("view-only viewer pressed a key")
	}
	if err := view.SetClipboard("x"); err == nil {
		t.Fatal("view-only viewer set the clipboard")
	}
	steps := []func() error{
		func() error { return ctl.Pointer(10, 20, desktop.ButtonLeft) },
		func() error { return ctl.Pointer(500, -5, 0) },
		func() error { return ctl.Wheel(0, 3) },
		func() error { return ctl.Key("ShiftLeft", true) },
		func() error { return ctl.Type("hé\n") },
	}
	for _, s := range steps {
		if err := s(); err != nil {
			t.Fatal(err)
		}
	}
	if err := ctl.Key("NoSuchKey", true); err == nil {
		t.Fatal("unknown key accepted")
	}
	act := ctl.Leave()
	want := []string{"move 110,20", "button 1 true", "move 299,0", "button 1 false", "scroll 0,3",
		"key ShiftLeft true", `type "hé\n"`, "key ShiftLeft false"}
	if got := scr.Events(); !slices.Equal(got, want) {
		t.Fatalf("events\n got %q\nwant %q", got, want)
	}
	if act != (desktop.Activity{Clicks: 1, Keys: 1, Scrolls: 1, TypedChars: 3}) {
		t.Fatalf("activity %+v", act)
	}
	// Input that races with the disconnect must not press anything afterwards.
	if ctl.Key("ControlLeft", true) == nil || ctl.Pointer(1, 1, desktop.ButtonLeft) == nil {
		t.Fatal("input accepted after Leave")
	}
	if got := scr.Events(); len(got) != len(want) {
		t.Fatalf("input reached the screen after Leave: %q", got[len(want):])
	}
}

func TestClipboardSync(t *testing.T) {
	scr := desktoptest.New(desktop.Display{Name: "d", W: 64, H: 64, Primary: true})
	scr.SetHostClipboard("copied before the share")
	got := make(chan string, 4)
	h := startHub(t, scr, desktop.Options{Display: -1, OnClipboard: func(s string) { got <- s }})
	v := h.Join(&recorder{}, true)

	if err := v.SetClipboard("from the client"); err != nil {
		t.Fatal(err)
	}
	if text, _ := v.Clipboard(); text != "from the client" {
		t.Fatalf("host clipboard %q", text)
	}
	scr.SetHostClipboard("from the host")
	select {
	case s := <-got:
		if s != "from the host" {
			t.Fatalf("forwarded %q, want only the host's own change", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("host clipboard change not forwarded")
	}
	select {
	case s := <-got:
		t.Fatalf("unexpected clipboard forward %q", s)
	case <-time.After(700 * time.Millisecond):
	}
}
