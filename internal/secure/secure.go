// Package secure holds the cryptographic primitives shared by the host, the
// terminal client and (mirrored in JavaScript) the static web client.
//
// Nothing here is novel cryptography: it is HKDF-SHA256, AES-256-GCM,
// PBKDF2-SHA256 and HMAC-SHA256 from the Go standard library, combined so that
//
//   - a random 256-bit invite secret is the only thing a joiner needs,
//   - signaling relays only ever see ciphertext and a random-looking room id,
//   - an optional password is checked over the already-encrypted WebRTC
//     channel and bound to that channel's DTLS fingerprints,
//   - both ends can display a short authentication string (SAS) derived from
//     the DTLS fingerprints to rule out a man in the middle.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	infoRoomID    = "setu/v1/room-id"
	infoSignalKey = "setu/v1/signal-key"

	// PBKDF2Iterations is the work factor for password derived keys. It is
	// sent to the client in the hello message so it can be raised later
	// without a protocol change.
	PBKDF2Iterations = 200_000

	// MinPBKDF2Iterations is the lowest work factor a client accepts, so a
	// malicious host cannot make password guessing cheap.
	MinPBKDF2Iterations = 100_000
)

// Secret is a 256-bit invite secret. Whoever holds it can ask to join.
type Secret [32]byte

// NewSecret returns a fresh random secret.
func NewSecret() (Secret, error) {
	var s Secret
	if _, err := rand.Read(s[:]); err != nil {
		return s, err
	}
	return s, nil
}

// String encodes the secret as unpadded base64url (43 chars).
func (s Secret) String() string { return base64.RawURLEncoding.EncodeToString(s[:]) }

// ParseSecret decodes a secret produced by String.
func ParseSecret(v string) (Secret, error) {
	var s Secret
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return s, fmt.Errorf("invalid secret encoding: %w", err)
	}
	if len(b) != len(s) {
		return s, fmt.Errorf("invalid secret length %d", len(b))
	}
	copy(s[:], b)
	return s, nil
}

// Room is the signaling identity derived from a Secret: a public rendezvous
// id (hex, looks like a nostr pubkey) and a symmetric key that seals every
// signaling message.
type Room struct {
	ID   string
	aead cipher.AEAD
}

// NewRoom derives the room id and signaling key from a secret.
func NewRoom(s Secret) (*Room, error) {
	id, err := hkdf.Key(sha256.New, s[:], nil, infoRoomID, 32)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, s[:], nil, infoSignalKey, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Room{ID: hex.EncodeToString(id), aead: aead}, nil
}

// SignalAAD binds a sealed message to its room, sender and recipient so a
// relay cannot replay a ciphertext under a different identity.
func SignalAAD(roomID, from, to string) []byte {
	return []byte("setu/v1/signal|" + roomID + "|" + from + "|" + to)
}

// Seal encrypts plaintext and returns base64(nonce || ciphertext).
func (r *Room) Seal(plaintext, aad []byte) (string, error) {
	nonce := make([]byte, r.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := r.aead.Seal(nonce, nonce, plaintext, aad)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Open reverses Seal.
func (r *Room) Open(sealed string, aad []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, err
	}
	ns := r.aead.NonceSize()
	if len(raw) < ns+r.aead.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	return r.aead.Open(nil, raw[:ns], raw[ns:], aad)
}

// Fingerprint extracts the first DTLS fingerprint ("sha-256 AB:CD:...") from
// an SDP blob, normalised so both ends compute identical strings.
func Fingerprint(sdp string) (string, error) {
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=fingerprint:") {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(line, "a=fingerprint:"))
		if len(parts) != 2 {
			continue
		}
		return strings.ToLower(parts[0]) + " " + strings.ToUpper(parts[1]), nil
	}
	return "", errors.New("no DTLS fingerprint in SDP")
}

func sortedPair(a, b string) (string, string) {
	p := []string{a, b}
	sort.Strings(p)
	return p[0], p[1]
}

// SAS returns a 6 digit short authentication string ("123 456") derived from
// both DTLS fingerprints. If the host and the client display the same code,
// nobody is sitting in the middle of the DTLS session.
func SAS(fpA, fpB string) string {
	a, b := sortedPair(fpA, fpB)
	h := sha256.Sum256([]byte("setu/v1/sas\n" + a + "\n" + b))
	n := binary.BigEndian.Uint32(h[:4]) % 1_000_000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}

// PasswordKey stretches a password with PBKDF2-SHA256.
func PasswordKey(password string, salt []byte, iterations int) ([]byte, error) {
	if iterations < MinPBKDF2Iterations {
		return nil, fmt.Errorf("pbkdf2 iterations %d below minimum %d", iterations, MinPBKDF2Iterations)
	}
	return pbkdf2.Key(sha256.New, password, salt, iterations, 32)
}

// AuthProof is the client's answer to the host's password challenge. It is
// bound to the per-connection nonce and to both DTLS fingerprints, so a proof
// is useless outside the exact WebRTC session it was made for.
func AuthProof(key, nonce []byte, fpA, fpB string) []byte {
	a, b := sortedPair(fpA, fpB)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("setu/v1/auth\n"))
	m.Write([]byte(base64.StdEncoding.EncodeToString(nonce)))
	m.Write([]byte("\n" + a + "\n" + b))
	return m.Sum(nil)
}

// RandomBytes returns n random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return b
}

// RandomID returns a random hex id of n bytes.
func RandomID(n int) string { return hex.EncodeToString(RandomBytes(n)) }
