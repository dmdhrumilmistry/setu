// Package access holds the block and allow lists of a share: rules that keep
// a device, a person or a network out (block) or let only matching clients
// in (allow).
//
// Rules sit on top of the normal login, never instead of it: an allowed
// client still needs the link and the password, pairing code or known device
// the share asks for. They are checked when a client logs in or resumes, and
// again while it is connected, so a new rule also disconnects a client it
// covers.
//
// What a rule can name, from strongest to weakest:
//
//   - device: a device key fingerprint, proven by a signature over the
//     connection, so it cannot be claimed by another device.
//   - person: a personal key name, which the host derives from which key
//     decrypted the join request, never from what the client says.
//   - network: an IP address or CIDR range. A block rule matches the address
//     the connection really comes from and also the public addresses the
//     client listed in its offer (it can leave those out, so it cannot use
//     them to dodge a rule on its real address). An allow rule matches only
//     the real address, which is a TURN server's when the connection is
//     relayed.
//
// Evaluation: an active block rule that matches denies; otherwise, if any
// allow rule is active, the client must match one of them; otherwise it is
// allowed. Expired rules are ignored.
package access

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// Rule kinds and actions.
const (
	KindDevice  = "device"
	KindPerson  = "person"
	KindNetwork = "network"

	Block = "block"
	Allow = "allow"
)

// Rule is one entry of a block or allow list.
type Rule struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Value   string    `json:"value"`
	Action  string    `json:"action"`
	Note    string    `json:"note,omitempty"`
	Added   time.Time `json:"added"`
	Expires time.Time `json:"expires,omitzero"` // zero: never
}

// Subject is what the host knows about a client when it checks the rules.
type Subject struct {
	Person    string // personal key name; "" for a shared link
	DeviceKey string // verified device key fingerprint; "" if none
	Address   string // the remote address of the connection
	Claimed   []string
}

var personRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

const maxNote = 200

// New builds a validated rule with a fresh id. A zero d never expires.
func New(kind, value, action, note string, d time.Duration, now time.Time) (Rule, error) {
	r := Rule{Kind: kind, Value: value, Action: action, Note: note, Added: now.UTC().Truncate(time.Second)}
	if d < 0 {
		return Rule{}, errors.New("a rule cannot expire in the past")
	}
	if d > 0 {
		r.Expires = now.Add(d).UTC().Truncate(time.Second)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return Rule{}, err
	}
	r.ID = hex.EncodeToString(b)
	if err := r.Normalize(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// Normalize checks the rule and puts its value in canonical form.
func (r *Rule) Normalize() error {
	if len(r.ID) == 0 || len(r.ID) > 32 || strings.Trim(r.ID, "0123456789abcdef") != "" {
		return fmt.Errorf("rule id %q: want hex", r.ID)
	}
	switch r.Action {
	case Block, Allow:
	default:
		return fmt.Errorf("rule action %q: want %q or %q", r.Action, Block, Allow)
	}
	v := strings.TrimSpace(r.Value)
	switch r.Kind {
	case KindDevice:
		fp, err := secure.NormalizeDeviceFingerprint(v)
		if err != nil {
			return err
		}
		v = fp
	case KindPerson:
		if !personRE.MatchString(v) {
			return fmt.Errorf("person %q: use the personal key's name (1-32 lowercase letters, digits, '-' or '_')", v)
		}
	case KindNetwork:
		p, err := parseNetwork(v)
		if err != nil {
			return err
		}
		v = p.String()
	default:
		return fmt.Errorf("rule kind %q: want device, person or network", r.Kind)
	}
	r.Value = v
	if strings.IndexFunc(r.Note, func(c rune) bool { return unicode.IsControl(c) || unicode.Is(unicode.Bidi_Control, c) }) >= 0 {
		return errors.New("a note cannot hold control characters")
	}
	if len(r.Note) > maxNote {
		return fmt.Errorf("a note is at most %d bytes", maxNote)
	}
	return nil
}

func parseNetwork(v string) (netip.Prefix, error) {
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("network %q: want an IP address or a CIDR range like 203.0.113.0/24", v)
		}
		p = p.Masked()
		if p.Bits() == 0 {
			return netip.Prefix{}, fmt.Errorf("network %q covers every address; disable the profile instead", v)
		}
		return p, nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("network %q: want an IP address or a CIDR range like 203.0.113.0/24", v)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Active reports whether the rule applies at now.
func (r Rule) Active(now time.Time) bool { return r.Expires.IsZero() || now.Before(r.Expires) }

// Matches reports whether the rule names s.
func (r Rule) Matches(s Subject) bool {
	switch r.Kind {
	case KindDevice:
		return s.DeviceKey != "" && s.DeviceKey == r.Value
	case KindPerson:
		return s.Person != "" && s.Person == r.Value
	case KindNetwork:
		p, err := netip.ParsePrefix(r.Value)
		if err != nil {
			return false
		}
		addrs := []string{s.Address}
		if r.Action == Block {
			addrs = append(addrs, s.Claimed...)
		}
		for _, a := range addrs {
			if ip, err := netip.ParseAddr(a); err == nil && p.Contains(ip.Unmap()) {
				return true
			}
		}
	}
	return false
}

// Describe names the rule for people: "device 3f2a-…", "network 10.0.0.0/8".
func (r Rule) Describe() string { return r.Kind + " " + r.Value }

// Decide applies rules to s: a matching block rule denies, then, if any
// allow rule is active, s must match one. reason says why s was denied.
func Decide(rules []Rule, s Subject, now time.Time) (ok bool, reason string) {
	allows, allowed := 0, false
	for _, r := range rules {
		if !r.Active(now) {
			continue
		}
		switch r.Action {
		case Block:
			if r.Matches(s) {
				return false, "blocked: " + r.Describe()
			}
		case Allow:
			allows++
			allowed = allowed || r.Matches(s)
		}
	}
	if allows > 0 && !allowed {
		return false, "not on the allow list"
	}
	return true, ""
}
