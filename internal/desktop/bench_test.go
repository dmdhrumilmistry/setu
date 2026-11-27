package desktop

import (
	"image"
	"image/color"
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

// BenchmarkEncodeFrame measures encoding a whole 1080p frame, the cost of a
// full refresh for one viewer: the hot path that a faster encoder would
// speed up.
func BenchmarkEncodeFrame(b *testing.B) {
	for _, kind := range []string{"text", "photo"} {
		img := benchFrame(kind)
		b.Run(kind, func(b *testing.B) {
			b.SetBytes(int64(len(img.Pix)))
			b.ReportAllocs()
			size := 0
			for b.Loop() {
				size = 0
				for _, m := range encodeRect(img, img.Rect, 70) {
					size += len(m)
				}
			}
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
