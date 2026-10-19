package desktop

import (
	"encoding/binary"
	"errors"
	"sync"
)

// AudioSource captures what this computer plays (its sound output, not the
// microphone). Close must unblock a pending Read.
type AudioSource interface {
	// Format is the sample rate (Hz) and channel count of Read's samples.
	Format() (rate, channels int)
	// Read blocks until there are samples and returns them interleaved.
	// The slice is only valid until the next Read.
	Read() ([]int16, error)
	Close() error
}

// ErrAudioUnsupported is returned by OpenAudio on platforms without a
// sound capture backend.
var ErrAudioUnsupported = errors.New("sharing sound is not supported on this platform")

// OpenAudio starts capturing this computer's sound output.
func OpenAudio(opt OpenOptions) (AudioSource, error) { return openAudio(opt) }

// Wire format of the binary messages on the audio channel. Integers are
// little endian.
//
//	[MsgAudio, channels u8, rate u16, seq u32, frames u16,
//	 per channel: predictor i16, step index u8, 0 u8,
//	 per channel: ceil(frames/2) bytes of 4-bit IMA ADPCM, low nibble first]
//
// Every packet carries the decoder state it starts from, so it decodes on
// its own: the channel is unordered and never retransmits, and a lost
// packet is a short gap instead of a growing delay.
const (
	MsgAudio     = 1
	audioHeader  = 10
	audioPerChan = 4
	// 20 ms per packet: short enough to start playing quickly, long
	// enough that headers stay a small part of the stream.
	audioPacketsPerSecond = 50
	maxAudioChannels      = 2
)

var adpcmSteps = [89]int32{
	7, 8, 9, 10, 11, 12, 13, 14, 16, 17, 19, 21, 23, 25, 28, 31, 34, 37, 41, 45,
	50, 55, 60, 66, 73, 80, 88, 97, 107, 118, 130, 143, 157, 173, 190, 209, 230,
	253, 279, 307, 337, 371, 408, 449, 494, 544, 598, 658, 724, 796, 876, 963,
	1060, 1166, 1282, 1411, 1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024, 3327,
	3660, 4026, 4428, 4871, 5358, 5894, 6484, 7132, 7845, 8630, 9493, 10442,
	11487, 12635, 13899, 15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
	32767,
}

var adpcmIndex = [16]int32{-1, -1, -1, -1, 2, 4, 6, 8, -1, -1, -1, -1, 2, 4, 6, 8}

// adpcm is the IMA ADPCM state of one channel.
type adpcm struct {
	pred  int32
	index int32
}

// encode returns the 4-bit code for s and advances the state exactly as
// decode does, so encoder and decoder never drift apart.
func (a *adpcm) encode(s int16) byte {
	step := adpcmSteps[a.index]
	diff := int32(s) - a.pred
	var code byte
	if diff < 0 {
		code = 8
		diff = -diff
	}
	if diff >= step {
		code |= 4
		diff -= step
	}
	if diff >= step>>1 {
		code |= 2
		diff -= step >> 1
	}
	if diff >= step>>2 {
		code |= 1
	}
	a.decode(code)
	return code
}

func (a *adpcm) decode(code byte) int16 {
	step := adpcmSteps[a.index]
	d := step >> 3
	if code&4 != 0 {
		d += step
	}
	if code&2 != 0 {
		d += step >> 1
	}
	if code&1 != 0 {
		d += step >> 2
	}
	if code&8 != 0 {
		a.pred -= d
	} else {
		a.pred += d
	}
	a.pred = max(-32768, min(32767, a.pred))
	a.index = max(0, min(88, a.index+adpcmIndex[code]))
	return int16(a.pred)
}

// encodeAudio encodes interleaved samples (frames x len(st) channels) as
// one packet, advancing st.
func encodeAudio(seq uint32, rate int, st []adpcm, samples []int16) []byte {
	ch := len(st)
	frames := len(samples) / ch
	per := (frames + 1) / 2
	b := make([]byte, audioHeader+ch*audioPerChan+ch*per)
	b[0], b[1] = MsgAudio, byte(ch)
	binary.LittleEndian.PutUint16(b[2:], uint16(rate))
	binary.LittleEndian.PutUint32(b[4:], seq)
	binary.LittleEndian.PutUint16(b[8:], uint16(frames))
	for c := range st {
		h := b[audioHeader+c*audioPerChan:]
		binary.LittleEndian.PutUint16(h, uint16(int16(st[c].pred)))
		h[2] = byte(st[c].index)
	}
	data := b[audioHeader+ch*audioPerChan:]
	for c := range st {
		out := data[c*per:]
		for i := 0; i < frames; i++ {
			code := st[c].encode(samples[i*ch+c])
			if i&1 == 0 {
				out[i/2] = code
			} else {
				out[i/2] |= code << 4
			}
		}
	}
	return b
}

// silent reports whether a packet would be inaudible, so it is not sent.
func silent(samples []int16) bool {
	for _, s := range samples {
		if s > 2 || s < -2 {
			return false
		}
	}
	return true
}

// AudioSink delivers one audio packet to a listener. Packets that cannot be
// sent right away should be dropped: late sound is worse than a gap.
type AudioSink interface {
	SendAudio(b []byte)
}

// AudioOptions configures an AudioHub.
type AudioOptions struct {
	// Open starts a capture; nil captures this computer's sound.
	Open func() (AudioSource, error)
	Logf func(format string, args ...any)
	// OnStop is called when capture fails. The hub has already dropped
	// sinks, which must listen again to retry.
	OnStop func(err error, sinks []AudioSink)
}

// AudioHub captures the sound once for every listener, and only while
// someone listens.
type AudioHub struct {
	opt   AudioOptions
	mu    sync.Mutex
	sinks map[AudioSink]bool
	run   *audioRun // the capture in progress, if any
}

type audioRun struct {
	mu     sync.Mutex
	src    AudioSource // set once open
	closed bool
}

// stop ends the run; a capture still opening closes as soon as it opens.
func (r *audioRun) stop() {
	r.mu.Lock()
	r.closed = true
	src := r.src
	r.mu.Unlock()
	if src != nil {
		src.Close()
	}
}

// NewAudioHub returns a hub that captures nothing until someone listens.
func NewAudioHub(opt AudioOptions) *AudioHub {
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.Open == nil {
		opt.Open = func() (AudioSource, error) { return OpenAudio(OpenOptions{}) }
	}
	return &AudioHub{opt: opt, sinks: map[AudioSink]bool{}}
}

// Listen sends the sound to s from now on, starting the capture if s is
// the first listener.
func (h *AudioHub) Listen(s AudioSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sinks == nil { // closed
		return
	}
	h.sinks[s] = true
	if h.run == nil {
		h.run = &audioRun{}
		go h.capture(h.run)
	}
}

// Unlisten stops sending to s, and stops the capture after the last
// listener. It reports whether s was listening.
func (h *AudioHub) Unlisten(s AudioSink) bool {
	h.mu.Lock()
	was := h.sinks[s]
	delete(h.sinks, s)
	var run *audioRun
	if len(h.sinks) == 0 {
		run, h.run = h.run, nil
	}
	h.mu.Unlock()
	if run != nil {
		run.stop()
	}
	return was
}

// Close stops the capture and drops every listener.
func (h *AudioHub) Close() {
	h.mu.Lock()
	run := h.run
	h.run, h.sinks = nil, nil
	h.mu.Unlock()
	if run != nil {
		run.stop()
	}
}

func (h *AudioHub) listeners(run *audioRun) []AudioSink {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.run != run {
		return nil
	}
	out := make([]AudioSink, 0, len(h.sinks))
	for s := range h.sinks {
		out = append(out, s)
	}
	return out
}

func (h *AudioHub) capture(run *audioRun) {
	err := h.stream(run)
	run.mu.Lock()
	stopped := run.closed
	run.mu.Unlock()
	if stopped { // nobody is listening any more: not a failure
		return
	}
	h.mu.Lock()
	var dropped []AudioSink
	if h.run == run {
		h.run = nil
		for s := range h.sinks {
			dropped = append(dropped, s)
		}
		clear(h.sinks)
	}
	h.mu.Unlock()
	h.opt.Logf("WARNING: sound capture stopped: %v", err)
	if h.opt.OnStop != nil && len(dropped) > 0 {
		h.opt.OnStop(err, dropped)
	}
}

// stream captures and sends packets until the run stops or capture fails.
func (h *AudioHub) stream(run *audioRun) error {
	src, err := h.opt.Open()
	if err != nil {
		return err
	}
	run.mu.Lock()
	if run.closed {
		run.mu.Unlock()
		src.Close()
		return nil
	}
	run.src = src
	run.mu.Unlock()
	defer src.Close()

	rate, ch := src.Format()
	if rate <= 0 || rate > 65535 || ch < 1 || ch > maxAudioChannels {
		return errors.New("the sound device uses an unsupported format")
	}
	h.opt.Logf("capturing sound (%d Hz, %d channel(s))", rate, ch)
	st := make([]adpcm, ch)
	n := rate / audioPacketsPerSecond * ch
	buf := make([]int16, 0, 2*n)
	var seq uint32
	for {
		s, err := src.Read()
		if err != nil {
			run.mu.Lock()
			closed := run.closed
			run.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		buf = append(buf, s...)
		for len(buf) >= n {
			pkt := buf[:n]
			seq++
			if silent(pkt) {
				// The decoder restarts from each packet's header, so
				// skipped packets only need the encoder to settle.
				st = make([]adpcm, ch)
			} else {
				b := encodeAudio(seq, rate, st, pkt)
				for _, sink := range h.listeners(run) {
					sink.SendAudio(b)
				}
			}
			buf = append(buf[:0], buf[n:]...)
		}
	}
}

// floatSample converts a float sample in [-1, 1] to 16 bits.
func floatSample(f float32) int16 {
	f *= 32767
	if f > 32767 {
		return 32767
	}
	if f < -32768 {
		return -32768
	}
	return int16(f)
}
