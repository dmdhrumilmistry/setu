package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// readCast parses an asciicast v2 file into its header and events.
func readCast(t *testing.T, path string) (map[string]any, [][]any) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var hdr map[string]any
	var events [][]any
	for i := 0; sc.Scan(); i++ {
		if i == 0 {
			if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil {
				t.Fatalf("header %q: %v", sc.Text(), err)
			}
			continue
		}
		var ev []any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || len(ev) != 3 {
			t.Fatalf("event %q: %v", sc.Text(), err)
		}
		events = append(events, ev)
	}
	return hdr, events
}

func TestRecorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.cast")
	r, err := newRecorder(path, 100, 30, "bash", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	hi := []byte("héllo \U0001F44B\n")
	cut := strings.Index(string(hi), "\U0001F44B") + 2 // split the emoji between chunks
	r.output(hi[:cut])
	r.output(hi[cut:])
	r.resize(120, 40)
	r.output([]byte("\x1b[31mred\x1b[0m"))
	r.close()

	hdr, events := readCast(t, path)
	if hdr["version"] != float64(2) || hdr["width"] != float64(100) || hdr["height"] != float64(30) || hdr["title"] != "bash" {
		t.Fatalf("header %v", hdr)
	}
	var out strings.Builder
	var kinds []string
	last := -1.0
	for _, ev := range events {
		at := ev[0].(float64)
		if at < last {
			t.Errorf("time goes backwards: %v", events)
		}
		last = at
		kinds = append(kinds, ev[1].(string))
		if ev[1] == "o" {
			out.WriteString(ev[2].(string))
		}
		if ev[1] == "r" && ev[2] != "120x40" {
			t.Errorf("resize event %v", ev)
		}
	}
	if out.String() != string(hi)+"\x1b[31mred\x1b[0m" {
		t.Fatalf("recorded output %q", out.String())
	}
	if got := strings.Join(kinds, ""); strings.Trim(got, "or") != "" || !strings.Contains(got, "r") {
		t.Errorf("event kinds %v, want output and one resize", kinds)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}
	if _, err := newRecorder(path, 80, 24, "again", nil); err == nil {
		t.Error("an existing recording was replaced")
	}
	// Writing after close is ignored, not a panic.
	r.output([]byte("late"))
	r.resize(1, 1)
}

// TestRecorderStopsOnWriteError: a failing disk stops the recording with
// one warning instead of a silently truncated file.
func TestRecorderStopsOnWriteError(t *testing.T) {
	var warnings []string
	var mu sync.Mutex
	r, err := newRecorder(filepath.Join(t.TempDir(), "s.cast"), 80, 24, "sh", func(f string, a ...any) {
		mu.Lock()
		warnings = append(warnings, fmt.Sprintf(f, a...))
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	r.f.Close() // every later write fails
	for range 3000 {
		r.output([]byte(strings.Repeat("x", 100)))
	}
	r.close()
	mu.Lock()
	defer mu.Unlock()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "recording stopped") {
		t.Fatalf("warnings %q", warnings)
	}
}

func TestRecordingPath(t *testing.T) {
	at := time.Date(2026, 10, 16, 9, 30, 5, 0, time.UTC)
	if got := filepath.Base(recordingPath("/rec", "claude", "ab12cd34", at)); got != "claude-20261016-093005-ab12cd34.cast" {
		t.Fatalf("path %s", got)
	}
}
