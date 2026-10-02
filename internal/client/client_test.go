package client

import "testing"

func TestPipedNewlines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello\n", "hello\r"},
		{"a\r\nb\n", "a\rb\r"},
		{"no newline", "no newline"},
		{"\r", "\r"},
	}
	for _, c := range cases {
		last := false
		if got := string(pipedNewlines([]byte(c.in), &last)); got != c.want {
			t.Errorf("pipedNewlines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// CRLF split across two reads must not produce two CRs.
	last := false
	got := string(pipedNewlines([]byte("x\r"), &last)) + string(pipedNewlines([]byte("\ny\n"), &last))
	if got != "x\ry\r" {
		t.Errorf("split CRLF: got %q", got)
	}
}
