package secure

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
)

// A device key is a client's long-lived ECDSA P-256 key. The client proves
// it holds the key by signing a transcript bound to the current connection
// (the host's nonce and both DTLS fingerprints), so a signature cannot be
// replayed on another connection. The host identifies the device by the
// key's fingerprint, which is what --trust-device and the "always approve"
// answer remember.
//
// Formats match WebCrypto, so browsers produce them natively: the public
// key is the 65-byte uncompressed point and signatures are IEEE P1363 (r||s,
// 64 bytes).

const deviceTranscriptLabel = "setu/v1/device"

// DeviceKey is a client's signing key.
type DeviceKey struct {
	priv *ecdsa.PrivateKey
}

// NewDeviceKey generates a key that lives only in memory.
func NewDeviceKey() (*DeviceKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &DeviceKey{priv: k}, nil
}

// LoadOrCreateDeviceKey reads the key at path, creating it (mode 0600) if
// it does not exist yet. A key file other users can read is refused.
func LoadOrCreateDeviceKey(path string) (*DeviceKey, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		k, err := NewDeviceKey()
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(k.priv)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
			f.Close()
			return nil, err
		}
		return k, f.Close()
	}
	if err != nil {
		return nil, err
	}
	if err := checkKeyFileMode(path); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a PEM private key", path)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s: not a P-256 key", path)
	}
	return &DeviceKey{priv: ec}, nil
}

// Public returns the 65-byte uncompressed public key.
func (k *DeviceKey) Public() []byte {
	b, _ := k.priv.PublicKey.Bytes()
	return b
}

// Fingerprint identifies the key, e.g. "3f2a-91c0-77de-0b4e-a1c9".
func (k *DeviceKey) Fingerprint() string { return DeviceFingerprint(k.Public()) }

// DeviceTranscript is the message a device signs for one connection.
func DeviceTranscript(nonce []byte, fpA, fpB string) []byte {
	a, b := sortedPair(fpA, fpB)
	return []byte(deviceTranscriptLabel + "\n" + base64.StdEncoding.EncodeToString(nonce) + "\n" + a + "\n" + b)
}

// Sign signs the transcript of the current connection (P1363, 64 bytes).
func (k *DeviceKey) Sign(nonce []byte, fpA, fpB string) ([]byte, error) {
	h := sha256.Sum256(DeviceTranscript(nonce, fpA, fpB))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, h[:])
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig, nil
}

// VerifyDevice checks a device's signature over this connection's
// transcript and returns the device fingerprint.
func VerifyDevice(pub, sig, nonce []byte, fpA, fpB string) (string, error) {
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		return "", fmt.Errorf("device key: %w", err)
	}
	if len(sig) != 64 {
		return "", errors.New("device signature: want 64 bytes")
	}
	h := sha256.Sum256(DeviceTranscript(nonce, fpA, fpB))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, h[:], r, s) {
		return "", errors.New("device signature does not verify")
	}
	return DeviceFingerprint(pub), nil
}

// DeviceFingerprint is the first 80 bits of SHA-256 of the public key, as
// five dash-separated groups of four hex digits.
func DeviceFingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	x := hex.EncodeToString(h[:10])
	return x[0:4] + "-" + x[4:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20]
}

// NormalizeDeviceFingerprint accepts a fingerprint with any case, spaces or
// dashes and returns the canonical form.
func NormalizeDeviceFingerprint(s string) (string, error) {
	x := strings.ToLower(strings.NewReplacer("-", "", " ", "", ":", "").Replace(strings.TrimSpace(s)))
	if len(x) != 20 {
		return "", fmt.Errorf("device fingerprint %q: want 20 hex digits like 3f2a-91c0-77de-0b4e-a1c9", s)
	}
	if _, err := hex.DecodeString(x); err != nil {
		return "", fmt.Errorf("device fingerprint %q: not hex", s)
	}
	return x[0:4] + "-" + x[4:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20], nil
}
