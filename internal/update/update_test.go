package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.2.0", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.1", "v1.2.0", false},
		{"v1.2.0", "dev", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// release serves a fake release v9.9.9 whose "binary" is a script printing
// its version. tamper corrupts the binary after its checksum was taken.
func release(t *testing.T, key ed25519.PrivateKey, tamper bool) (*httptest.Server, Source) {
	t.Helper()
	src := Source{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	bin := []byte("#!/bin/sh\necho 'setu v9.9.9'\n")
	sum := sha256.Sum256(bin)
	sums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), src.Asset("v9.9.9")))
	if tamper {
		bin = append(bin, '#')
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/v9.9.9", http.StatusFound)
	})
	mux.HandleFunc("/download/v9.9.9/", func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "SHA256SUMS":
			w.Write(sums)
		case "SHA256SUMS.sig":
			if key == nil {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, sums))))
		case src.Asset("v9.9.9"):
			w.Write(bin)
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	src.BaseURL = srv.URL
	return srv, src
}

func TestApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release binary is a shell script")
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ctx := context.Background()
	exe := filepath.Join(t.TempDir(), "setu")
	os.WriteFile(exe, []byte("old"), 0o755)

	_, src := release(t, priv, false)
	src.Keys = []ed25519.PublicKey{pub}
	v, err := src.Target(ctx, "v1.0.0", "")
	if err != nil || v != "v9.9.9" {
		t.Fatalf("target %q, %v", v, err)
	}
	if v, _ := src.Target(ctx, "v9.9.9", ""); v != "" {
		t.Errorf("up to date, but target %q", v)
	}
	if v, _ := src.Target(ctx, "v9.9.9", "v9.0.0"); v != "v9.0.0" {
		t.Errorf("a pinned version must win, got %q", v)
	}
	if err := src.Apply(ctx, exe, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); !strings.Contains(string(b), "v9.9.9") {
		t.Error("binary not replaced")
	}

	_, bad := release(t, priv, true)
	bad.Keys = []ed25519.PublicKey{pub}
	if err := bad.Apply(ctx, exe, "v9.9.9"); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered binary: %v", err)
	}
	_, unsigned := release(t, nil, false)
	unsigned.Keys = []ed25519.PublicKey{pub}
	if err := unsigned.Apply(ctx, exe, "v9.9.9"); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("unsigned release with release keys configured: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	_, forged := release(t, priv, false)
	forged.Keys = []ed25519.PublicKey{other}
	if err := forged.Apply(ctx, exe, "v9.9.9"); err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("release signed by another key: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".setu-update-*")); len(m) != 0 {
		t.Errorf("failed updates left files behind: %v", m)
	}
}

func TestCheckBaseURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://mirror.example.com/setu": true, "http://127.0.0.1:8080": true,
		"http://mirror.example.com": false, "file:///tmp": false, "https://x/?a=b": false,
	} {
		if (CheckBaseURL(u) == nil) != ok {
			t.Errorf("CheckBaseURL(%s) ok=%v", u, !ok)
		}
	}
}

func TestReleaseKeysParse(t *testing.T) {
	k, err := Source{}.keys()
	if err != nil || len(k) == 0 {
		t.Fatalf("built-in release keys: %v (%d)", err, len(k))
	}
	if !(Source{}).Signed() {
		t.Error("builds with a release key must require signatures")
	}
}
