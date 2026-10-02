// Package relaytest is a minimal in-memory nostr relay for tests. It only
// forwards events to live subscribers (ephemeral semantics).
package relaytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/gorilla/websocket"
)

// Relay is a running test relay.
type Relay struct {
	*httptest.Server
	mu    sync.Mutex
	conns map[*client]bool
	// Events counts events accepted.
	Events int
}

type client struct {
	mu   sync.Mutex
	ws   *websocket.Conn
	subs map[string]nostr.Filter
}

// URL returns the ws:// URL of the relay.
func (r *Relay) URL() string { return "ws" + strings.TrimPrefix(r.Server.URL, "http") }

// New starts a plain ws:// relay.
func New() *Relay { return start(false) }

// NewTLS starts a wss:// relay with a self-signed certificate (see
// Server.Certificate()).
func NewTLS() *Relay { return start(true) }

func start(useTLS bool) *Relay {
	r := &Relay{conns: map[*client]bool{}}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := up.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		c := &client{ws: ws, subs: map[string]nostr.Filter{}}
		r.mu.Lock()
		r.conns[c] = true
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			delete(r.conns, c)
			r.mu.Unlock()
			ws.Close()
		}()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			r.handle(c, data)
		}
	})
	if useTLS {
		r.Server = httptest.NewTLSServer(h)
	} else {
		r.Server = httptest.NewServer(h)
	}
	return r
}

func (c *client) write(v any) {
	b, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.ws.WriteMessage(websocket.TextMessage, b)
}

func matches(f nostr.Filter, ev nostr.Event) bool {
	if len(f.Kinds) > 0 {
		ok := false
		for _, k := range f.Kinds {
			ok = ok || k == ev.Kind
		}
		if !ok {
			return false
		}
	}
	if len(f.P) > 0 {
		ok := false
		for _, t := range ev.Tags {
			if len(t) >= 2 && t[0] == "p" {
				for _, p := range f.P {
					ok = ok || p == t[1]
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (r *Relay) handle(c *client, data []byte) {
	var msg []json.RawMessage
	if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
		return
	}
	var typ string
	_ = json.Unmarshal(msg[0], &typ)
	switch typ {
	case "EVENT":
		var ev nostr.Event
		if json.Unmarshal(msg[1], &ev) != nil {
			return
		}
		if err := ev.Verify(); err != nil {
			c.write([]any{"OK", ev.ID, false, "invalid: " + err.Error()})
			return
		}
		c.write([]any{"OK", ev.ID, true, ""})
		r.mu.Lock()
		r.Events++
		conns := make([]*client, 0, len(r.conns))
		for cc := range r.conns {
			conns = append(conns, cc)
		}
		r.mu.Unlock()
		for _, cc := range conns {
			cc.mu.Lock()
			var hits []string
			for id, f := range cc.subs {
				if matches(f, ev) {
					hits = append(hits, id)
				}
			}
			cc.mu.Unlock()
			for _, id := range hits {
				cc.write([]any{"EVENT", id, ev})
			}
		}
	case "REQ":
		if len(msg) < 3 {
			return
		}
		var id string
		var f nostr.Filter
		_ = json.Unmarshal(msg[1], &id)
		_ = json.Unmarshal(msg[2], &f)
		c.mu.Lock()
		c.subs[id] = f
		c.mu.Unlock()
		c.write([]any{"EOSE", id})
	case "CLOSE":
		var id string
		_ = json.Unmarshal(msg[1], &id)
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}
