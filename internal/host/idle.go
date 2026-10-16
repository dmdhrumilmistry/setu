package host

import (
	"context"
	"fmt"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
)

// idleWarning is how long before an idle disconnect the client is warned.
const idleWarning = time.Minute

// touch records input from the client: keys, mouse, clipboard, uploads.
// Watching (and window resizes) does not count.
func (p *peer) touch() {
	p.lastInput.Store(time.Now().UnixNano())
	p.idleWarned.Store(false)
}

// idleLoop disconnects control clients that sent no input for
// cfg.IdleTimeout, warning them a minute before.
func (s *Session) idleLoop(ctx context.Context) {
	limit := s.cfg.IdleTimeout
	t := time.NewTicker(max(min(limit/4, 5*time.Second), 50*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, p := range s.readyPeers(true) {
				p.checkIdle(limit)
			}
		}
	}
}

func (p *peer) checkIdle(limit time.Duration) {
	idle := time.Since(time.Unix(0, p.lastInput.Load()))
	switch {
	case idle >= limit:
		if p.idleClosing.CompareAndSwap(false, true) {
			p.s.revokeTicket(p) // an idle client must join again, not resume
			p.sendControl(proto.Control{T: proto.CtlError, Msg: fmt.Sprintf("disconnected after %s without input", limit)})
			time.AfterFunc(200*time.Millisecond, func() { p.close("idle timeout") })
		}
	case limit > 2*idleWarning && idle >= limit-idleWarning && p.idleWarned.CompareAndSwap(false, true):
		p.sendControl(proto.Control{T: proto.CtlInfo, Msg: "No input for a while: you will be disconnected in a minute unless you type or move the mouse."})
	}
}
