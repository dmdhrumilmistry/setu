package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
)

// TestRecord records a real shared session and checks the asciicast file
// holds what the client saw.
func TestRecord(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "recordings")
	hctx, stop := context.WithCancel(ctx)
	ctl, _ := startHost(t, hctx, r.URL(), func(c *host.Config) { c.RecordDir = dir; c.Profile = "agent" })

	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, c, "READY")
	_ = c.Write([]byte("recorded line\r"))
	waitFor(t, c, "got:recorded line")
	c.Close()
	stop() // end the share: the recording is complete
	var path string
	deadline := time.Now().Add(10 * time.Second)
	for path == "" && time.Now().Before(deadline) {
		files, _ := filepath.Glob(filepath.Join(dir, "agent-*.cast"))
		if len(files) == 1 {
			if b, _ := os.ReadFile(files[0]); strings.Contains(string(b), "got:recorded line") {
				path = files[0]
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if path == "" {
		t.Fatal("no complete recording")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	sc.Scan()
	var hdr map[string]any
	if json.Unmarshal(sc.Bytes(), &hdr) != nil || hdr["version"] != float64(2) {
		t.Fatalf("header %q", sc.Text())
	}
	var out strings.Builder
	for sc.Scan() {
		var ev []any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("event %q: %v", sc.Text(), err)
		}
		if ev[1] == "o" {
			out.WriteString(ev[2].(string))
		}
	}
	if !strings.Contains(out.String(), "READY") || !strings.Contains(out.String(), "got:recorded line") {
		t.Fatalf("recording lacks the session output: %q", out.String())
	}
}
