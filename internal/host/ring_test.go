package host

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// TestAppendRing checks the allocation-free replay window against the
// obvious implementation (append, keep the last replayBytes) over random
// chunk sizes, including chunks larger than the window.
func TestAppendRing(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	var s Session
	var want []byte
	next := byte(0)
	for i := range 3000 {
		n := r.IntN(40 << 10)
		if i%500 == 0 {
			n = replayBytes + 1234 // bigger than the window
		}
		b := make([]byte, n)
		for j := range b {
			b[j] = next
			next++
		}
		s.appendRing(b)
		want = append(want, b...)
		if len(want) > replayBytes {
			want = want[len(want)-replayBytes:]
		}
		if !bytes.Equal(s.ring, want) {
			t.Fatalf("chunk %d (%d bytes): ring has %d bytes, differs from the expected %d", i, n, len(s.ring), len(want))
		}
		if cap(s.ringBuf) != 2*replayBytes {
			t.Fatalf("chunk %d: backing array reallocated (cap %d)", i, cap(s.ringBuf))
		}
	}
}

func BenchmarkAppendRing(b *testing.B) {
	var s Session
	chunk := make([]byte, 32<<10)
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for b.Loop() {
		s.appendRing(chunk)
	}
}
