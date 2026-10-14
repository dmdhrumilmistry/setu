package secure

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// jsVector was produced by web/js/device.mjs (WebCrypto in Node): the Go
// host must verify browser signatures byte for byte.
var jsVector = struct{ pub, sig, fp string }{
	pub: "040b0edec9c706b804cd653df98f2ef5d92de6ff324da599b90eff458e7cf0afec535c998d49c40e9a78472459b4cc1f6d2f51a015c231c3ea450dbdedb785cc3d",
	sig: "b43096e7461c887cf912dded95cec67ed637ed019c12e153ebe55e67939f21005a0fea1eddbee9a82724d2a07c05e21f24705da0d2d6d6a34b7bc1b219940ebc",
	fp:  "0c01-5969-1e72-3f7d-508f",
}

func vectorNonce() []byte {
	n := make([]byte, 32)
	for i := range n {
		n[i] = byte(i)
	}
	return n
}

func TestDeviceSignatureFromWebCrypto(t *testing.T) {
	pub, _ := hex.DecodeString(jsVector.pub)
	sig, _ := hex.DecodeString(jsVector.sig)
	fp, err := VerifyDevice(pub, sig, vectorNonce(), "sha-256 CC:DD", "sha-256 AA:BB") // order must not matter
	if err != nil {
		t.Fatal(err)
	}
	if fp != jsVector.fp {
		t.Fatalf("fingerprint %s, JS computed %s", fp, jsVector.fp)
	}
}

func TestDeviceSignRoundTripAndTamper(t *testing.T) {
	k, err := NewDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	nonce := RandomBytes(32)
	sig, err := k.Sign(nonce, "fp-a", "fp-b")
	if err != nil {
		t.Fatal(err)
	}
	fp, err := VerifyDevice(k.Public(), sig, nonce, "fp-a", "fp-b")
	if err != nil || fp != k.Fingerprint() {
		t.Fatalf("verify: %v %s", err, fp)
	}
	// A signature is bound to its connection: other nonce or fingerprints fail.
	if _, err := VerifyDevice(k.Public(), sig, RandomBytes(32), "fp-a", "fp-b"); err == nil {
		t.Error("signature replayed with another nonce")
	}
	if _, err := VerifyDevice(k.Public(), sig, nonce, "fp-a", "fp-x"); err == nil {
		t.Error("signature replayed on another connection")
	}
	other, _ := NewDeviceKey()
	if _, err := VerifyDevice(other.Public(), sig, nonce, "fp-a", "fp-b"); err == nil {
		t.Error("signature accepted for another key")
	}
	if _, err := VerifyDevice([]byte{4, 1, 2}, sig, nonce, "fp-a", "fp-b"); err == nil {
		t.Error("malformed key accepted")
	}
	if _, err := VerifyDevice(k.Public(), sig[:63], nonce, "fp-a", "fp-b"); err == nil {
		t.Error("short signature accepted")
	}
}

func TestDeviceKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "device.key")
	a, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateDeviceKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("key changed between loads")
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode %v", st.Mode().Perm())
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateDeviceKey(path); err == nil {
			t.Error("world-readable key file accepted")
		}
	}
}

func TestNormalizeDeviceFingerprint(t *testing.T) {
	for in, want := range map[string]string{
		"0C01-5969-1E72-3F7D-508F":   "0c01-5969-1e72-3f7d-508f",
		" 0c01 5969 1e72 3f7d 508f ": "0c01-5969-1e72-3f7d-508f",
		"0c0159691e723f7d508f":       "0c01-5969-1e72-3f7d-508f",
	} {
		if got, err := NormalizeDeviceFingerprint(in); err != nil || got != want {
			t.Errorf("Normalize(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0c01-5969", "zzzz-5969-1e72-3f7d-508f", "0c01-5969-1e72-3f7d-508f-00"} {
		if _, err := NormalizeDeviceFingerprint(bad); err == nil {
			t.Errorf("Normalize(%q) accepted", bad)
		}
	}
}
