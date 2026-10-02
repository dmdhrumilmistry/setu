package secure

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func fixedSecret() Secret {
	var s Secret
	for i := range s {
		s[i] = byte(i)
	}
	return s
}

func TestSecretRoundTrip(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseSecret(s.String())
	if err != nil || p != s {
		t.Fatalf("round trip failed: %v", err)
	}
	if _, err := ParseSecret("short"); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestSealOpen(t *testing.T) {
	r, err := NewRoom(fixedSecret())
	if err != nil {
		t.Fatal(err)
	}
	aad := SignalAAD(r.ID, "a", "b")
	ct, err := r.Seal([]byte("hi"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := r.Open(ct, aad)
	if err != nil || string(pt) != "hi" {
		t.Fatalf("open: %v %q", err, pt)
	}
	if _, err := r.Open(ct, SignalAAD(r.ID, "x", "b")); err == nil {
		t.Fatal("opened with wrong AAD")
	}
	other, _ := NewSecret()
	r2, _ := NewRoom(other)
	if _, err := r2.Open(ct, aad); err == nil {
		t.Fatal("opened with wrong key")
	}
}

// Test vectors shared with web/test/crypto.test.mjs so the JS client stays
// byte-compatible with the Go implementation.
const (
	vectorRoomID = "477d2abd3cbaf16a91c3190076bace99c6e39dd51da5e746692f82844b2b4a10"
	vectorSAS    = "774 610"
	vectorProof  = "b6660ec4b55b56c57d702ee56424657a7a82c8ad458c49a11cfa3dbc07a73a44"
	vectorFPA    = "sha-256 AA:BB:CC"
	vectorFPB    = "sha-256 11:22:33"
)

func TestVectors(t *testing.T) {
	r, _ := NewRoom(fixedSecret())
	key, err := PasswordKey("correct horse", []byte("saltsaltsaltsalt"), MinPBKDF2Iterations)
	if err != nil {
		t.Fatal(err)
	}
	proof := hex.EncodeToString(AuthProof(key, bytes.Repeat([]byte{7}, 32), vectorFPA, vectorFPB))
	sas := SAS(vectorFPA, vectorFPB)
	t.Logf("room=%s sas=%s proof=%s", r.ID, sas, proof)
	if r.ID != vectorRoomID || sas != vectorSAS || proof != vectorProof {
		t.Fatalf("vector mismatch: room=%s sas=%s proof=%s", r.ID, sas, proof)
	}
	if SAS(vectorFPB, vectorFPA) != sas {
		t.Fatal("SAS not symmetric")
	}
	if hex.EncodeToString(AuthProof(key, bytes.Repeat([]byte{7}, 32), vectorFPB, vectorFPA)) != proof {
		t.Fatal("proof not symmetric")
	}
}

func TestFingerprint(t *testing.T) {
	sdp := "v=0\r\na=group:BUNDLE 0\r\na=fingerprint:SHA-256 ab:cd:ef\r\n"
	fp, err := Fingerprint(sdp)
	if err != nil || fp != "sha-256 AB:CD:EF" {
		t.Fatalf("got %q %v", fp, err)
	}
	if _, err := Fingerprint("v=0\r\n"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPasswordKeyMinIterations(t *testing.T) {
	if _, err := PasswordKey("x", []byte("s"), 10); err == nil {
		t.Fatal("weak iteration count accepted")
	}
}
