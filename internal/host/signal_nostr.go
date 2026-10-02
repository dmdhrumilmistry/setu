package host

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

const (
	maxClockSkew  = 2 * time.Minute
	helloInterval = 300 * time.Millisecond
)

type roomRole struct {
	room *secure.Room
	role string
}

type nostrSignaler struct {
	s     *Session
	pool  *nostr.Pool
	kp    *nostr.Keypair
	rooms map[string]roomRole

	mu        sync.Mutex
	pending   map[string]*pendingJoin // sid -> join
	seen      map[string]time.Time    // sid -> first seen (replay guard)
	lastHello time.Time
}

type pendingJoin struct {
	peer      *peer
	clientPub string
	room      roomRole
}

// serveNostr listens for encrypted join requests on public nostr relays.
func (s *Session) serveNostr(ctx context.Context) error {
	kp, err := nostr.GenerateKey()
	if err != nil {
		return err
	}
	ns := &nostrSignaler{s: s, kp: kp, rooms: map[string]roomRole{}, pending: map[string]*pendingJoin{}, seen: map[string]time.Time{}}
	add := func(sec *secure.Secret, role string) error {
		if sec == nil {
			return nil
		}
		r, err := secure.NewRoom(*sec)
		if err != nil {
			return err
		}
		ns.rooms[r.ID] = roomRole{room: r, role: role}
		return nil
	}
	if err := add(s.cfg.ControlSecret, proto.RoleControl); err != nil {
		return err
	}
	if err := add(s.cfg.ViewSecret, proto.RoleView); err != nil {
		return err
	}
	ids := make([]string, 0, len(ns.rooms))
	for id := range ns.rooms {
		ids = append(ids, id)
	}

	ns.pool = nostr.NewPool(ctx, s.cfg.Relays, s.debugf)
	defer ns.pool.Close()
	ns.pool.Subscribe("setu-"+secure.RandomID(4), nostr.Filter{
		Kinds: []int{nostr.KindSignal},
		P:     ids,
		Since: time.Now().Add(-maxClockSkew).Unix(),
	}, func(ev nostr.Event) { go ns.handle(ctx, ev) })

	wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err = ns.pool.WaitConnected(wctx, 1)
	cancel()
	if err != nil && ctx.Err() == nil {
		s.logf("WARNING: no nostr relay reachable yet (%v); still retrying in the background", err)
	} else if ctx.Err() == nil {
		s.debugf("connected to %d relay(s)", ns.pool.Connected())
	}

	gc := time.NewTicker(time.Minute)
	defer gc.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-gc.C:
			ns.mu.Lock()
			for sid, at := range ns.seen {
				if time.Since(at) > 2*maxClockSkew {
					delete(ns.seen, sid)
				}
			}
			ns.mu.Unlock()
		}
	}
}

func (ns *nostrSignaler) handle(ctx context.Context, ev nostr.Event) {
	rr, ok := ns.rooms[ev.TagValue("p")]
	if !ok || ev.Kind != nostr.KindSignal {
		return
	}
	pt, err := rr.room.Open(ev.Content, secure.SignalAAD(rr.room.ID, ev.PubKey, rr.room.ID))
	if err != nil {
		return // not for us / tampered: ignore silently
	}
	var m proto.Signal
	if json.Unmarshal(pt, &m) != nil || m.SID == "" || len(m.SID) > 64 {
		return
	}
	if d := time.Since(time.Unix(m.TS, 0)); d > maxClockSkew || d < -maxClockSkew {
		ns.s.debugf("dropping stale %s (clock skew %s)", m.Type, d)
		return
	}
	switch m.Type {
	case proto.SigHello:
		ns.onHello(ctx, ev, rr, m)
	case proto.SigAnswer:
		ns.onAnswer(ev, m)
	}
}

func (ns *nostrSignaler) onHello(ctx context.Context, ev nostr.Event, rr roomRole, m proto.Signal) {
	key := ev.PubKey + "/" + m.SID
	ns.mu.Lock()
	if _, dup := ns.seen[key]; dup {
		ns.mu.Unlock()
		return // retransmission or replay
	}
	if time.Since(ns.lastHello) < helloInterval {
		ns.mu.Unlock()
		return // rate limited; the client retransmits
	}
	ns.seen[key] = time.Now()
	ns.lastHello = time.Now()
	ns.mu.Unlock()

	p, err := ns.s.newPeer(rr.role, "nostr")
	if err != nil {
		ns.reply(ev.PubKey, rr, proto.Signal{Type: proto.SigReject, SID: m.SID, Reason: err.Error()})
		ns.s.logf("rejected a join request: %v", err)
		return
	}
	ns.s.debugf("client #%d: join request (role=%s)", p.id, rr.role)
	sdp, err := p.offer(ctx)
	if err != nil {
		p.close("offer failed: " + err.Error())
		return
	}
	ns.mu.Lock()
	ns.pending[m.SID] = &pendingJoin{peer: p, clientPub: ev.PubKey, room: rr}
	ns.mu.Unlock()
	p.addCloseHook(func() {
		ns.mu.Lock()
		delete(ns.pending, m.SID)
		ns.mu.Unlock()
	})
	ns.reply(ev.PubKey, rr, proto.Signal{Type: proto.SigOffer, SID: m.SID, SDP: sdp, ICE: ns.s.cfg.ICE})
}

func (ns *nostrSignaler) onAnswer(ev nostr.Event, m proto.Signal) {
	ns.mu.Lock()
	pj := ns.pending[m.SID]
	if pj == nil || pj.clientPub != ev.PubKey {
		ns.mu.Unlock()
		return
	}
	delete(ns.pending, m.SID)
	ns.mu.Unlock()
	if err := pj.peer.acceptAnswer(m.SDP); err != nil {
		pj.peer.close("bad answer: " + err.Error())
	}
}

func (ns *nostrSignaler) reply(to string, rr roomRole, m proto.Signal) {
	m.TS = time.Now().Unix()
	pt, err := json.Marshal(m)
	if err != nil {
		return
	}
	ct, err := rr.room.Seal(pt, secure.SignalAAD(rr.room.ID, ns.kp.Pub, to))
	if err != nil {
		return
	}
	ev := nostr.Event{Kind: nostr.KindSignal, Tags: [][]string{{"p", to}}, Content: ct}
	if err := ns.kp.Sign(&ev); err != nil {
		return
	}
	if n := ns.pool.Publish(ev); n == 0 {
		ns.s.logf("WARNING: could not publish %s: no relay connected", m.Type)
	}
}
