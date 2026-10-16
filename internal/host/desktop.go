package host

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/desktop"
	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// DesktopConfig configures a desktop share.
type DesktopConfig struct {
	Screen  desktop.Screen // nil opens this computer's desktop
	FPS     int            // frame rate cap (0 = default)
	Display int            // index of the display to share; -1 = primary
	// X11Display and XAuthority locate an X11 session when the environment
	// does not (a background service); empty = the environment's.
	X11Display string
	XAuthority string
}

type desktopShare struct {
	hub *desktop.Hub
	scr desktop.Screen
}

// inputProblem says why remote control would not work right now, if the
// backend can tell (macOS: missing permission), or "".
func (d *desktopShare) inputProblem() string {
	if c, ok := d.scr.(desktop.InputChecker); ok {
		if err := c.InputStatus(); err != nil {
			return err.Error()
		}
	}
	return ""
}

const (
	maxClipboard  = 1 << 20 // bytes of clipboard text in either direction
	clipboardPart = 8 << 10 // runes per clipboard message (stays under maxInputFrame)
	inputErrEvery = 30 * time.Second
)

var errSlowClient = errors.New("client too slow")

// startDesktop opens the screen and starts streaming. The returned func
// stops it.
func (s *Session) startDesktop(ctx context.Context) (func(), error) {
	dc := s.cfg.Desktop
	scr := dc.Screen
	if scr == nil {
		var err error
		if scr, err = desktop.OpenWith(desktop.OpenOptions{X11Display: dc.X11Display, XAuthority: dc.XAuthority}); err != nil {
			return nil, err
		}
	}
	hub, err := desktop.NewHub(scr, desktop.Options{FPS: dc.FPS, Display: dc.Display, Logf: s.logf,
		OnScreen: s.broadcastScreen, OnClipboard: s.broadcastClipboard, OnStatus: s.broadcastInfo})
	if err != nil {
		if dc.Screen == nil {
			scr.Close()
		}
		return nil, err
	}
	s.desk = &desktopShare{hub: hub, scr: scr}
	ds, cur := hub.Displays()
	s.logf("sharing display %s (%dx%d; %d display(s) attached)", ds[cur].Name, ds[cur].W, ds[cur].H, len(ds))
	if p := s.desk.inputProblem(); p != "" {
		s.logf("WARNING: viewers can see the screen but not control it yet: %s", p)
	}
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.Run(hctx)
	}()
	return func() {
		cancel()
		<-done
		if dc.Screen == nil {
			scr.Close()
		}
	}, nil
}

// hostOS tells desktop clients which keyboard conventions apply here.
func (s *Session) hostOS() string {
	if s.desk == nil {
		return ""
	}
	return runtime.GOOS
}

func screenControl(ds []desktop.Display, cur int) proto.Control {
	info := make([]proto.DisplayInfo, len(ds))
	for i, d := range ds {
		info[i] = proto.DisplayInfo{Name: d.Name, W: d.W, H: d.H, Primary: d.Primary}
	}
	return proto.Control{T: proto.CtlScreen, Displays: info, Display: &cur}
}

// readyPeers returns the streaming clients (only control clients if
// controlOnly).
func (s *Session) readyPeers(controlOnly bool) []*peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*peer
	for _, p := range s.peers {
		if p.isReady() && (!controlOnly || p.role == proto.RoleControl) {
			out = append(out, p)
		}
	}
	return out
}

func (s *Session) broadcastScreen(ds []desktop.Display, cur int) {
	msg := screenControl(ds, cur)
	for _, p := range s.readyPeers(false) {
		p.sendControl(msg)
	}
}

func (s *Session) broadcastInfo(msg string) {
	for _, p := range s.readyPeers(false) {
		p.sendControl(proto.Control{T: proto.CtlInfo, Msg: msg})
	}
}

// broadcastClipboard sends host clipboard changes to control clients only:
// view-only links never see clipboard contents.
func (s *Session) broadcastClipboard(text string) {
	if len(text) > maxClipboard {
		s.logf("host clipboard text not sent: %d bytes is over the %d byte limit", len(text), maxClipboard)
		return
	}
	for _, p := range s.readyPeers(true) {
		p.sendClipboard(text)
		s.audit.Clipboard(p.auditClient(), audit.ClipboardToClient, len(text))
		s.logf("sent the host clipboard (%d bytes) to client #%d", len(text), p.id)
	}
}

// sendClipboard sends text in parts; clipOut keeps two transfers (a host
// copy and a client's request) from interleaving.
func (p *peer) sendClipboard(text string) {
	p.clipOut.Lock()
	defer p.clipOut.Unlock()
	r := []rune(text)
	for i := 0; ; i += clipboardPart {
		end := min(i+clipboardPart, len(r))
		p.sendControl(proto.Control{T: proto.CtlClipboard, Text: string(r[i:end]), More: end < len(r)})
		if end == len(r) {
			return
		}
	}
}

// screenSink sends frames on the peer's screen channel.
type screenSink struct{ p *peer }

func (k screenSink) SendFrame(b []byte) error {
	if k.p.screen.BufferedAmount() > maxBuffered {
		go k.p.close("client too slow")
		return errSlowClient
	}
	return k.p.screen.Send(b)
}

// joinDesktop starts streaming to a peer that just became ready.
func (p *peer) joinDesktop() {
	hub := p.s.desk.hub
	v := hub.Join(screenSink{p}, p.role == proto.RoleControl)
	p.mu.Lock()
	if p.state != stateReady {
		p.mu.Unlock()
		v.Leave()
		return
	}
	p.viewer = v // before "screen", so the client's first replies find it
	p.mu.Unlock()
	ds, cur := hub.Displays()
	p.sendControl(screenControl(ds, cur))
	if p.role == proto.RoleControl {
		if problem := p.s.desk.inputProblem(); problem != "" {
			p.sendControl(proto.Control{T: proto.CtlInfo, Msg: "Remote control is not available yet: " + problem})
		}
	}
}

// leaveDesktop stops streaming and records what the client did.
func (p *peer) leaveDesktop(v *desktop.Viewer, c *audit.Client) {
	a := v.Leave()
	if c == nil || a == (desktop.Activity{}) {
		return
	}
	p.s.audit.DesktopInput(*c, a.Clicks, a.Keys, a.Scrolls, a.TypedChars)
	p.s.logf("client #%d remote input: %d clicks, %d key presses, %d scrolls, %d typed characters",
		p.id, a.Clicks, a.Keys, a.Scrolls, a.TypedChars)
}

// onDesktopMessage handles a ready client's desktop control message.
func (p *peer) onDesktopMessage(c proto.Control) {
	p.mu.Lock()
	v := p.viewer
	p.mu.Unlock()
	if v == nil {
		return
	}
	switch c.T {
	case proto.CtlAck:
		v.Ack(c.Frame)
		return
	case proto.CtlRefresh:
		v.Refresh()
		return
	case proto.CtlQuality:
		v.SetQuality(c.Quality)
		return
	}
	if p.role != proto.RoleControl {
		return
	}
	var err error
	switch c.T {
	case proto.CtlPointer:
		err = v.Pointer(c.X, c.Y, c.Buttons)
	case proto.CtlWheel:
		err = v.Wheel(c.DX, c.DY)
	case proto.CtlKey:
		err = v.Key(c.Key, c.Down)
	case proto.CtlText:
		err = v.Type(c.Text)
	case proto.CtlClipboard:
		err = p.receiveClipboard(v, c)
	case proto.CtlClipboardGet:
		err = p.sendHostClipboard(v)
	case proto.CtlSelectDisplay:
		err = p.selectDisplay(c.Display)
	}
	if err != nil {
		p.inputFailed(err)
	}
}

// receiveClipboard collects clipboard parts and puts the text on the host
// clipboard. Only its size is logged.
func (p *peer) receiveClipboard(v *desktop.Viewer, c proto.Control) error {
	if p.clip.Len()+len(c.Text) > maxClipboard {
		p.clip.Reset()
		return fmt.Errorf("clipboard text is over the %d byte limit", maxClipboard)
	}
	p.clip.WriteString(c.Text)
	if c.More {
		return nil
	}
	text := p.clip.String()
	p.clip.Reset()
	if err := v.SetClipboard(text); err != nil {
		return fmt.Errorf("set the clipboard: %w", err)
	}
	p.s.audit.Clipboard(p.auditClient(), audit.ClipboardToHost, len(text))
	p.s.logf("client #%d put %d bytes of text on this computer's clipboard", p.id, len(text))
	return nil
}

func (p *peer) sendHostClipboard(v *desktop.Viewer) error {
	text, err := v.Clipboard()
	if err != nil {
		return fmt.Errorf("read the clipboard: %w", err)
	}
	if len(text) > maxClipboard {
		return fmt.Errorf("the host clipboard is over the %d byte limit", maxClipboard)
	}
	p.sendClipboard(text)
	p.s.audit.Clipboard(p.auditClient(), audit.ClipboardToClient, len(text))
	p.s.logf("client #%d read the host clipboard (%d bytes)", p.id, len(text))
	return nil
}

func (p *peer) selectDisplay(i *int) error {
	if i == nil {
		return errors.New("no display given")
	}
	hub := p.s.desk.hub
	if err := hub.SelectDisplay(*i); err != nil {
		return err
	}
	ds, cur := hub.Displays()
	p.s.audit.DisplaySelected(p.auditClient(), ds[cur].Name)
	p.s.logf("client #%d switched the shared display to %s (%dx%d)", p.id, ds[cur].Name, ds[cur].W, ds[cur].H)
	return nil
}

// inputFailed tells the client (and the host log) why input is not
// working, at most every inputErrEvery, so a blocked desktop (lock screen,
// elevated window) does not flood either side.
func (p *peer) inputFailed(err error) {
	p.mu.Lock()
	if time.Since(p.inputErrAt) < inputErrEvery {
		p.mu.Unlock()
		return
	}
	p.inputErrAt = time.Now()
	p.mu.Unlock()
	p.s.logf("client #%d: remote input failed: %v", p.id, err)
	p.sendControl(proto.Control{T: proto.CtlInfo, Msg: "Remote input failed: " + err.Error()})
}
