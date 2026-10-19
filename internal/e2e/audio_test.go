package e2e

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/audit/audittest"
	"github.com/dmdhrumilmistry/setu/internal/desktop"
	"github.com/dmdhrumilmistry/setu/internal/desktop/desktoptest"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// toneSource plays a 440 Hz tone until closed, or fails when fail is set.
type toneSource struct {
	mu     sync.Mutex
	closed bool
	n      int
	fail   error
}

func (s *toneSource) Format() (int, int) { return 16000, 2 }

func (s *toneSource) Read() ([]int16, error) {
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("closed")
	}
	if s.fail != nil {
		return nil, s.fail
	}
	out := make([]int16, 320*2)
	for i := range 320 {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(s.n+i)/16000))
		out[2*i], out[2*i+1] = v, v
	}
	s.n += 320
	return out, nil
}

func (s *toneSource) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *toneSource) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// startDesktop runs a desktop host with cfg and connects one control client.
func startDesktop(t *testing.T, ctx context.Context, dc *host.DesktopConfig, al *audit.Logger) (*deskClient, proto.Control) {
	t.Helper()
	offers := make(chan string, 1)
	answers := make(chan string, 1)
	s, err := host.New(host.Config{
		Desktop:    dc,
		Manual:     true,
		ICE:        []proto.ICEServer{},
		Log:        newTlog(t),
		Audit:      al,
		OnInvite:   func(inv host.Invite) { offers <- inv.Code },
		ReadAnswer: func(ctx context.Context) (string, error) { return <-answers, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	c, answer := newDeskClient(t, ctx, <-offers)
	answers <- answer
	select {
	case c.ctlDC = <-c.dc:
	case <-time.After(30 * time.Second):
		t.Fatal("data channel never opened")
	}
	c.expect(proto.CtlHello)
	c.send(proto.Control{T: proto.CtlAuth})
	ready := c.expect(proto.CtlReady)
	c.expect(proto.CtlScreen)
	return c, ready
}

func (c *deskClient) noAudio(d time.Duration) {
	c.t.Helper()
	select {
	case <-c.audio:
		c.t.Fatal("got sound while it was off")
	case <-time.After(d):
	}
}

func (c *deskClient) drainAudio() {
	for {
		select {
		case <-c.audio:
		default:
			return
		}
	}
}

func TestDesktopAudio(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	col := audittest.New(t)
	al, err := audit.New(ctx, audit.Config{Endpoint: col.URL, Required: true}, audit.Options{Version: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sources []*toneSource
	scr := desktoptest.New(desktop.Display{Name: "main", W: 64, H: 64, Primary: true})
	c, ready := startDesktop(t, ctx, &host.DesktopConfig{Screen: scr, Display: -1, Audio: true, OpenAudio: func() (desktop.AudioSource, error) {
		s := &toneSource{}
		mu.Lock()
		sources = append(sources, s)
		mu.Unlock()
		return s, nil
	}}, al)
	if !ready.Audio {
		t.Fatal(`"ready" does not offer sound`)
	}
	if c.audioReliable {
		t.Error("the audio channel must be unordered and never retransmit")
	}
	// Nothing is captured until the client asks.
	c.noAudio(300 * time.Millisecond)
	mu.Lock()
	n := len(sources)
	mu.Unlock()
	if n != 0 {
		t.Fatal("sound captured before anyone listened")
	}

	c.send(proto.Control{T: proto.CtlAudio, Audio: true})
	for range 5 {
		select {
		case b := <-c.audio:
			// 16 kHz / 50 = 320 frames, 2 channels: 10 + 2*4 + 2*160 bytes.
			if b[0] != desktop.MsgAudio || b[1] != 2 || len(b) != 10+8+320 {
				t.Fatalf("audio packet: type %d, %d channels, %d bytes", b[0], b[1], len(b))
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no sound after turning it on")
		}
	}

	c.send(proto.Control{T: proto.CtlAudio})
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	mu.Lock()
	first := sources[0]
	mu.Unlock()
	waitFor("capture kept running after the sound was turned off", first.isClosed)
	time.Sleep(100 * time.Millisecond)
	c.drainAudio()
	c.noAudio(300 * time.Millisecond)

	// A capture failure turns the sound off and says why.
	c.send(proto.Control{T: proto.CtlAudio, Audio: true})
	waitFor("no second capture", func() bool { mu.Lock(); defer mu.Unlock(); return len(sources) == 2 })
	mu.Lock()
	sources[1].mu.Lock()
	sources[1].fail = errors.New("device unplugged")
	sources[1].mu.Unlock()
	mu.Unlock()
	if m := c.expect(proto.CtlAudio); m.Audio || m.Msg == "" {
		t.Fatalf("stop message %+v", m)
	}

	c.ctlDC.Close()
	time.Sleep(700 * time.Millisecond)
	if err := al.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	recs := col.Events("setu.desktop.sound")
	if len(recs) != 3 || audittest.Attr(recs[0], "setu.desktop.sound") != "true" || audittest.Attr(recs[1], "setu.desktop.sound") != "false" {
		t.Fatalf("sound records: %v", recs)
	}
}

// Without --audio there is no audio channel, and asking for sound is refused.
func TestDesktopNoAudio(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scr := desktoptest.New(desktop.Display{Name: "main", W: 64, H: 64, Primary: true})
	opened := false
	c, ready := startDesktop(t, ctx, &host.DesktopConfig{Screen: scr, Display: -1, OpenAudio: func() (desktop.AudioSource, error) {
		opened = true
		return &toneSource{}, nil
	}}, nil)
	if ready.Audio {
		t.Fatal(`"ready" offers sound without --audio`)
	}
	c.send(proto.Control{T: proto.CtlAudio, Audio: true})
	if m := c.expect(proto.CtlAudio); m.Audio || m.Msg == "" {
		t.Fatalf("reply %+v", m)
	}
	c.noAudio(300 * time.Millisecond)
	if opened {
		t.Fatal("sound captured without --audio")
	}
}

// TestDesktopAudioLive streams this computer's real sound through a host to
// a client while SETU_AUDIO_PROBE (a command that plays a sound) runs; see
// TestAudioProbe in internal/desktop.
func TestDesktopAudioLive(t *testing.T) {
	cmd := os.Getenv("SETU_AUDIO_PROBE")
	if cmd == "" {
		t.Skip("set SETU_AUDIO_PROBE to a command that plays a sound")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scr := desktoptest.New(desktop.Display{Name: "main", W: 64, H: 64, Primary: true})
	c, ready := startDesktop(t, ctx, &host.DesktopConfig{Screen: scr, Display: -1, Audio: true}, nil)
	if !ready.Audio {
		t.Fatal(`"ready" does not offer sound`)
	}
	c.send(proto.Control{T: proto.CtlAudio, Audio: true})
	time.Sleep(time.Second) // let the capture start
	play := exec.Command("sh", "-c", cmd)
	if err := play.Start(); err != nil {
		t.Fatal(err)
	}
	defer play.Wait()
	select {
	case b := <-c.audio:
		t.Logf("first packet: %d bytes, %d channel(s), %d Hz", len(b), b[1], binary.LittleEndian.Uint16(b[2:]))
	case m := <-c.ctl:
		t.Fatalf("host: %+v", m)
	case <-time.After(15 * time.Second):
		t.Fatal("no sound arrived")
	}
}
