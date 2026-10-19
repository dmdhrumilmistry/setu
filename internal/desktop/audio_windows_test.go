//go:build windows

package desktop

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestParseMixFormat(t *testing.T) {
	// WAVEFORMATEXTENSIBLE, 48 kHz, 6 channels, 32-bit float.
	b := make([]byte, waveFormatExtensibleLen)
	binary.LittleEndian.PutUint16(b, waveFormatExtensible)
	binary.LittleEndian.PutUint16(b[2:], 6)
	binary.LittleEndian.PutUint32(b[4:], 48000)
	binary.LittleEndian.PutUint16(b[14:], 32)
	binary.LittleEndian.PutUint16(b[16:], 22)
	binary.LittleEndian.PutUint16(b[24:], waveFormatIEEEFloat)
	f, err := parseMixFormat(b)
	if err != nil || !f.float || f.channels != 6 || f.rate != 48000 {
		t.Fatalf("got %+v, %v", f, err)
	}
	binary.LittleEndian.PutUint16(b[24:], 0x55) // MP3: not a mix format
	if _, err := parseMixFormat(b); err == nil {
		t.Error("accepted an unsupported format")
	}
}

func TestConverterDownmixesAndDecimates(t *testing.T) {
	// 96 kHz, 4 channels of float: keep the first two, halve the rate.
	c := converter{f: mixFormat{rate: 96000, channels: 4, bits: 32, float: true}, factor: 2}
	raw := make([]byte, 4*4*4) // 4 frames
	for i := 0; i < 4; i++ {
		for k := 0; k < 4; k++ {
			binary.LittleEndian.PutUint32(raw[(i*4+k)*4:], math.Float32bits(float32(k+1)/10))
		}
	}
	out := c.convert(raw, 4)
	if len(out) != 4 { // 2 frames x 2 channels
		t.Fatalf("got %d samples, want 4", len(out))
	}
	if out[0] != floatSample(0.1) || out[1] != floatSample(0.2) {
		t.Errorf("got %v", out)
	}
	if s := c.convert(nil, 3); len(s) != 2 || s[0] != 0 { // silence; one frame left over
		t.Errorf("silence: got %v", s)
	}
}
