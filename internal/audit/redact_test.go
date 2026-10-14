package audit

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	r, err := newRedactor([]string{`internal-[0-9]{4}`})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ in, want string }{
		{"ls -la", "ls -la"},
		{"git commit --author: me", "git commit --author: me"},
		{"export API_KEY=abc123", "export API_KEY=[REDACTED]"},
		{"export GITHUB_TOKEN=abc123", "export GITHUB_TOKEN=[REDACTED]"},
		{"password: letmein", "password: [REDACTED]"},
		{"mysql --password=hunter2 db", "mysql --password=[REDACTED] db"},
		{"login --token s3cr3t", "login --token [REDACTED]"},
		{"curl -H 'Authorization: Bearer abcdefghijkl'", "curl -H 'Authorization: Bearer [REDACTED]'"},
		{"git clone https://bob:pa55@github.com/x/y", "git clone https://bob:[REDACTED]@github.com/x/y"},
		{"aws AKIAABCDEFGHIJKLMNOP", "aws [REDACTED]"},
		{"x sk-ant-" + strings.Repeat("x", 30), "x [REDACTED]"},
		{"setu join 'k=" + strings.Repeat("A", 43) + "'", "setu join 'k=[REDACTED]'"},
		{"echo internal-1234", "echo [REDACTED]"},
	}
	for _, tc := range cases {
		got, changed := r.apply(tc.in)
		if got != tc.want {
			t.Errorf("apply(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if changed != (tc.in != tc.want) {
			t.Errorf("apply(%q) changed=%v", tc.in, changed)
		}
	}
}

func TestRedactBadPattern(t *testing.T) {
	if _, err := newRedactor([]string{"("}); err == nil {
		t.Fatal("expected an error for an invalid pattern")
	}
}
