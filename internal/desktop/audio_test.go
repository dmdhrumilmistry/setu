package desktop

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

// decodeAudio is the inverse of encodeAudio (web/js/audio.mjs is the
// client's decoder). It returns interleaved samples.
func decodeAudio(b []byte) (rate, ch int, seq uint32, samples []int16, err error) {
	if len(b) < audioHeader || b[0] != MsgAudio || b[1] == 0 || b[1] > maxAudioChannels {
		return 0, 0, 0, nil, errors.New("not an audio packet")
	}
	ch = int(b[1])
	rate = int(binary.LittleEndian.Uint16(b[2:]))
	seq = binary.LittleEndian.Uint32(b[4:])
	frames := int(binary.LittleEndian.Uint16(b[8:]))
	per := (frames + 1) / 2
	if len(b) != audioHeader+ch*audioPerChan+ch*per {
		return 0, 0, 0, nil, errors.New("audio packet has the wrong length")
	}
	samples = make([]int16, frames*ch)
	data := b[audioHeader+ch*audioPerChan:]
	for c := 0; c < ch; c++ {
		h := b[audioHeader+c*audioPerChan:]
		a := adpcm{pred: int32(int16(binary.LittleEndian.Uint16(h))), index: min(88, int32(h[2]))}
		for i := 0; i < frames; i++ {
			code := data[c*per+i/2]
			if i&1 == 1 {
				code >>= 4
			}
			samples[i*ch+c] = a.decode(code & 15)
		}
	}
	return rate, ch, seq, samples, nil
}

func tone(frames, ch, rate int, hz float64) []int16 {
	s := make([]int16, frames*ch)
	for i := 0; i < frames; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
		for c := 0; c < ch; c++ {
			s[i*ch+c] = v / int16(c+1)
		}
	}
	return s
}

func TestADPCMRoundTrip(t *testing.T) {
	const rate, ch = 48000, 2
	st := make([]adpcm, ch)
	in := tone(4*960, ch, rate, 440)
	var out []int16
	for i := 0; i < 4; i++ {
		pkt := encodeAudio(uint32(i+1), rate, st, in[i*960*ch:(i+1)*960*ch])
		r, c, seq, s, err := decodeAudio(pkt)
		if err != nil {
			t.Fatal(err)
		}
		if r != rate || c != ch || seq != uint32(i+1) || len(s) != 960*ch {
			t.Fatalf("packet %d: rate %d, channels %d, seq %d, %d samples", i, r, c, seq, len(s))
		}
		out = append(out, s...)
	}
	// Signal to noise ratio, skipping the first packet while the step size settles.
	var sig, noise float64
	for i := 960 * ch; i < len(in); i++ {
		sig += float64(in[i]) * float64(in[i])
		d := float64(in[i]) - float64(out[i])
		noise += d * d
	}
	if snr := 10 * math.Log10(sig/noise); snr < 25 {
		t.Errorf("SNR %.1f dB, want at least 25", snr)
	}
}

// Every packet decodes on its own: losing one must not affect the next.
func TestADPCMPacketsIndependent(t *testing.T) {
	st := make([]adpcm, 1)
	in := tone(3*960, 1, 48000, 1000)
	encodeAudio(1, 48000, st, in[:960])
	encodeAudio(2, 48000, st, in[960:1920]) // "lost"
	third := encodeAudio(3, 48000, st, in[1920:])
	full := make([]adpcm, 1)
	encodeAudio(1, 48000, full, in[:1920])
	want := encodeAudio(3, 48000, full, in[1920:])
	_, _, _, got, err := decodeAudio(third)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, ref, _ := decodeAudio(want)
	for i := range got {
		if got[i] != ref[i] {
			t.Fatalf("sample %d: %d, want %d", i, got[i], ref[i])
		}
	}
}

func TestDecodeAudioRejects(t *testing.T) {
	good := encodeAudio(1, 48000, make([]adpcm, 2), tone(961, 2, 48000, 300)) // odd frame count
	if _, _, _, _, err := decodeAudio(good); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"short":     good[:5],
		"truncated": good[:len(good)-1],
		"type":      append([]byte{9}, good[1:]...),
		"channels":  append([]byte{MsgAudio, 3}, good[2:]...),
	} {
		if _, _, _, _, err := decodeAudio(b); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
}

// fakeAudio plays a tone, then silence, forever (or fails).
type fakeAudio struct {
	mu     sync.Mutex
	closed bool
	n      int
	fail   error
	done   chan struct{}
}

func (f *fakeAudio) Format() (int, int) { return 8000, 1 }

func (f *fakeAudio) Read() ([]int16, error) {
	time.Sleep(time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("closed")
	}
	if f.fail != nil {
		return nil, f.fail
	}
	f.n++
	if f.n > 10 {
		return make([]int16, 160), nil // silence
	}
	return tone(160, 1, 8000, 440), nil
}

func (f *fakeAudio) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.done)
	}
	return nil
}

type audioRecorder struct {
	mu  sync.Mutex
	got [][]byte
}

func (r *audioRecorder) SendAudio(b []byte) {
	r.mu.Lock()
	r.got = append(r.got, b)
	r.mu.Unlock()
}

func (r *audioRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAudioHubCapturesOnlyWhileListened(t *testing.T) {
	var mu sync.Mutex
	var opened []*fakeAudio
	h := NewAudioHub(AudioOptions{Open: func() (AudioSource, error) {
		f := &fakeAudio{done: make(chan struct{})}
		mu.Lock()
		opened = append(opened, f)
		mu.Unlock()
		return f, nil
	}})
	defer h.Close()
	a, b := &audioRecorder{}, &audioRecorder{}
	h.Listen(a)
	h.Listen(b)
	waitFor(t, "sound", func() bool { return a.count() > 0 && b.count() > 0 })
	// 10 reads of 160 samples = 10 packets of 160 (8000 Hz / 50); silence is not sent.
	time.Sleep(50 * time.Millisecond)
	if n := a.count(); n != 10 {
		t.Errorf("listener got %d packets, want 10 (silence must not be sent)", n)
	}
	if !h.Unlisten(a) || h.Unlisten(a) {
		t.Error("Unlisten should report whether the sink was listening")
	}
	mu.Lock()
	first := opened[0]
	mu.Unlock()
	select {
	case <-first.done:
		t.Fatal("capture stopped while someone still listens")
	case <-time.After(20 * time.Millisecond):
	}
	h.Unlisten(b)
	select {
	case <-first.done:
	case <-time.After(5 * time.Second):
		t.Fatal("capture kept running after the last listener left")
	}
	h.Listen(a)
	waitFor(t, "a second capture", func() bool { mu.Lock(); defer mu.Unlock(); return len(opened) == 2 })
}

func TestAudioHubReportsFailure(t *testing.T) {
	stopped := make(chan []AudioSink, 1)
	h := NewAudioHub(AudioOptions{
		Open:   func() (AudioSource, error) { return nil, errors.New("no sound device") },
		OnStop: func(err error, sinks []AudioSink) { stopped <- sinks },
	})
	defer h.Close()
	a := &audioRecorder{}
	h.Listen(a)
	select {
	case sinks := <-stopped:
		if len(sinks) != 1 || sinks[0] != a {
			t.Errorf("OnStop got %v", sinks)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no OnStop")
	}
	if h.Unlisten(a) {
		t.Error("a failed capture should drop its listeners")
	}
}

func TestAudioHubClose(t *testing.T) {
	f := &fakeAudio{done: make(chan struct{})}
	h := NewAudioHub(AudioOptions{Open: func() (AudioSource, error) { return f, nil }})
	h.Listen(&audioRecorder{})
	time.Sleep(10 * time.Millisecond)
	h.Close()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the capture")
	}
	h.Listen(&audioRecorder{}) // after Close: ignored, no panic
}

var updateGolden = flag.Bool("update", false, "rewrite web/test/testdata/audio.json")

// The web client decodes the same packet (web/test/audio.test.mjs): this
// keeps the two in step.
func TestAudioGoldenVector(t *testing.T) {
	const path = "../../web/test/testdata/audio.json"
	in := tone(41, 2, 16000, 700) // odd frame count: a half-used last byte
	pkt := encodeAudio(7, 16000, make([]adpcm, 2), in)
	_, _, _, out, err := decodeAudio(pkt)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(map[string]any{"packet": hex.EncodeToString(pkt), "rate": 16000, "seq": 7, "samples": out}, "", " ")
	want = append(want, '\n')
	if *updateGolden {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows checkouts may turn the file's line endings into CRLF.
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Fatalf("%s is stale: run go test ./internal/desktop -run Golden -update", path)
	}
}
