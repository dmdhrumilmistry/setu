package tty

import "testing"

func TestEscape(t *testing.T) {
	cases := []struct {
		in     string
		out    string
		detach bool
	}{
		{"hello", "hello", false},
		{"~.", "", true},
		{"ab~.", "ab~.", false},
		{"ab\r~.", "ab\r", true},
		{"\r~~x", "\r~x", false},
		{"\r~x", "\r~x", false},
	}
	for _, c := range cases {
		var e Escape
		out, d := e.Filter([]byte(c.in))
		if string(out) != c.out || d != c.detach {
			t.Errorf("Filter(%q) = %q,%v want %q,%v", c.in, out, d, c.out, c.detach)
		}
	}
	// split across calls
	var e Escape
	if out, d := e.Filter([]byte("x\r~")); string(out) != "x\r" || d {
		t.Fatalf("got %q %v", out, d)
	}
	if _, d := e.Filter([]byte(".")); !d {
		t.Fatal("expected detach across reads")
	}
}
