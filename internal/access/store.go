package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileName is the rules file: in the service directory for profiles, in a
// background share's own session directory for that share.
const FileName = "access.json"

// AllScope holds the rules that apply to every share the file serves (every
// profile, in the service directory; the share itself, in a session
// directory).
const AllScope = "*"

const fileVersion = 1

// maxRules keeps the file, and every check, small.
const maxRules = 512

// File is the rules file: rules by scope (AllScope or a profile name).
type File struct {
	Version int                `json:"version"`
	Scopes  map[string][]*Rule `json:"scopes"`
}

// Path is the rules file in dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// Load reads and validates a rules file; a missing one is empty. It fails
// on anything unexpected (unknown fields, an invalid rule, a file others
// can read or write), and the host then admits nobody: rules that cannot
// be read must not silently stop applying.
func Load(path string) (*File, error) {
	f := &File{Version: fileVersion, Scopes: map[string][]*Rule{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, f.Version)
	}
	if f.Scopes == nil {
		f.Scopes = map[string][]*Rule{}
	}
	n := 0
	for scope, rules := range f.Scopes {
		if scope != AllScope && !personRE.MatchString(scope) {
			return nil, fmt.Errorf("%s: scope %q is not a profile name", path, scope)
		}
		for _, r := range rules {
			if r == nil {
				return nil, fmt.Errorf("%s: empty rule in %s", path, scope)
			}
			if err := r.Normalize(); err != nil {
				return nil, fmt.Errorf("%s: %s: %w", path, scope, err)
			}
			n++
		}
	}
	if n > maxRules {
		return nil, fmt.Errorf("%s: %d rules; at most %d", path, n, maxRules)
	}
	return f, nil
}

// For returns the rules that apply to scope: its own and AllScope's.
func (f *File) For(scope string) []Rule {
	var out []Rule
	for _, s := range []string{AllScope, scope} {
		for _, r := range f.Scopes[s] {
			out = append(out, *r)
		}
		if scope == AllScope {
			break
		}
	}
	return out
}

// Add appends r to scope, refusing an exact duplicate.
func (f *File) Add(scope string, r Rule) error {
	if scope != AllScope && !personRE.MatchString(scope) {
		return fmt.Errorf("scope %q is not a profile name", scope)
	}
	n := 0
	for _, rs := range f.Scopes {
		n += len(rs)
	}
	if n >= maxRules {
		return fmt.Errorf("at most %d rules", maxRules)
	}
	for _, x := range f.Scopes[scope] {
		if x.Kind == r.Kind && x.Value == r.Value && x.Action == r.Action && x.Active(time.Now()) {
			return fmt.Errorf("that %s rule for %s already exists", r.Action, r.Describe())
		}
	}
	f.Scopes[scope] = append(f.Scopes[scope], &r)
	return nil
}

// Remove deletes the rule with id from scope.
func (f *File) Remove(scope, id string) error {
	rs := f.Scopes[scope]
	for i, r := range rs {
		if r.ID == id {
			f.Scopes[scope] = append(rs[:i], rs[i+1:]...)
			return nil
		}
	}
	return errors.New("no such rule")
}

// Prune drops rules that expired more than a day ago, so the file does not
// grow with temporary blocks.
func (f *File) Prune(now time.Time) {
	for scope, rs := range f.Scopes {
		kept := rs[:0]
		for _, r := range rs {
			if r.Expires.IsZero() || now.Sub(r.Expires) < 24*time.Hour {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(f.Scopes, scope)
		} else {
			f.Scopes[scope] = kept
		}
	}
}

// Update changes the rules file under a lock and writes it atomically.
func Update(path string, fn func(*File) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := lock(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	f, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	f.Prune(time.Now())
	for _, rs := range f.Scopes {
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Added.Before(rs[j].Added) })
	}
	f.Version = fileVersion
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

// Checker answers for one scope of a rules file, re-reading the file only
// when it changes. It is what a host calls on every login and, for
// connected clients, every few seconds.
type Checker struct {
	path, scope string

	mu    sync.Mutex
	stamp string
	rules []Rule
	err   error
}

// NewChecker checks the rules of scope in the file at path.
func NewChecker(path, scope string) *Checker { return &Checker{path: path, scope: scope} }

// Check decides whether s may join or stay. A rules file that cannot be
// read denies everyone (fail closed).
func (c *Checker) Check(s Subject) (bool, string) {
	rules, err := c.load()
	if err != nil {
		return false, "the access rules cannot be read: " + err.Error()
	}
	return Decide(rules, s, time.Now())
}

func (c *Checker) load() ([]Rule, error) {
	stamp := "missing"
	if st, err := os.Stat(c.path); err == nil {
		stamp = fmt.Sprintf("%d/%d/%v", st.Size(), st.ModTime().UnixNano(), st.Mode())
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if stamp != c.stamp || c.err != nil {
		f, err := Load(c.path)
		c.stamp, c.err = stamp, err
		c.rules = nil
		if err == nil {
			c.rules = f.For(c.scope)
		}
	}
	return c.rules, c.err
}

func lock(path string) (release func(), err error) {
	const stale = 30 * time.Second
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) > stale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked (remove it if no setu command is running)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writePrivate(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".access-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
