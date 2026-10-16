package desktop

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"runtime"
	"slices"
	"sync"
	"time"
)

// Wire format of the binary messages on the screen channel. Integers are
// little endian.
//
//	tile:      [MsgTile, enc, x u16, y u16, w u16, h u16, image...]
//	frame end: [MsgFrameEnd, id u32, cursorX i32, cursorY i32, cursorVisible u8]
//
// The client draws a frame's tiles, then acknowledges its id. A viewer has
// at most maxInflight unacknowledged frames, so a slow link or device gets
// fewer, fresher frames instead of a growing queue.
const (
	MsgTile     = 1
	MsgFrameEnd = 2

	EncJPEG = 1
	EncPNG  = 2

	// MaxMessage keeps every message under the 64 KiB that all WebRTC
	// implementations accept.
	MaxMessage = 60 << 10

	TileSize    = 64
	maxRunTiles = 8
	maxInflight = 2
	tileHeader  = 10
)

// Image qualities a viewer can choose.
const (
	QualitySharp    = "sharp"
	QualityBalanced = "balanced"
	QualityFast     = "fast"
)

func jpegQuality(q string) int {
	switch q {
	case QualitySharp:
		return 88
	case QualityFast:
		return 45
	}
	return 72
}

// ValidQuality reports whether q names a quality.
func ValidQuality(q string) bool {
	return q == QualitySharp || q == QualityBalanced || q == QualityFast
}

// Sink delivers one binary message to a viewer.
type Sink interface {
	SendFrame(b []byte) error
}

// Options configures a Hub.
type Options struct {
	FPS     int // frames per second at most (default 20)
	Display int // index into Displays(); -1 = the primary display
	Logf    func(format string, args ...any)
	// OnScreen is called when the shared display or the display layout
	// changes.
	OnScreen func(displays []Display, current int)
	// OnClipboard is called when the host's clipboard text changes.
	OnClipboard func(text string)
	// OnStatus reports capture problems and recovery to viewers.
	OnStatus func(msg string)
}

// Hub streams one screen to many viewers.
type Hub struct {
	scr Screen
	opt Options

	mu       sync.Mutex
	viewers  map[*Viewer]struct{}
	displays []Display
	cur      int
	prev     *image.RGBA // last captured frame
	spare    *image.RGBA
	clipSeq  uint64
	clipText string
	capErr   string

	wake chan struct{}
}

// NewHub prepares a hub for scr.
func NewHub(scr Screen, opt Options) (*Hub, error) {
	if opt.FPS <= 0 {
		opt.FPS = 20
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	ds, err := scr.Displays()
	if err != nil {
		return nil, fmt.Errorf("list displays: %w", err)
	}
	if len(ds) == 0 {
		return nil, errors.New("no display found")
	}
	cur := primary(ds)
	if opt.Display >= 0 {
		if opt.Display >= len(ds) {
			return nil, fmt.Errorf("display %d does not exist (this machine has %d)", opt.Display, len(ds))
		}
		cur = opt.Display
	}
	return &Hub{scr: scr, opt: opt, viewers: map[*Viewer]struct{}{}, displays: ds, cur: cur,
		clipSeq: scr.ClipboardSeq(), wake: make(chan struct{}, 1)}, nil
}

func primary(ds []Display) int {
	for i, d := range ds {
		if d.Primary {
			return i
		}
	}
	return 0
}

// Displays returns the displays and the index of the shared one.
func (h *Hub) Displays() ([]Display, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.displays), h.cur
}

// SelectDisplay switches every viewer to display i.
func (h *Hub) SelectDisplay(i int) error {
	h.mu.Lock()
	if i < 0 || i >= len(h.displays) {
		h.mu.Unlock()
		return fmt.Errorf("no display %d", i)
	}
	changed := i != h.cur
	h.cur = i
	if changed {
		h.resetLocked()
	}
	ds, cur := slices.Clone(h.displays), h.cur
	h.mu.Unlock()
	if changed && h.opt.OnScreen != nil {
		h.opt.OnScreen(ds, cur)
	}
	h.kick()
	return nil
}

// resetLocked forces a full repaint for everyone.
func (h *Hub) resetLocked() {
	h.prev = nil
	for v := range h.viewers {
		v.full = true
	}
}

func (h *Hub) kick() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Run captures and streams until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	frame := time.NewTicker(time.Second / time.Duration(h.opt.FPS))
	defer frame.Stop()
	clip := time.NewTicker(500 * time.Millisecond)
	defer clip.Stop()
	layout := time.NewTicker(5 * time.Second)
	defer layout.Stop()
	interval := time.Second / time.Duration(h.opt.FPS)
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-frame.C:
			h.tick()
			last = time.Now()
		case <-h.wake:
			// Joins, refreshes and display switches are served early, but never
			// faster than the frame rate, so clients cannot force constant work.
			if time.Since(last) >= interval {
				h.tick()
				last = time.Now()
			}
		case <-clip.C:
			h.pollClipboard()
		case <-layout.C:
			h.pollLayout()
		}
	}
}

// pollLayout notices resolution and monitor changes.
func (h *Hub) pollLayout() {
	ds, err := h.scr.Displays()
	if err != nil || len(ds) == 0 {
		return
	}
	h.mu.Lock()
	if slices.Equal(ds, h.displays) {
		h.mu.Unlock()
		return
	}
	name := h.displays[h.cur].Name
	h.displays, h.cur = ds, primary(ds)
	for i, d := range ds {
		if d.Name == name {
			h.cur = i
		}
	}
	h.resetLocked()
	cur := h.cur
	h.mu.Unlock()
	h.opt.Logf("display layout changed; sharing %s (%dx%d)", ds[cur].Name, ds[cur].W, ds[cur].H)
	if h.opt.OnScreen != nil {
		h.opt.OnScreen(slices.Clone(ds), cur)
	}
	h.kick()
}

func (h *Hub) setCaptureError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	h.mu.Lock()
	was := h.capErr
	h.capErr = msg
	h.mu.Unlock()
	switch {
	case msg == was:
	case msg != "":
		h.opt.Logf("WARNING: screen capture failing: %s", msg)
		if was == "" && h.opt.OnStatus != nil {
			h.opt.OnStatus("The remote screen cannot be captured right now (it may be locked or showing a secure prompt).")
		}
	default:
		h.opt.Logf("screen capture resumed")
		if h.opt.OnStatus != nil {
			h.opt.OnStatus("Screen capture resumed.")
		}
	}
}

type frameJob struct {
	v     *Viewer
	id    uint32
	rects []image.Rectangle
	q     int
	end   []byte
}

type encKey struct {
	r image.Rectangle
	q int
}

func (h *Hub) tick() {
	h.mu.Lock()
	var ready []*Viewer
	for v := range h.viewers {
		if v.sent-v.acked < maxInflight {
			ready = append(ready, v)
		}
	}
	d := h.displays[h.cur]
	img := h.spare
	h.mu.Unlock()
	if len(ready) == 0 {
		return
	}
	if img == nil || img.Rect.Dx() != d.W || img.Rect.Dy() != d.H {
		img = image.NewRGBA(image.Rect(0, 0, d.W, d.H))
	}
	if err := h.scr.Capture(d, img); err != nil {
		h.setCaptureError(err)
		return
	}
	h.setCaptureError(nil)
	cx, cy, vis := h.scr.Cursor()
	cx, cy = cx-d.X, cy-d.Y
	vis = vis && cx >= 0 && cy >= 0 && cx < d.W && cy < d.H

	h.mu.Lock()
	if h.displays[h.cur] != d {
		h.mu.Unlock() // switched displays while capturing
		h.kick()
		return
	}
	cols, rows := (d.W+TileSize-1)/TileSize, (d.H+TileSize-1)/TileSize
	changed := diffTiles(h.prev, img, cols, rows)
	h.spare, h.prev = h.prev, img
	for v := range h.viewers {
		v.mark(changed, cols*rows)
	}
	var jobs []frameJob
	for _, v := range ready {
		if _, ok := h.viewers[v]; !ok {
			continue
		}
		rects := v.take(cols, rows, d.W, d.H)
		cursor := !v.cursorSent || v.cx != cx || v.cy != cy || v.cvis != vis
		if len(rects) == 0 && !cursor {
			continue
		}
		v.sent++
		v.cx, v.cy, v.cvis, v.cursorSent = cx, cy, vis, true
		jobs = append(jobs, frameJob{v: v, id: v.sent, rects: rects, q: jpegQuality(v.quality), end: frameEnd(v.sent, cx, cy, vis)})
	}
	h.mu.Unlock()

	encoded := encodeAll(img, jobs)
	for _, j := range jobs {
		for _, r := range j.rects {
			for _, m := range encoded[encKey{r, j.q}] {
				_ = j.v.sink.SendFrame(m)
			}
		}
		_ = j.v.sink.SendFrame(j.end)
	}
}

// encodeAll encodes every distinct (rectangle, quality) once, in parallel.
// Viewers that need the same tiles share the encoded bytes.
func encodeAll(img *image.RGBA, jobs []frameJob) map[encKey][][]byte {
	out := map[encKey][][]byte{}
	var todo []encKey
	for _, j := range jobs {
		for _, r := range j.rects {
			k := encKey{r, j.q}
			if _, ok := out[k]; !ok {
				out[k] = nil
				todo = append(todo, k)
			}
		}
	}
	workers := max(1, min(runtime.NumCPU()/2, 8, len(todo)))
	results := make([][][]byte, len(todo))
	var wg sync.WaitGroup
	next := make(chan int)
	for range workers {
		wg.Go(func() {
			for i := range next {
				results[i] = encodeRect(img, todo[i].r, todo[i].q)
			}
		})
	}
	for i := range todo {
		next <- i
	}
	close(next)
	wg.Wait()
	for i, k := range todo {
		out[k] = results[i]
	}
	return out
}

// diffTiles reports which tiles differ between two frames (nil: all).
func diffTiles(prev, cur *image.RGBA, cols, rows int) []bool {
	if prev == nil || prev.Rect != cur.Rect {
		return nil
	}
	w, h := cur.Rect.Dx(), cur.Rect.Dy()
	out := make([]bool, cols*rows)
	for ty := range rows {
		for tx := range cols {
			x0, x1 := tx*TileSize*4, min((tx+1)*TileSize, w)*4
			for y := ty * TileSize; y < min((ty+1)*TileSize, h); y++ {
				off := y * cur.Stride
				if !bytes.Equal(cur.Pix[off+x0:off+x1], prev.Pix[off+x0:off+x1]) {
					out[ty*cols+tx] = true
					break
				}
			}
		}
	}
	return out
}

func frameEnd(id uint32, x, y int, visible bool) []byte {
	b := make([]byte, 14)
	b[0] = MsgFrameEnd
	binary.LittleEndian.PutUint32(b[1:], id)
	binary.LittleEndian.PutUint32(b[5:], uint32(int32(x)))
	binary.LittleEndian.PutUint32(b[9:], uint32(int32(y)))
	if visible {
		b[13] = 1
	}
	return b
}

// encodeRect encodes one rectangle as one or more tile messages, splitting
// it when the encoding would exceed MaxMessage.
func encodeRect(img *image.RGBA, r image.Rectangle, q int) [][]byte {
	sub := img.SubImage(r).(*image.RGBA)
	data, enc := encodeImage(sub, q)
	if len(data)+tileHeader > MaxMessage {
		switch {
		case r.Dx() > TileSize:
			mid := r.Min.X + max(1, r.Dx()/2/TileSize)*TileSize
			return append(encodeRect(img, image.Rect(r.Min.X, r.Min.Y, mid, r.Max.Y), q),
				encodeRect(img, image.Rect(mid, r.Min.Y, r.Max.X, r.Max.Y), q)...)
		case r.Dy() > 8:
			mid := r.Min.Y + r.Dy()/2
			return append(encodeRect(img, image.Rect(r.Min.X, r.Min.Y, r.Max.X, mid), q),
				encodeRect(img, image.Rect(r.Min.X, mid, r.Max.X, r.Max.Y), q)...)
		}
	}
	msg := make([]byte, tileHeader, tileHeader+len(data))
	msg[0], msg[1] = MsgTile, enc
	binary.LittleEndian.PutUint16(msg[2:], uint16(r.Min.X))
	binary.LittleEndian.PutUint16(msg[4:], uint16(r.Min.Y))
	binary.LittleEndian.PutUint16(msg[6:], uint16(r.Dx()))
	binary.LittleEndian.PutUint16(msg[8:], uint16(r.Dy()))
	return [][]byte{append(msg, data...)}
}

var pngEncoder = png.Encoder{CompressionLevel: png.BestSpeed}

// encodeImage uses lossless PNG for areas with few colours (text, UI) and
// JPEG for everything else (photos, video, gradients).
func encodeImage(img *image.RGBA, q int) ([]byte, byte) {
	var buf bytes.Buffer
	if p := paletted(img); p != nil {
		if pngEncoder.Encode(&buf, p) == nil {
			return buf.Bytes(), EncPNG
		}
		buf.Reset()
	}
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	return buf.Bytes(), EncJPEG
}

// paletted converts img to a paletted image if it has at most 256 colours.
func paletted(img *image.RGBA) *image.Paletted {
	b := img.Bounds()
	index := make(map[uint32]uint8, 64)
	var pal color.Palette
	out := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), nil)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):]
		dst := out.Pix[(y-b.Min.Y)*out.Stride:]
		for x := range b.Dx() {
			p := row[x*4 : x*4+3]
			c := uint32(p[0])<<16 | uint32(p[1])<<8 | uint32(p[2])
			i, ok := index[c]
			if !ok {
				if len(pal) == 256 {
					return nil
				}
				i = uint8(len(pal))
				index[c] = i
				pal = append(pal, color.RGBA{p[0], p[1], p[2], 0xff})
			}
			dst[x] = i
		}
	}
	out.Palette = pal
	return out
}

// Viewer is one client watching the hub.
type Viewer struct {
	h       *Hub
	sink    Sink
	control bool

	// inMu serializes this viewer's input with Leave, so no key or button
	// can be pressed after Leave released everything.
	inMu sync.Mutex
	left bool

	// guarded by h.mu
	quality         string
	dirty           []bool
	full            bool
	sent, acked     uint32
	cx, cy          int
	cvis            bool
	cursorSent      bool
	held            map[string]bool // keys this viewer is holding down
	buttons         int
	clicks, keyDown int
	scrolls, typed  int
}

// Join adds a viewer. Only control viewers may send input.
func (h *Hub) Join(sink Sink, control bool) *Viewer {
	v := &Viewer{h: h, sink: sink, control: control, quality: QualityBalanced, full: true, held: map[string]bool{}}
	h.mu.Lock()
	h.viewers[v] = struct{}{}
	h.mu.Unlock()
	h.kick()
	return v
}

// Activity is what a viewer did, for the audit log.
type Activity struct {
	Clicks, Keys, Scrolls, TypedChars int
}

// Leave removes the viewer and releases any key or button it still holds,
// so a dropped connection cannot leave a key stuck down.
func (v *Viewer) Leave() Activity {
	v.inMu.Lock()
	defer v.inMu.Unlock()
	v.left = true
	h := v.h
	h.mu.Lock()
	delete(h.viewers, v)
	held := make([]string, 0, len(v.held))
	for k := range v.held {
		held = append(held, k)
	}
	v.held = map[string]bool{}
	buttons := v.buttons
	v.buttons = 0
	act := Activity{Clicks: v.clicks, Keys: v.keyDown, Scrolls: v.scrolls, TypedChars: v.typed}
	h.mu.Unlock()
	for _, k := range held {
		_ = h.scr.Key(k, false)
	}
	for b := 1; b <= ButtonForward; b <<= 1 {
		if buttons&b != 0 {
			_ = h.scr.Button(b, false)
		}
	}
	return act
}

// Ack records that the client has drawn frame id.
func (v *Viewer) Ack(id uint32) {
	v.h.mu.Lock()
	if id > v.acked && id <= v.sent {
		v.acked = id
	}
	v.h.mu.Unlock()
}

// Refresh repaints the whole screen for this viewer.
func (v *Viewer) Refresh() {
	v.h.mu.Lock()
	v.full = true
	v.h.mu.Unlock()
	v.h.kick()
}

// SetQuality changes the image quality and repaints.
func (v *Viewer) SetQuality(q string) {
	if !ValidQuality(q) {
		return
	}
	v.h.mu.Lock()
	v.quality, v.full = q, true
	v.h.mu.Unlock()
	v.h.kick()
}

// mark adds changed tiles (nil: all) to the viewer's dirty set.
func (v *Viewer) mark(changed []bool, n int) {
	if len(v.dirty) != n {
		v.dirty = make([]bool, n)
		v.full = true
	}
	if changed == nil {
		v.full = true
		return
	}
	for i, c := range changed {
		if c {
			v.dirty[i] = true
		}
	}
}

// take returns the dirty tiles as horizontal runs and clears them.
func (v *Viewer) take(cols, rows, w, h int) []image.Rectangle {
	var out []image.Rectangle
	for ty := range rows {
		for tx := 0; tx < cols; {
			if !v.full && !v.dirty[ty*cols+tx] {
				tx++
				continue
			}
			start := tx
			for tx < cols && tx-start < maxRunTiles && (v.full || v.dirty[ty*cols+tx]) {
				v.dirty[ty*cols+tx] = false
				tx++
			}
			out = append(out, image.Rect(start*TileSize, ty*TileSize, min(tx*TileSize, w), min((ty+1)*TileSize, h)))
		}
	}
	v.full = false
	clear(v.dirty)
	return out
}

var (
	errViewOnly = errors.New("view-only viewers cannot send input")
	errLeft     = errors.New("viewer has left")
)

// input starts one input action; call the returned func when it is done.
func (v *Viewer) input() (func(), error) {
	if !v.control {
		return nil, errViewOnly
	}
	v.inMu.Lock()
	if v.left {
		v.inMu.Unlock()
		return nil, errLeft
	}
	return v.inMu.Unlock, nil
}

func (v *Viewer) display() Display {
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	return v.h.displays[v.h.cur]
}

// Pointer moves the mouse to (x, y) on the shared display and applies the
// pressed buttons (a PointerEvent.buttons mask).
func (v *Viewer) Pointer(x, y, buttons int) error {
	done, err := v.input()
	if err != nil {
		return err
	}
	defer done()
	d := v.display()
	x, y = min(max(x, 0), d.W-1), min(max(y, 0), d.H-1)
	if err := v.h.scr.MoveMouse(d.X+x, d.Y+y); err != nil {
		return err
	}
	v.h.mu.Lock()
	prev := v.buttons
	v.buttons = buttons & (ButtonForward<<1 - 1)
	now := v.buttons
	for b := 1; b <= ButtonForward; b <<= 1 {
		if now&b != 0 && prev&b == 0 {
			v.clicks++
		}
	}
	v.h.mu.Unlock()
	for b := 1; b <= ButtonForward; b <<= 1 {
		if (prev^now)&b != 0 {
			if err := v.h.scr.Button(b, now&b != 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// Wheel scrolls by notches.
func (v *Viewer) Wheel(dx, dy int) error {
	done, err := v.input()
	if err != nil {
		return err
	}
	defer done()
	dx, dy = min(max(dx, -10), 10), min(max(dy, -10), 10)
	if dx == 0 && dy == 0 {
		return nil
	}
	v.h.mu.Lock()
	v.scrolls++
	v.h.mu.Unlock()
	return v.h.scr.Scroll(dx, dy)
}

// Key presses or releases a physical key.
func (v *Viewer) Key(code string, down bool) error {
	done, err := v.input()
	if err != nil {
		return err
	}
	defer done()
	if _, ok := lookupKey(code); !ok {
		return fmt.Errorf("unknown key %q", code)
	}
	v.h.mu.Lock()
	if down {
		v.held[code] = true
		v.keyDown++
	} else {
		delete(v.held, code)
	}
	v.h.mu.Unlock()
	return v.h.scr.Key(code, down)
}

// MaxTypeRunes bounds one Type call.
const MaxTypeRunes = 4096

// Type enters text.
func (v *Viewer) Type(text string) error {
	done, err := v.input()
	if err != nil {
		return err
	}
	defer done()
	r := []rune(text)
	if len(r) > MaxTypeRunes {
		return fmt.Errorf("text too long (%d characters, at most %d)", len(r), MaxTypeRunes)
	}
	v.h.mu.Lock()
	v.typed += len(r)
	v.h.mu.Unlock()
	return v.h.scr.Type(text)
}

// SetClipboard puts text on the host's clipboard.
func (v *Viewer) SetClipboard(text string) error {
	if !v.control {
		return errViewOnly
	}
	h := v.h
	if err := h.scr.SetClipboard(text); err != nil {
		return err
	}
	h.mu.Lock()
	h.clipText, h.clipSeq = text, h.scr.ClipboardSeq()
	h.mu.Unlock()
	return nil
}

// Clipboard reads the host's clipboard.
func (v *Viewer) Clipboard() (string, error) {
	if !v.control {
		return "", errViewOnly
	}
	return v.h.scr.Clipboard()
}

// pollClipboard forwards host clipboard changes while someone is connected.
// Text that came from a viewer is not echoed back.
func (h *Hub) pollClipboard() {
	h.mu.Lock()
	n := len(h.viewers)
	h.mu.Unlock()
	seq := h.scr.ClipboardSeq()
	if n == 0 || seq == 0 {
		return
	}
	h.mu.Lock()
	if seq == h.clipSeq {
		h.mu.Unlock()
		return
	}
	h.clipSeq = seq
	h.mu.Unlock()
	text, err := h.scr.Clipboard()
	if err != nil || text == "" {
		return
	}
	h.mu.Lock()
	same := text == h.clipText
	h.clipText = text
	h.mu.Unlock()
	if !same && h.opt.OnClipboard != nil {
		h.opt.OnClipboard(text)
	}
}
