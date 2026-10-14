package audit

import (
	"fmt"
	"regexp"
)

const redacted = "[REDACTED]"

// builtinRedactions catch the secrets people most often type or paste into
// a shell. A pattern's first group (if any) is kept before the marker and its
// second group after it, so "token=abc" becomes "token=[REDACTED]" and the
// record stays readable.
var builtinRedactions = []string{
	// setu invite secrets (k=<43 base64url chars>): a leaked link is a shell.
	`(\bk=)[A-Za-z0-9_-]{43}\b`,
	// key=value / key: value style credentials.
	// The name may carry a prefix (DB_PASSWORD, GITHUB_TOKEN) but must end
	// in the keyword, so "author: x" is left alone.
	`(?i)(\b[A-Za-z0-9_]*(?:password|passwd|pwd|passphrase|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?|auth)["']?\s*[=:]\s*["']?)[^\s"']+`,
	// --password value / --token=value style flags.
	`(?i)(--?(?:password|passwd|pass|secret|token|api-?key)(?:=|\s+))[^\s]+`,
	// HTTP Authorization values.
	`(?i)(\b(?:bearer|basic)\s+)[A-Za-z0-9._~+/=-]{8,}`,
	// user:password@host in URLs.
	`(://[^/\s:@]+:)[^/\s@]+(@)`,
	// Well-known token formats.
	`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`,
	`\bgh[pousr]_[A-Za-z0-9]{36,}\b`,
	`\bgithub_pat_[A-Za-z0-9_]{20,}\b`,
	`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`,
	`\bsk-(?:ant-)?[A-Za-z0-9_-]{20,}\b`,
	`\bAIza[0-9A-Za-z_-]{35}\b`,
	`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`,
	`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`,
}

type redactor struct {
	res []*regexp.Regexp
}

func newRedactor(extra []string) (*redactor, error) {
	r := &redactor{}
	for _, p := range append(append([]string(nil), builtinRedactions...), extra...) {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("audit redaction pattern %q: %w", p, err)
		}
		r.res = append(r.res, re)
	}
	return r, nil
}

// apply returns s with secrets replaced, and whether anything was replaced.
func (r *redactor) apply(s string) (string, bool) {
	changed := false
	for _, re := range r.res {
		out := re.ReplaceAllStringFunc(s, func(m string) string {
			changed = true
			sub := re.FindStringSubmatch(m)
			switch {
			case len(sub) > 2:
				return sub[1] + redacted + sub[2]
			case len(sub) > 1:
				return sub[1] + redacted
			}
			return redacted
		})
		s = out
	}
	return s, changed
}
