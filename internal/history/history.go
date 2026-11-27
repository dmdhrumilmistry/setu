// Package history keeps a small local record of who connected to this
// computer's shares and when, for `setu dashboard`.
//
// It is metadata only, the same facts the service log already holds:
// connections, disconnections (with how long they lasted), failed logins,
// lockouts, rejected joins and pairings. Keystrokes, commands and terminal
// output are never part of it (the audit log is the place for those, and it
// lives off the machine).
//
// The file is append-only JSON lines, owner-only (0600) in the service
// directory, written by every process that hosts something unattended (the
// service and background shares). Each event is one write to a file opened
// with O_APPEND, so writers in several processes never interleave inside a
// line. It is capped: past maxBytes the file is renamed to history.jsonl.1
// (replacing the previous one), so at most about twice that is kept.
package history

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Event kinds.
const (
	SessionStart   = "session_start"   // a share started serving
	SessionEnd     = "session_end"     // a share stopped
	Connect        = "connect"         // a client passed every check
	Reconnect      = "reconnect"       // a client resumed after its connection dropped
	Disconnect     = "disconnect"      // a connected client left (Seconds: how long)
	AuthFailure    = "auth_failure"    // wrong password or pairing code
	Lockout        = "lockout"         // too many failures: new joins refused for a while
	Rejected       = "rejected"        // a join refused before it could log in
	Paired         = "paired"          // a new device proved a pairing code
	InviteDisabled = "invite_disabled" // the share stopped admitting new clients
	Dashboard      = "dashboard"       // a change made from the dashboard
)

// Event is one line of the history file.
type Event struct {
	Time time.Time `json:"t"`
	Kind string    `json:"kind"`
	// Source names what was shared: "profile:<name>" for a service
	// profile, "share:<id>" for a background share.
	Source    string  `json:"src,omitempty"`
	Session   string  `json:"session,omitempty"` // the host's session id
	Client    int     `json:"client,omitempty"`
	Name      string  `json:"name,omitempty"` // personal key, or which shared link
	Role      string  `json:"role,omitempty"`
	DeviceKey string  `json:"device_key,omitempty"`
	Device    string  `json:"device,omitempty"` // self-reported summary: for display only
	Address   string  `json:"addr,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Seconds   float64 `json:"secs,omitempty"`
}

const (
	// FileName is the history file in the service directory.
	FileName = "history.jsonl"
	maxBytes = 2 << 20
	// maxLine bounds what Read accepts per line; Append writes far less.
	maxLine  = 16 << 10
	maxField = 256
)

// Store appends to and reads one history file. Its methods are safe for
// concurrent use; other processes may append to the same file.
type Store struct {
	path string
	max  int64
	mu   sync.Mutex
}

// Open returns the store for the history file in dir. It touches nothing on
// disk until the first Append or Read.
func Open(dir string) *Store {
	return &Store{path: filepath.Join(dir, FileName), max: maxBytes}
}

// Path is the history file's location.
func (s *Store) Path() string { return s.path }

// Append records e. A nil Store records nothing. Fields are cleaned of
// control characters and truncated, so a line stays small and printable
// whatever a client reported about itself.
func (s *Store) Append(e Event) error {
	if s == nil {
		return nil
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Time = e.Time.UTC().Truncate(time.Millisecond)
	for _, f := range []*string{&e.Kind, &e.Source, &e.Session, &e.Name, &e.Role, &e.DeviceKey, &e.Device, &e.Address, &e.Reason} {
		*f = clean(*f)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(s.path); err == nil && st.Size()+int64(len(b)) > s.max {
		// Another process may rotate at the same moment; the worst case
		// is losing one generation early, never a corrupt line.
		_ = os.Rename(s.path, s.path+".1")
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := checkPrivate(f); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Read returns the events at or after since, oldest first. Lines that do
// not parse (a torn write after a crash, a newer format) are skipped. A
// history file other users can access is refused, as the service refuses
// its config.
func (s *Store) Read(since time.Time) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, p := range []string{s.path + ".1", s.path} {
		evs, err := readFile(p, since)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	return out, nil
}

func readFile(path string, since time.Time) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := checkPrivate(f); err != nil {
		return nil, err
	}
	var out []Event
	r := bufio.NewReaderSize(io.LimitReader(f, 4*maxBytes), maxLine)
	for {
		line, err := r.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			// An overlong line: skip to its end.
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			continue
		case err != nil && len(line) == 0:
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		var e Event
		if json.Unmarshal(line, &e) == nil && e.Kind != "" && !e.Time.Before(since) {
			out = append(out, e)
		}
		if err != nil {
			return out, nil // last line without a newline
		}
	}
}

// clean makes s printable and bounded.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s)
	if len(s) > maxField {
		s = s[:maxField]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Writer returns a func that queues events for Append on its own goroutine,
// so a slow disk never stalls a share (host.Config.Events must not block).
// When the queue is full, events are dropped. The goroutine writes what is
// queued and stops when ctx ends; done is closed once it has.
func (s *Store) Writer(ctx context.Context, logf func(string, ...any)) (write func(Event), done <-chan struct{}) {
	ch := make(chan Event, 256)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		var lastErr string
		put := func(e Event) {
			err := s.Append(e)
			switch {
			case err != nil && err.Error() != lastErr && logf != nil:
				logf("connection history: %v", err)
				lastErr = err.Error()
			case err == nil:
				lastErr = ""
			}
		}
		for {
			select {
			case e := <-ch:
				put(e)
			case <-ctx.Done():
				for {
					select {
					case e := <-ch:
						put(e)
					default:
						return
					}
				}
			}
		}
	}()
	return func(e Event) {
		select {
		case ch <- e:
		default:
		}
	}, fin
}
