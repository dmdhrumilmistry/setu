package desktop

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/rand/v2"
	"testing"
)

// benchFrame builds a deterministic 1920x1080 frame: "text" is a light page
// of dark glyph-sized marks (few colours: the PNG path), "photo" is smooth
// colour gradients with noise (the JPEG path).
func benchFrame(kind string) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	r := rand.New(rand.NewPCG(7, 11))
	for y := range 1080 {
		for x := range 1920 {
			var c color.RGBA
			switch kind {
			case "text":
				c = color.RGBA{250, 250, 248, 255}
				if y%22 > 4 && y%22 < 18 && x%9 < 6 && (x/9+y/22)%7 != 0 && r.IntN(3) > 0 {
					c = color.RGBA{30, 34, 48, 255}
				}
			default:
				n := uint8(r.IntN(24))
				c = color.RGBA{uint8(x * 255 / 1920), uint8(y * 255 / 1080), uint8((x + y) % 256), 255}
				c.R, c.G, c.B = c.R/2+n, c.G/2+n, c.B/2+n
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// BenchmarkEncodeFrame measures encoding a whole 1080p frame through
// encodeAll (the parallel path the hub uses), the cost of a full refresh.
func BenchmarkEncodeFrame(b *testing.B) {
	for _, kind := range []string{"text", "photo"} {
		img := benchFrame(kind)
		b.Run(kind, func(b *testing.B) {
			b.SetBytes(int64(len(img.Pix)))
			b.ReportAllocs()
			size, msgs := 0, 0
			job := []frameJob{{rects: []image.Rectangle{img.Rect}, q: 70}}
			for b.Loop() {
				size, msgs = 0, 0
				for _, ms := range encodeAll(img, job) {
					for _, m := range ms {
						size += len(m)
						msgs++
					}
				}
			}
			b.ReportMetric(float64(msgs), "msgs/frame")
			b.ReportMetric(float64(size), "bytes/frame")
		})
	}
}

// BenchmarkDiffTiles measures finding the changed tiles between two frames.
func BenchmarkDiffTiles(b *testing.B) {
	prev, cur := benchFrame("photo"), benchFrame("photo")
	cur.Pix[len(cur.Pix)/2] ^= 1
	cols, rows := (1920+TileSize-1)/TileSize, (1080+TileSize-1)/TileSize
	b.SetBytes(int64(len(cur.Pix)))
	for b.Loop() {
		diffTiles(prev, cur, cols, rows)
	}
}

// TestEncodeAllPieces: a large area is split into pieces that each fit in
// one message, cover it exactly, and decode back to the same pixels (the
// text frame takes the lossless PNG path).
func TestEncodeAllPieces(t *testing.T) {
	img := benchFrame("text")
	r := image.Rect(64, 0, 1920, 1080) // not starting at the origin
	got := image.NewRGBA(img.Rect)
	covered := make([]int, img.Rect.Dx()*img.Rect.Dy())
	msgs := encodeAll(img, []frameJob{{rects: []image.Rectangle{r}, q: 70}})[encKey{r, 70}]
	if len(msgs) < 2 {
		t.Fatalf("a %v area was not split (%d messages)", r.Size(), len(msgs))
	}
	for _, m := range msgs {
		if len(m) > MaxMessage {
			t.Fatalf("message of %d bytes exceeds %d", len(m), MaxMessage)
		}
		if m[0] != MsgTile || m[1] != EncPNG {
			t.Fatalf("message type %d, encoding %d", m[0], m[1])
		}
		x, y := int(binary.LittleEndian.Uint16(m[2:])), int(binary.LittleEndian.Uint16(m[4:]))
		w, h := int(binary.LittleEndian.Uint16(m[6:])), int(binary.LittleEndian.Uint16(m[8:]))
		dec, err := png.Decode(bytes.NewReader(m[tileHeader:]))
		if err != nil {
			t.Fatal(err)
		}
		if dec.Bounds().Dx() != w || dec.Bounds().Dy() != h {
			t.Fatalf("piece header says %dx%d, image is %v", w, h, dec.Bounds().Size())
		}
		draw.Draw(got, image.Rect(x, y, x+w, y+h), dec, dec.Bounds().Min, draw.Src)
		for py := y; py < y+h; py++ {
			for px := x; px < x+w; px++ {
				covered[py*img.Rect.Dx()+px]++
			}
		}
	}
	for py := range img.Rect.Dy() {
		for px := range img.Rect.Dx() {
			want := 0
			if (image.Point{px, py}).In(r) {
				want = 1
				if got.RGBAAt(px, py) != img.RGBAAt(px, py) {
					t.Fatalf("pixel %d,%d differs after decoding", px, py)
				}
			}
			if covered[py*img.Rect.Dx()+px] != want {
				t.Fatalf("pixel %d,%d covered %d times, want %d", px, py, covered[py*img.Rect.Dx()+px], want)
			}
		}
	}
}
