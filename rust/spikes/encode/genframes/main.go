// Command genframes writes the desktop benchmark's two 1920x1080 frames
// (internal/desktop/bench_test.go: same generator and seeds) as raw RGBA,
// and times Go's standard library encoders on them the way setu's
// encodeImage does (PNG BestSpeed for few colours, else JPEG), so the Rust
// encoders can be compared on identical pixels.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"time"
)

func frame(kind string) *image.RGBA {
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

// paletted mirrors setu's: at most 256 colours, else nil.
func paletted(img *image.RGBA) *image.Paletted {
	b := img.Bounds()
	index := map[uint32]uint8{}
	var pal color.Palette
	out := image.NewPaletted(b, nil)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			o := img.PixOffset(x, y)
			p := img.Pix[o : o+4]
			k := uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
			i, ok := index[k]
			if !ok {
				if len(pal) == 256 {
					return nil
				}
				i = uint8(len(pal))
				index[k] = i
				pal = append(pal, color.RGBA{p[0], p[1], p[2], p[3]})
			}
			out.Pix[out.PixOffset(x, y)] = i
		}
	}
	out.Palette = pal
	return out
}

func main() {
	dir := os.Args[1]
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	for _, kind := range []string{"text", "photo"} {
		img := frame(kind)
		if err := os.WriteFile(dir+"/"+kind+".rgba", img.Pix, 0o644); err != nil {
			panic(err)
		}
		const n = 10
		var buf bytes.Buffer
		start := time.Now()
		for range n {
			buf.Reset()
			if p := paletted(img); p != nil {
				_ = enc.Encode(&buf, p)
			} else {
				_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70})
			}
		}
		fmt.Printf("go-stdlib %-5s %6.1f ms/frame %7d bytes\n", kind, float64(time.Since(start).Microseconds())/1000/n, buf.Len())
	}
}
