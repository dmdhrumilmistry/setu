package access

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustRule(t *testing.T, kind, value, action string, d time.Duration) Rule {
	t.Helper()
	r, err := New(kind, value, action, "", d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNormalize(t *testing.T) {
	for in, want := range map[[2]string]string{
		{KindDevice, "3F2A91C077DE0B4EA1C9"}: "3f2a-91c0-77de-0b4e-a1c9",
		{KindNetwork, "203.0.113.77/24"}:     "203.0.113.0/24",
		{KindNetwork, "198.51.100.4"}:        "198.51.100.4/32",
		{KindNetwork, "::ffff:198.51.100.4"}: "198.51.100.4/32",
		{KindNetwork, "2001:db8::1"}:         "2001:db8::1/128",
		{KindPerson, " alice "}:              "alice",
	} {
		r := mustRule(t, in[0], in[1], Block, 0)
		if r.Value != want {
			t.Fatalf("%v: %q, want %q", in, r.Value, want)
		}
	}
	for _, bad := range [][3]string{
		{KindDevice, "nope", Block}, {KindNetwork, "0.0.0.0/0", Block}, {KindNetwork, "example.com", Block},
		{KindPerson, "Alice Smith", Block}, {"user", "x", Block}, {KindPerson, "alice", "deny"},
	} {
		if _, err := New(bad[0], bad[1], bad[2], "", 0, time.Now()); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if _, err := New(KindPerson, "alice", Block, "a\x1b[2Jb", 0, time.Now()); err == nil {
		t.Fatal("control characters in a note accepted")
	}
}

func TestDecide(t *testing.T) {
	now := time.Now()
	dev := "3f2a-91c0-77de-0b4e-a1c9"
	alice := Subject{Person: "alice", DeviceKey: dev, Address: "198.51.100.4", Claimed: []string{"203.0.113.9"}}
	shared := Subject{Address: "192.0.2.10"}

	if ok, _ := Decide(nil, alice, now); !ok {
		t.Fatal("no rules refused")
	}
	// Block by any identifier; a network block also matches claimed addresses.
	for _, r := range []Rule{
		mustRule(t, KindDevice, dev, Block, 0),
		mustRule(t, KindPerson, "alice", Block, 0),
		mustRule(t, KindNetwork, "198.51.100.0/24", Block, 0),
		mustRule(t, KindNetwork, "203.0.113.9", Block, 0),
	} {
		if ok, why := Decide([]Rule{r}, alice, now); ok || !strings.HasPrefix(why, "blocked") {
			t.Fatalf("%s did not block: %v %q", r.Describe(), ok, why)
		}
		if ok, _ := Decide([]Rule{r}, shared, now); !ok {
			t.Fatalf("%s blocked an unrelated client", r.Describe())
		}
	}
	// An allow rule matches only the real address, never a claimed one.
	allowClaimed := mustRule(t, KindNetwork, "203.0.113.9", Allow, 0)
	if ok, why := Decide([]Rule{allowClaimed}, alice, now); ok || why != "not on the allow list" {
		t.Fatalf("an allow rule matched a self-reported address: %v %q", ok, why)
	}
	allowAlice := mustRule(t, KindPerson, "alice", Allow, 0)
	if ok, _ := Decide([]Rule{allowAlice}, alice, now); !ok {
		t.Fatal("allow list refused its match")
	}
	if ok, _ := Decide([]Rule{allowAlice}, shared, now); ok {
		t.Fatal("allow list admitted a shared-link client")
	}
	// Block wins over allow; expired rules do nothing.
	if ok, _ := Decide([]Rule{allowAlice, mustRule(t, KindDevice, dev, Block, 0)}, alice, now); ok {
		t.Fatal("allow beat block")
	}
	expired := mustRule(t, KindPerson, "alice", Block, time.Minute)
	if ok, _ := Decide([]Rule{expired}, alice, now.Add(2*time.Minute)); !ok {
		t.Fatal("an expired rule still blocks")
	}
	if ok, _ := Decide([]Rule{mustRule(t, KindPerson, "bob", Allow, time.Minute)}, alice, now.Add(2*time.Minute)); !ok {
		t.Fatal("an expired allow rule still restricts")
	}
}

func TestFileAndChecker(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	c := NewChecker(path, "claude")
	alice := Subject{Person: "alice"}
	if ok, _ := c.Check(alice); !ok {
		t.Fatal("missing file refused")
	}
	if err := Update(path, func(f *File) error { return f.Add(AllScope, mustRule(t, KindPerson, "alice", Block, 0)) }); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Check(alice); ok {
		t.Fatal("rule for every profile did not apply to claude")
	}
	if ok, _ := NewChecker(path, "other").Check(Subject{Person: "bob"}); !ok {
		t.Fatal("unrelated person refused")
	}
	r := mustRule(t, KindPerson, "alice", Block, 0)
	if err := Update(path, func(f *File) error { return f.Add(AllScope, r) }); err == nil {
		t.Fatal("duplicate rule accepted")
	}
	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	id := f.Scopes[AllScope][0].ID
	if err := Update(path, func(f *File) error { return f.Remove(AllScope, id) }); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Check(alice); !ok {
		t.Fatal("removed rule still applies (stale cache)")
	}

	// Expired temporary rules are pruned a day after they end.
	old := mustRule(t, KindPerson, "carol", Block, time.Minute)
	old.Expires = time.Now().Add(-48 * time.Hour)
	if err := Update(path, func(f *File) error { f.Scopes["claude"] = []*Rule{&old}; return nil }); err != nil {
		t.Fatal(err)
	}
	if f, _ := Load(path); len(f.Scopes["claude"]) != 0 {
		t.Fatal("long-expired rule kept")
	}

	// A file that cannot be trusted refuses everyone (fail closed).
	os.WriteFile(path, []byte(`{"version":1,"scopes":{"*":[{"id":"ab","kind":"person","value":"alice","action":"block","added":"2026-01-01T00:00:00Z","extra":1}]}}`), 0o600)
	if ok, why := c.Check(Subject{Person: "bob"}); ok || !strings.Contains(why, "cannot be read") {
		t.Fatalf("malformed rules file did not fail closed: %v %q", ok, why)
	}
	os.WriteFile(path, []byte(`{"version":1,"scopes":{"../x":[]}}`), 0o600)
	if _, err := Load(path); err == nil {
		t.Fatal("bad scope accepted")
	}
	if runtime.GOOS != "windows" {
		os.WriteFile(path, []byte(`{"version":1,"scopes":{}}`), 0o600)
		os.Chmod(path, 0o666)
		if _, err := Load(path); err == nil {
			t.Fatal("world-writable rules file accepted")
		}
	}
}
