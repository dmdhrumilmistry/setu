package secure

import (
	"crypto/rand"
	"strings"
)

// A pairing code lets a new device join a host that only admits devices it
// knows (a service profile in device mode). It is typed once on the new
// device, which is then remembered by its device key. The code is single use
// and short-lived, and the host stores only a PBKDF2 verifier of it; it is
// checked with the same challenge-response as a password, so it never
// crosses the wire. 12 symbols of Crockford base32 are 60 bits: far beyond
// what an attacker can guess online against the host's lockout.

const (
	pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford base32: no I, L, O, U
	pairingLen      = 12
)

// NewPairingCode returns a random code formatted for reading aloud, such as
// "7K2M-QX9D-4HRT".
func NewPairingCode() (string, error) {
	b := make([]byte, pairingLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(pairingAlphabet[c&31]) // 256 is a multiple of 32: no bias
	}
	return sb.String(), nil
}

// NormalizePairingCode turns what a person typed into the canonical form
// the verifier is derived from: upper case, separators dropped, and the
// letters people confuse with digits (O, I, L) read as those digits. The
// web client (web/js/crypto.mjs) does the same.
func NormalizePairingCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch {
		case r == 'O':
			sb.WriteByte('0')
		case r == 'I' || r == 'L':
			sb.WriteByte('1')
		case (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z'):
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// NewPairingVerifier returns a fresh pairing code and its verifier.
func NewPairingVerifier() (code string, v *PasswordVerifier, err error) {
	if code, err = NewPairingCode(); err != nil {
		return "", nil, err
	}
	v, err = NewPasswordVerifier(NormalizePairingCode(code))
	return code, v, err
}
