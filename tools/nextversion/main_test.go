package main

import "testing"

func TestNext(t *testing.T) {
	code := []string{"internal/host/peer.go"}
	for _, c := range []struct {
		name, last string
		msgs, files []string
		bump, want  string
	}{
		{"first release", "", []string{"feat: share terminals"}, code, "auto", "v0.1.0"},
		{"fix is a patch", "v0.3.1", []string{"fix: reconnect"}, code, "auto", "v0.3.2"},
		{"scoped subject is a patch", "v0.3.1", []string{"upload: send files"}, code, "auto", "v0.3.2"},
		{"feat is a minor", "v1.2.3", []string{"fix: x", "feat(service): pairing"}, code, "auto", "v1.3.0"},
		{"breaking is a major", "v1.2.3", []string{"service!: drop passwords"}, code, "auto", "v2.0.0"},
		{"breaking footer", "v1.2.3", []string{"fix: x\n\nBREAKING CHANGE: new wire format"}, code, "auto", "v2.0.0"},
		{"breaking before v1 is a minor", "v0.4.0", []string{"proto!: v2"}, code, "auto", "v0.5.0"},
		{"docs only", "v1.0.0", []string{"docs: typo"}, []string{"docs/src/cli.md", "README.md"}, "auto", ""},
		{"tests only", "v1.0.0", []string{"test: more"}, []string{"internal/host/peer_test.go", "internal/e2e/x.go"}, "auto", ""},
		{"web only", "v1.0.0", []string{"web: scroll"}, []string{"web/js/app.mjs"}, "auto", ""},
		{"dependency bump", "v1.0.0", []string{"build(deps): bump pion"}, []string{"go.sum"}, "auto", "v1.0.1"},
		{"skip", "v1.0.0", []string{"fix: x [skip release]"}, code, "auto", ""},
		{"manual minor", "v1.0.0", nil, nil, "minor", "v1.1.0"},
		{"manual patch with no changes", "v1.0.0", nil, nil, "patch", "v1.0.1"},
	} {
		got, reason, err := next(c.last, c.msgs, c.files, c.bump)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q (%s), %v; want %q", c.name, got, reason, err, c.want)
		}
	}
	if _, _, err := next("release-1", []string{"fix: x"}, code, "auto"); err == nil {
		t.Error("a malformed last tag must be an error")
	}
}
