package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Filter is the subset of NIP-01 filters setu needs.
type Filter struct {
	Kinds []int    `json:"kinds,omitempty"`
	P     []string `json:"#p,omitempty"`
	Since int64    `json:"since,omitempty"`
}

// Handler receives verified, de-duplicated events.
type Handler func(Event)

type subscription struct {
	id      string
	filter  Filter
	handler Handler
}

// Pool fans publishes out to every relay and merges subscriptions from all of
// them. Relays reconnect automatically with backoff.
type Pool struct {
	ctx    context.Context
	cancel context.CancelFunc
	logf   func(string, ...any)
	relays []*relay

	mu   sync.Mutex
	subs map[string]*subscription
	seen map[string]time.Time

	connMu    sync.Mutex
	connCond  *sync.Cond
	connected int
}

// NewPool starts connecting to the given relay URLs. logf may be nil.
func NewPool(ctx context.Context, urls []string, logf func(string, ...any)) *Pool {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Pool{ctx: ctx, cancel: cancel, logf: logf, subs: map[string]*subscription{}, seen: map[string]time.Time{}}
	p.connCond = sync.NewCond(&p.connMu)
	for _, u := range urls {
		r := &relay{url: u, pool: p}
		p.relays = append(p.relays, r)
		go r.run()
	}
	go p.gcSeen()
	go func() {
		<-ctx.Done()
		p.connMu.Lock()
		p.connCond.Broadcast()
		p.connMu.Unlock()
	}()
	return p
}

// Close disconnects from every relay.
func (p *Pool) Close() { p.cancel() }

// WaitConnected blocks until at least n relays are connected or ctx ends.
func (p *Pool) WaitConnected(ctx context.Context, n int) error {
	stop := context.AfterFunc(ctx, func() {
		p.connMu.Lock()
		p.connCond.Broadcast()
		p.connMu.Unlock()
	})
	defer stop()
	p.connMu.Lock()
	defer p.connMu.Unlock()
	for p.connected < n {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("no nostr relay reachable: %w", err)
		}
		if err := p.ctx.Err(); err != nil {
			return err
		}
		p.connCond.Wait()
	}
	return nil
}

// Connected returns the number of currently connected relays.
func (p *Pool) Connected() int {
	p.connMu.Lock()
	defer p.connMu.Unlock()
	return p.connected
}

func (p *Pool) setConnected(delta int) {
	p.connMu.Lock()
	p.connected += delta
	p.connMu.Unlock()
	p.connCond.Broadcast()
}

// Publish sends the event to every connected relay and returns how many
// relays it was written to.
func (p *Pool) Publish(ev Event) int {
	msg, err := json.Marshal([]any{"EVENT", ev})
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range p.relays {
		if r.send(msg) == nil {
			n++
		}
	}
	return n
}

// Subscribe registers a subscription on all relays (now and on reconnect).
func (p *Pool) Subscribe(id string, f Filter, h Handler) {
	s := &subscription{id: id, filter: f, handler: h}
	p.mu.Lock()
	p.subs[id] = s
	p.mu.Unlock()
	for _, r := range p.relays {
		_ = r.sendReq(s)
	}
}

// Unsubscribe removes a subscription.
func (p *Pool) Unsubscribe(id string) {
	p.mu.Lock()
	delete(p.subs, id)
	p.mu.Unlock()
	msg, _ := json.Marshal([]any{"CLOSE", id})
	for _, r := range p.relays {
		_ = r.send(msg)
	}
}

func (p *Pool) deliver(subID string, ev Event) {
	// Verify before de-duplicating so a relay cannot shadow a genuine event
	// by first sending a forgery with the same id.
	if err := ev.Verify(); err != nil {
		return
	}
	p.mu.Lock()
	s := p.subs[subID]
	if s == nil {
		p.mu.Unlock()
		return
	}
	if _, dup := p.seen[ev.ID]; dup {
		p.mu.Unlock()
		return
	}
	p.seen[ev.ID] = time.Now()
	p.mu.Unlock()
	s.handler(ev)
}

func (p *Pool) gcSeen() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			cut := time.Now().Add(-10 * time.Minute)
			p.mu.Lock()
			for id, at := range p.seen {
				if at.Before(cut) {
					delete(p.seen, id)
				}
			}
			p.mu.Unlock()
		}
	}
}

type relay struct {
	url  string
	pool *Pool

	mu   sync.Mutex
	conn *websocket.Conn
}

const maxRelayMessage = 512 << 10

func (r *relay) run() {
	backoff := time.Second
	for r.pool.ctx.Err() == nil {
		start := time.Now()
		err := r.session()
		if r.pool.ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		r.pool.logf("relay %s: %v (retry in %s)", r.url, err, backoff)
		select {
		case <-r.pool.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (r *relay) session() error {
	d := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(r.pool.ctx, 15*time.Second)
	conn, _, err := d.DialContext(ctx, r.url, nil)
	cancel()
	if err != nil {
		return err
	}
	conn.SetReadLimit(maxRelayMessage)
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	r.pool.setConnected(1)
	defer func() {
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
		r.pool.setConnected(-1)
		conn.Close()
	}()

	stop := context.AfterFunc(r.pool.ctx, func() { conn.Close() })
	defer stop()

	r.pool.mu.Lock()
	subs := make([]*subscription, 0, len(r.pool.subs))
	for _, s := range r.pool.subs {
		subs = append(subs, s)
	}
	r.pool.mu.Unlock()
	for _, s := range subs {
		if err := r.sendReq(s); err != nil {
			return err
		}
	}

	// Keepalive: relays drop idle sockets.
	pingDone := make(chan struct{})
	defer close(pingDone)
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(90 * time.Second)) })
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				r.mu.Lock()
				_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				r.mu.Unlock()
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		r.handle(data)
	}
}

func (r *relay) handle(data []byte) {
	var msg []json.RawMessage
	if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
		return
	}
	var typ string
	if json.Unmarshal(msg[0], &typ) != nil {
		return
	}
	switch typ {
	case "EVENT":
		if len(msg) < 3 {
			return
		}
		var sub string
		var ev Event
		if json.Unmarshal(msg[1], &sub) != nil || json.Unmarshal(msg[2], &ev) != nil {
			return
		}
		r.pool.deliver(sub, ev)
	case "OK":
		if len(msg) >= 4 {
			var ok bool
			var reason string
			_ = json.Unmarshal(msg[2], &ok)
			_ = json.Unmarshal(msg[3], &reason)
			if !ok {
				r.pool.logf("relay %s rejected event: %s", r.url, reason)
			}
		}
	case "NOTICE", "CLOSED":
		var s string
		_ = json.Unmarshal(msg[len(msg)-1], &s)
		r.pool.logf("relay %s %s: %s", r.url, typ, s)
	}
}

func (r *relay) send(msg []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return fmt.Errorf("relay %s not connected", r.url)
	}
	_ = r.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return r.conn.WriteMessage(websocket.TextMessage, msg)
}

func (r *relay) sendReq(s *subscription) error {
	msg, err := json.Marshal([]any{"REQ", s.id, s.filter})
	if err != nil {
		return err
	}
	return r.send(msg)
}
