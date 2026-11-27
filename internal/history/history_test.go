package history

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAppendRead(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	now := time.Now()
	for i, k := range []string{Connect, Disconnect, AuthFailure} {
		if err := s.Append(Event{Time: now.Add(time.Duration(i) * time.Second), Kind: k, Source: "profile:a", Name: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	// An old event and a torn last line (a crash mid-write) are skipped.
	if err := s.Append(Event{Time: now.Add(-48 * time.Hour), Kind: Connect}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(s.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"t":"2026-01-01T00:00:00Z","kind":"conn`)
	f.Close()

	evs, err := s.Read(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Kind != Connect || evs[2].Kind != AuthFailure || evs[0].Name != "alice" {
		t.Fatalf("read %+v", evs)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(s.Path())
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("history file mode %v", st.Mode().Perm())
		}
	}
}

func TestCleanFields(t *testing.T) {
	s := Open(t.TempDir())
	long := strings.Repeat("é", 400)
	if err := s.Append(Event{Kind: Connect, Device: "evil\x1b[31m‮\nname", Reason: long}); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Read(time.Time{})
	if len(evs) != 1 {
		t.Fatalf("read %d events", len(evs))
	}
	if evs[0].Device != "evil[31mname" {
		t.Fatalf("control and bidi characters kept: %q", evs[0].Device)
	}
	if len(evs[0].Reason) > maxField || !strings.HasPrefix(long, evs[0].Reason) {
		t.Fatalf("field not truncated on a rune boundary: %d bytes", len(evs[0].Reason))
	}
}

func TestRotate(t *testing.T) {
	s := Open(t.TempDir())
	s.max = 600
	for range 20 {
		if err := s.Append(Event{Kind: Connect, Source: "profile:abc", Name: "someone"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{s.Path(), s.Path() + ".1"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > s.max {
			t.Fatalf("%s grew to %d bytes", filepath.Base(p), st.Size())
		}
	}
	evs, _ := s.Read(time.Time{})
	if len(evs) == 0 || len(evs) >= 20 {
		t.Fatalf("kept %d of 20 events; want the newest few", len(evs))
	}
}

func TestRefusesSharedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissions are ACLs on Windows")
	}
	s := Open(t.TempDir())
	if err := s.Append(Event{Kind: Connect}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(s.Path(), 0o644)
	if _, err := s.Read(time.Time{}); err == nil {
		t.Fatal("read a history file other users can access")
	}
	if err := s.Append(Event{Kind: Connect}); err == nil {
		t.Fatal("appended to a history file other users can access")
	}
}

func TestWriter(t *testing.T) {
	s := Open(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	write, done := s.Writer(ctx, t.Logf)
	for range 10 {
		write(Event{Kind: Connect})
	}
	cancel()
	<-done // everything queued is written before it stops
	evs, _ := s.Read(time.Time{})
	if len(evs) != 10 {
		t.Fatalf("wrote %d of 10 events", len(evs))
	}
	var nilStore *Store
	if err := nilStore.Append(Event{Kind: Connect}); err != nil {
		t.Fatal(err)
	}
}
