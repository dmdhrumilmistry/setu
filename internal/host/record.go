package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// recorder writes the terminal's output as an asciicast v2 recording
// (https://docs.asciinema.org/manual/asciicast/v2/), playable with
// `asciinema play` or the asciinema web player. Keystrokes are not
// recorded: they often include secrets, and the audit log covers commands.
//
// Writes happen on their own goroutine behind a bounded queue, so a slow
// disk can never stall the shared terminal: when the queue is full, output
// is left out of the recording and a marker says so.
type recorder struct {
	f     *os.File
	start time.Time
	ch    chan recEvent
	done  chan struct{}

	mu     sync.Mutex // output, resizes and close come from different goroutines
	closed bool
	carry  []byte // an incomplete UTF-8 sequence at the end of the last chunk
	drops  int    // events dropped since the last marker

	queued atomic.Int64 // bytes waiting in ch
	failed atomic.Bool  // a write failed: the recording stopped
	logf   func(format string, args ...any)
}

type recEvent struct {
	at   time.Duration
	kind string // "o" output, "r" resize, "m" marker
	data string
}

const (
	recQueue      = 4096
	recQueueBytes = 8 << 20 // bytes waiting to be written, at most
)

// newRecorder creates path (mode 0600, never replacing a file) and writes
// the asciicast header.
func newRecorder(path string, cols, rows int, title string, logf func(string, ...any)) (*recorder, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("recording: %w", err)
	}
	now := time.Now()
	hdr, _ := json.Marshal(map[string]any{
		"version": 2, "width": cols, "height": rows, "timestamp": now.Unix(), "title": title,
		"env": map[string]string{"TERM": "xterm-256color"},
	})
	if _, err := f.Write(append(hdr, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return nil, fmt.Errorf("recording: %w", err)
	}
	r := &recorder{f: f, start: now, ch: make(chan recEvent, recQueue), done: make(chan struct{}), logf: logf}
	go r.write()
	return r, nil
}

// recordingPath is a new file in dir named after the session.
func recordingPath(dir, prefix, id string, now time.Time) string {
	return filepath.Join(dir, fmt.Sprintf("%s-%s-%s.cast", prefix, now.Format("20060102-150405"), id))
}

func (r *recorder) write() {
	defer close(r.done)
	w := bufio.NewWriterSize(r.f, 64<<10)
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	for {
		select {
		case ev, ok := <-r.ch:
			if !ok {
				r.check(w.Flush())
				r.f.Close()
				return
			}
			r.queued.Add(-int64(len(ev.data)))
			if r.failed.Load() {
				continue
			}
			data, _ := json.Marshal(ev.data)
			_, err := fmt.Fprintf(w, "[%.6f, %q, %s]\n", ev.at.Seconds(), ev.kind, data)
			r.check(err)
		case <-flush.C:
			r.check(w.Flush()) // keep the file useful while the session runs
		}
	}
}

// check stops the recording at the first write error (a full disk, say),
// saying so once, rather than leaving a file that silently ends mid-line.
func (r *recorder) check(err error) {
	if err != nil && r.failed.CompareAndSwap(false, true) && r.logf != nil {
		r.logf("WARNING: recording stopped: %v", err)
	}
}

// emit queues an event; r.mu must be held.
func (r *recorder) emit(kind, data string) {
	if r.closed || r.failed.Load() {
		return
	}
	ev := recEvent{at: time.Since(r.start), kind: kind, data: data}
	if r.drops > 0 {
		marker := fmt.Sprintf("setu: %d output chunks left out (disk too slow)", r.drops)
		select {
		case r.ch <- recEvent{at: ev.at, kind: "m", data: marker}:
			r.queued.Add(int64(len(marker)))
			r.drops = 0
		default:
		}
	}
	if r.queued.Load()+int64(len(data)) > recQueueBytes {
		r.drops++
		return
	}
	select {
	case r.ch <- ev:
		r.queued.Add(int64(len(data)))
	default:
		r.drops++
	}
}

// output records terminal output. A UTF-8 sequence split across chunks is
// kept for the next one, so the recording stays valid text.
func (r *recorder) output(b []byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	b = append(r.carry, b...)
	r.carry = nil
	cut := len(b)
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				cut = i
			}
			break
		}
	}
	r.carry = append([]byte(nil), b[cut:]...)
	if cut > 0 {
		r.emit("o", string(b[:cut]))
	}
}

func (r *recorder) resize(cols, rows int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit("r", fmt.Sprintf("%dx%d", cols, rows))
}

// close writes what is queued and closes the file.
func (r *recorder) close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if len(r.carry) > 0 {
		r.emit("o", string(r.carry))
	}
	r.closed = true
	close(r.ch)
	r.mu.Unlock()
	<-r.done
}
