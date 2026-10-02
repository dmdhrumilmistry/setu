package host

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// serveManual performs one copy/paste offer/answer exchange. No network
// service other than STUN is involved.
func (s *Session) serveManual(ctx context.Context) error {
	if s.cfg.ReadAnswer == nil {
		return errors.New("manual mode needs an answer reader")
	}
	p, err := s.newPeer(proto.RoleControl, "manual")
	if err != nil {
		return err
	}
	sdp, err := p.offer(ctx)
	if err != nil {
		p.close("offer failed")
		return err
	}
	sid := secure.RandomID(8)
	code, err := link.EncodeCode(proto.Signal{Type: proto.SigOffer, SID: sid, SDP: sdp, ICE: s.cfg.ICE, TS: time.Now().Unix()})
	if err != nil {
		return err
	}
	if s.cfg.OnInvite != nil {
		s.cfg.OnInvite(Invite{Role: proto.RoleControl, Code: code})
	}
	for {
		ans, err := s.cfg.ReadAnswer(ctx)
		if err != nil {
			p.close("no answer")
			return err
		}
		var sig proto.Signal
		if err := link.DecodeCode(ans, &sig); err != nil {
			s.logf("could not decode answer code: %v — paste it again", err)
			continue
		}
		if sig.Type != proto.SigAnswer || sig.SID != sid {
			s.logf("that code is not an answer to this invite — paste it again")
			continue
		}
		if err := p.acceptAnswer(sig.SDP); err != nil {
			return fmt.Errorf("invalid answer: %w", err)
		}
		return nil
	}
}
