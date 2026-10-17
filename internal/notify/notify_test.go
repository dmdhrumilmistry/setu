package notify

import "testing"

func TestClean(t *testing.T) {
	if got := clean("a\x1b[31mb\nc‮d", 64); got != "a [31mb c d" {
		t.Errorf("clean = %q", got)
	}
	if got := clean("abcdef", 4); got != "abc…" {
		t.Errorf("clean = %q", got)
	}
}
