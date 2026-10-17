package host

import (
	"sync"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// Devices remembers devices that proved the session's secret (its password,
// or a pairing code), so they can join again without it. A device proves
// itself with its device key, which signs every connection and cannot be
// copied out of the browser (or `setu join` key file) that holds it, so the
// secret is asked once per device instead of on every join.
//
// A device is remembered for the invite key it joined with (its binding:
// see peer.binding), so a device remembered through the view link cannot
// skip the password on the control link. Implementations must be safe for
// concurrent use.
type Devices interface {
	Known(fp, binding string) bool
	Remember(fp, binding string, c audit.Client) error
}

// Pairer hands out one-time pairing codes, for hosts that admit only
// devices they know (Config.RequireDevice).
type Pairer interface {
	// Pairing returns the active pairing code's verifier, or nil.
	Pairing() *secure.PasswordVerifier
	// Pair remembers device fp after it proved pairing code v, and uses
	// the code up. It fails if v is no longer the active code (it was used
	// meanwhile, or expired).
	Pair(v *secure.PasswordVerifier, fp, binding string, c audit.Client) error
}

// maxMemoryDevices bounds the devices one share remembers in memory.
const maxMemoryDevices = 256

// memoryDevices remembers devices for the life of one share.
type memoryDevices struct {
	mu   sync.Mutex
	seen map[string]string // fingerprint -> binding
}

func newMemoryDevices() *memoryDevices { return &memoryDevices{seen: map[string]string{}} }

func (m *memoryDevices) Known(fp, binding string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.seen[fp]
	return ok && b == binding
}

func (m *memoryDevices) Remember(fp, binding string, _ audit.Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.seen[fp]; !ok && len(m.seen) >= maxMemoryDevices {
		return nil // a convenience: never grow without bound
	}
	m.seen[fp] = binding
	return nil
}

// secret returns what a new, unknown device must prove: the password, or
// the active pairing code (pairing=true), or nil if nothing.
func (s *Session) secret() (v *secure.PasswordVerifier, pairing bool) {
	if s.pwKey != nil {
		return &secure.PasswordVerifier{Salt: s.pwSalt, Iter: s.pwIter, Key: s.pwKey}, false
	}
	if s.cfg.RequireDevice && s.cfg.Pairing != nil {
		if v := s.cfg.Pairing.Pairing(); v != nil && v.Validate() == nil {
			return v, true
		}
	}
	return nil, false
}

// deviceAuth reports whether devices that proved the secret are remembered.
func (s *Session) deviceAuth() bool { return s.devices != nil }

// knownDevice reports whether device fp, joining through binding, may skip
// the secret.
func (s *Session) knownDevice(fp, binding string) bool {
	return fp != "" && s.devices != nil && s.devices.Known(fp, binding)
}

// dropRevoked disconnects clients whose device is no longer known, on hosts
// that admit only known devices: removing a device takes effect at once,
// not only at its next join.
func (s *Session) dropRevoked() {
	if !s.cfg.RequireDevice {
		return
	}
	for _, p := range s.readyPeers(false) {
		p.mu.Lock()
		fp := p.deviceFP
		p.mu.Unlock()
		if !s.knownDevice(fp, p.binding()) {
			s.revokeTicket(p)
			p.close("device removed from the host")
		}
	}
}
