// Package update installs setu releases in place.
//
// A release is a set of per-platform binaries plus SHA256SUMS (and, when the
// release workflow has a signing key, SHA256SUMS.sig: an ed25519 signature).
// Fetch downloads the binary for this platform next to the running one,
// checks it against SHA256SUMS (and the signature, which is required as soon
// as this build knows a release key), and runs it once to make sure it starts
// and reports the expected version. Install then swaps it in with a rename,
// so the file is never half written and programs already running from the
// old binary (background shares, the service until it restarts) keep going
// untouched.
//
// Releases come from GitHub, or from a mirror with the same layout:
//
//	<base>/latest                   a redirect to .../tag/<version>, or a text file holding <version>
//	<base>/download/<version>/<file>
package update

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is where official releases are published.
const DefaultBaseURL = "https://github.com/dmdhrumilmistry/setu/releases"

// ReleaseKeys are the base64 ed25519 public keys allowed to sign releases
// (SHA256SUMS.sig). When any is set, unsigned releases are refused; with none,
// a release is trusted on its checksums fetched over https.
var ReleaseKeys = []string{
	"4n/cWcdrCUpMYyqHsyEICBUzL+PkN2qWplBXIQPMnMo=", // SETU_RELEASE_SIGNING_KEY, created 2026-10-17
}

const (
	maxBinary = 256 << 20
	maxSmall  = 64 << 10
)

var versionRE = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// Valid reports whether v is a release version such as v1.4.0.
func Valid(v string) bool { return versionRE.MatchString(v) }

// Newer reports whether release version a is newer than b. Pre-releases
// (v1.4.0-rc.1) sort before the release.
func Newer(a, b string) bool {
	ma, mb := versionRE.FindStringSubmatch(a), versionRE.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x > y
		}
	}
	switch {
	case ma[4] == mb[4]:
		return false
	case ma[4] == "":
		return true
	case mb[4] == "":
		return false
	}
	return ma[4] > mb[4]
}

// Source is where releases come from.
type Source struct {
	BaseURL string              // DefaultBaseURL if empty
	Keys    []ed25519.PublicKey // nil: ReleaseKeys
	Client  *http.Client        // nil: a client with timeouts
	GOOS    string              // tests; runtime.GOOS if empty
	GOARCH  string
}

func (s Source) base() string {
	if s.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(s.BaseURL, "/")
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (s Source) keys() ([]ed25519.PublicKey, error) {
	if s.Keys != nil {
		return s.Keys, nil
	}
	var out []ed25519.PublicKey
	for _, k := range ReleaseKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil || len(b) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("bad built-in release key %q", k)
		}
		out = append(out, ed25519.PublicKey(b))
	}
	return out, nil
}

// Signed reports whether releases from s must carry a valid signature.
func (s Source) Signed() bool {
	k, err := s.keys()
	return err != nil || len(k) > 0
}

// CheckBaseURL accepts https URLs (and http on loopback, for testing).
func CheckBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return fmt.Errorf("update URL %q: want an https:// base URL", raw)
	}
	return nil
}

// Latest returns the newest release version.
func (s Source) Latest(ctx context.Context) (string, error) {
	if err := CheckBaseURL(s.base()); err != nil {
		return "", err
	}
	c := *s.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()
	var v string
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		v = path.Base(resp.Header.Get("Location")) // .../releases/tag/v1.4.0
	case resp.StatusCode == http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		v = strings.TrimSpace(string(b))
	default:
		return "", fmt.Errorf("checking for updates: %s from %s/latest", resp.Status, s.base())
	}
	if !Valid(v) {
		return "", fmt.Errorf("checking for updates: no release found at %s (got %q)", s.base(), v)
	}
	return v, nil
}

// Asset is the release file name for this platform.
func (s Source) Asset(version string) string {
	goos, goarch := s.GOOS, s.GOARCH
	if goos == "" {
		goos, goarch = runtime.GOOS, runtime.GOARCH
	}
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("setu-%s-%s-%s%s", version, goos, goarch, ext)
}

func (s Source) get(ctx context.Context, version, file string, w io.Writer, limit int64) error {
	u := s.base() + "/download/" + url.PathEscape(version) + "/" + url.PathEscape(file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("%s: larger than %d bytes", u, limit)
	}
	return err
}

// Fetch downloads and verifies version's binary into dir (which should be
// the installed binary's directory, so Install can rename it into place)
// and returns its path. The caller removes it if it does not install it.
func (s Source) Fetch(ctx context.Context, version, dir string) (string, error) {
	if !Valid(version) {
		return "", fmt.Errorf("%q is not a release version (like v1.4.0)", version)
	}
	if err := CheckBaseURL(s.base()); err != nil {
		return "", err
	}
	var sums bytes.Buffer
	if err := s.get(ctx, version, "SHA256SUMS", &sums, maxSmall); err != nil {
		return "", fmt.Errorf("release %s: %w", version, err)
	}
	keys, err := s.keys()
	if err != nil {
		return "", err
	}
	if len(keys) > 0 {
		var sig bytes.Buffer
		if err := s.get(ctx, version, "SHA256SUMS.sig", &sig, maxSmall); err != nil {
			return "", fmt.Errorf("release %s is not signed: %w", version, err)
		}
		if err := verify(keys, sums.Bytes(), sig.Bytes()); err != nil {
			return "", fmt.Errorf("release %s: %w", version, err)
		}
	}
	asset := s.Asset(version)
	want, err := checksum(sums.Bytes(), asset)
	if err != nil {
		return "", fmt.Errorf("release %s: %w", version, err)
	}
	f, err := os.CreateTemp(dir, ".setu-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to the installed setu (%s): %w", dir, err)
	}
	h := sha256.New()
	err = s.get(ctx, version, asset, io.MultiWriter(f, h), maxBinary)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !bytes.Equal(h.Sum(nil), want) {
		err = fmt.Errorf("%s does not match its checksum", asset)
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("release %s: %w", version, err)
	}
	return f.Name(), nil
}

// VerifySums checks a SHA256SUMS signature against this build's release
// keys (the release workflow uses it before publishing).
func VerifySums(sums, sig []byte) error {
	keys, err := Source{}.keys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return errors.New("this build has no release keys")
	}
	return verify(keys, sums, sig)
}

// verify checks an ed25519 signature (raw or base64) of sums.
func verify(keys []ed25519.PublicKey, sums, sig []byte) error {
	if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig))); err == nil {
		sig = b
	}
	for _, k := range keys {
		if ed25519.Verify(k, sums, sig) {
			return nil
		}
	}
	return errors.New("SHA256SUMS signature does not verify against any release key")
}

// checksum finds file's SHA-256 in a SHA256SUMS listing.
func checksum(sums []byte, file string) ([]byte, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			return hex.DecodeString(f[0])
		}
	}
	return nil, fmt.Errorf("no checksum for %s (is there a build for this platform?)", file)
}

// SelfTest runs a downloaded binary's `version` command and checks that it
// starts on this machine and is the expected release.
func SelfTest(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the new setu does not start here: %v: %s", err, bytes.TrimSpace(out))
	}
	if !bytes.Contains(out, []byte(version)) {
		return fmt.Errorf("the new binary reports %q, not %s", bytes.TrimSpace(out), version)
	}
	return nil
}

// Install puts the verified binary at newBin in place of exe. On Windows a
// running executable cannot be replaced but can be renamed, so the old one
// is moved aside to exe.old (removed by Cleanup once it is no longer used).
func Install(newBin, exe string) error {
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(exe); err == nil {
			_ = os.Chmod(newBin, st.Mode().Perm()|0o100)
		}
		return os.Rename(newBin, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(newBin, exe); err != nil {
		_ = os.Rename(old, exe) // put the working one back
		return err
	}
	return nil
}

// Cleanup removes what an earlier Install left behind (Windows exe.old,
// abandoned downloads), ignoring files still in use.
func Cleanup(exe string) {
	_ = os.Remove(exe + ".old")
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".setu-update-*")); len(m) > 0 {
		for _, f := range m {
			if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > time.Hour {
				_ = os.Remove(f)
			}
		}
	}
}

// Executable is the path of the running setu binary, with symlinks
// resolved. Binaries built by `go run` cannot be updated.
func Executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, "go-build") {
		return "", errors.New("this setu was started with `go run`; there is nothing to update")
	}
	return exe, nil
}

// Stamp identifies the file at path, to notice when it was replaced.
func Stamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", st.Size(), st.ModTime().UnixNano())
}

// Target decides which version to install: pinned if set (an
// administrator's choice, which may be older), else the latest release when
// it is newer than current. "" means nothing to do.
func (s Source) Target(ctx context.Context, current, pinned string) (string, error) {
	if pinned != "" {
		if pinned == current {
			return "", nil
		}
		return pinned, nil
	}
	latest, err := s.Latest(ctx)
	if err != nil {
		return "", err
	}
	if !Valid(current) || Newer(latest, current) {
		return latest, nil
	}
	return "", nil
}

// Apply downloads, verifies, tests and installs version over exe.
func (s Source) Apply(ctx context.Context, exe, version string) error {
	Cleanup(exe)
	bin, err := s.Fetch(ctx, version, filepath.Dir(exe))
	if err != nil {
		return err
	}
	if err := SelfTest(ctx, bin, version); err != nil {
		os.Remove(bin)
		return err
	}
	if err := Install(bin, exe); err != nil {
		os.Remove(bin)
		return fmt.Errorf("replacing %s: %w", exe, err)
	}
	return nil
}
