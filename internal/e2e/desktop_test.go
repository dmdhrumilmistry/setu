package e2e

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/audit/audittest"
	"github.com/dmdhrumilmistry/setu/internal/desktop"
	"github.com/dmdhrumilmistry/setu/internal/desktop/desktoptest"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/pion/webrtc/v4"
)

// deskClient is a minimal desktop client speaking the wire protocol the web
// client uses.
type deskClient struct {
	t      *testing.T
	pc     *webrtc.PeerConnection
	dc     chan *webrtc.DataChannel
	ctl    chan proto.Control
	frames chan []byte
	ctlDC  *webrtc.DataChannel
}

func newDeskClient(t *testing.T, ctx context.Context, offerCode string) (*deskClient, string) {
	t.Helper()
	var offer proto.Signal
	if err := link.DecodeCode(offerCode, &offer); err != nil {
		t.Fatal(err)
	}
	pc, err := rtc.NewPeer(offer.ICE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	c := &deskClient{t: t, pc: pc, dc: make(chan *webrtc.DataChannel, 1), ctl: make(chan proto.Control, 64), frames: make(chan []byte, 1024)}
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		switch d.Label() {
		case proto.DataChannelLabel:
			d.OnMessage(func(m webrtc.DataChannelMessage) {
				var ctl proto.Control
				if m.IsString && json.Unmarshal(m.Data, &ctl) == nil {
					c.ctl <- ctl
				}
			})
			d.OnOpen(func() { c.dc <- d })
		case proto.ScreenChannelLabel:
			d.OnMessage(func(m webrtc.DataChannelMessage) { c.frames <- append([]byte(nil), m.Data...) })
		}
	})
	sdp, err := rtc.Answer(ctx, pc, offer.SDP)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := link.EncodeCode(proto.Signal{Type: proto.SigAnswer, SID: offer.SID, SDP: sdp, TS: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return c, answer
}

func (c *deskClient) send(m proto.Control) {
	b, _ := json.Marshal(m)
	if err := c.ctlDC.SendText(string(b)); err != nil {
		c.t.Fatal(err)
	}
}

// expect waits for a control message of type typ.
func (c *deskClient) expect(typ string) proto.Control {
	c.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case m := <-c.ctl:
			if m.T == proto.CtlError {
				c.t.Fatalf("host error: %s", m.Msg)
			}
			if m.T == typ {
				return m
			}
		case <-deadline:
			c.t.Fatalf("no %q message", typ)
		}
	}
}

// frame reads one frame and acknowledges it; it returns the tiles' area.
func (c *deskClient) frame() int {
	c.t.Helper()
	area := 0
	deadline := time.After(20 * time.Second)
	for {
		select {
		case b := <-c.frames:
			switch b[0] {
			case desktop.MsgTile:
				area += int(binary.LittleEndian.Uint16(b[6:])) * int(binary.LittleEndian.Uint16(b[8:]))
			case desktop.MsgFrameEnd:
				c.send(proto.Control{T: proto.CtlAck, Frame: binary.LittleEndian.Uint32(b[1:])})
				return area
			}
		case <-deadline:
			c.t.Fatal("no frame")
		}
	}
}

func waitEvents(t *testing.T, scr *desktoptest.Screen, want []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := scr.Events(); len(got) >= len(want) {
			if !slices.Equal(got[:len(want)], want) {
				t.Fatalf("events\n got %q\nwant %q", got, want)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("events %q, want %q", scr.Events(), want)
}

func TestDesktop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	col := audittest.New(t)
	al, err := audit.New(ctx, audit.Config{Endpoint: col.URL, Required: true}, audit.Options{Version: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	scr := desktoptest.New(desktop.Display{Name: "main", W: 320, H: 200, Primary: true}, desktop.Display{Name: "side", X: 320, W: 160, H: 100})
	offers := make(chan string, 1)
	answers := make(chan string, 1)
	s, err := host.New(host.Config{
		Desktop:    &host.DesktopConfig{Screen: scr, FPS: 30, Display: -1},
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
	if ready.Kind != proto.KindDesktop || ready.Role != proto.RoleControl {
		t.Fatalf("ready %+v", ready)
	}
	scrMsg := c.expect(proto.CtlScreen)
	if len(scrMsg.Displays) != 2 || scrMsg.Display == nil || *scrMsg.Display != 0 || scrMsg.Displays[0].W != 320 {
		t.Fatalf("screen %+v", scrMsg)
	}
	if got := c.frame(); got != 320*200 {
		t.Fatalf("first frame covers %d pixels, want %d", got, 320*200)
	}
	scr.Fill(image.Rect(5, 5, 20, 20), color.RGBA{0, 200, 0, 255})
	for c.frame() == 0 {
	}

	c.send(proto.Control{T: proto.CtlPointer, X: 30, Y: 40, Buttons: desktop.ButtonLeft})
	c.send(proto.Control{T: proto.CtlPointer, X: 30, Y: 40})
	c.send(proto.Control{T: proto.CtlKey, Key: "KeyA", Down: true})
	c.send(proto.Control{T: proto.CtlKey, Key: "KeyA"})
	c.send(proto.Control{T: proto.CtlText, Text: "hi"})
	waitEvents(t, scr, []string{"move 30,40", "button 1 true", "move 30,40", "button 1 false", "key KeyA true", "key KeyA false", `type "hi"`})

	// Clipboard to the host, sent in two parts.
	c.send(proto.Control{T: proto.CtlClipboard, Text: "secret ", More: true})
	c.send(proto.Control{T: proto.CtlClipboard, Text: "token"})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if text, _ := scr.Clipboard(); text == "secret token" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clipboard never reached the host")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// And a host-side copy back to the client.
	scr.SetHostClipboard("from host")
	if m := c.expect(proto.CtlClipboard); m.Text != "from host" || m.More {
		t.Fatalf("clipboard message %+v", m)
	}

	one := 1
	c.send(proto.Control{T: proto.CtlSelectDisplay, Display: &one})
	if m := c.expect(proto.CtlScreen); m.Display == nil || *m.Display != 1 {
		t.Fatalf("display switch %+v", m)
	}
	for c.frame() != 160*100 {
	}

	c.ctlDC.Close()
	time.Sleep(700 * time.Millisecond) // let the host notice
	if err := al.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	start := col.Events("setu.session.start")
	if len(start) != 1 || audittest.Attr(start[0], "setu.session.kind") != proto.KindDesktop {
		t.Fatalf("session start records: %v", start)
	}
	clips := col.Events("setu.desktop.clipboard")
	if len(clips) != 2 {
		t.Fatalf("want 2 clipboard records, got %d", len(clips))
	}
	for _, r := range clips {
		if strings.Contains(audittest.Body(r), "secret") || strings.Contains(audittest.Body(r), "from host") {
			t.Fatalf("clipboard text leaked into the audit log: %s", audittest.Body(r))
		}
	}
	if audittest.Attr(clips[0], "setu.clipboard.direction") != audit.ClipboardToHost || audittest.Attr(clips[1], "setu.clipboard.direction") != audit.ClipboardToClient {
		t.Errorf("clipboard directions: %v / %v", clips[0].Attributes, clips[1].Attributes)
	}
	if len(col.Events("setu.desktop.display")) != 1 {
		t.Error("no display switch record")
	}
	in := col.Events("setu.desktop.input")
	if len(in) != 1 || audittest.Attr(in[0], "setu.desktop.clicks") != "1" || audittest.Attr(in[0], "setu.desktop.typed_chars") != "2" {
		t.Fatalf("input summary records: %v", in)
	}
}

// TestDesktopRefusesTerminalClient checks that `setu join` explains that a
// desktop invite needs the browser.
func TestDesktopRefusesTerminalClient(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scr := desktoptest.New(desktop.Display{Name: "main", W: 64, H: 64, Primary: true})
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) {
		c.Command = nil
		c.Desktop = &host.DesktopConfig{Screen: scr, Display: -1}
	})
	if _, err := dial(t, ctx, ctl, ""); err == nil || !strings.Contains(err.Error(), "remote desktop") {
		t.Fatalf("terminal client joined a desktop share (err %v)", err)
	}
}
