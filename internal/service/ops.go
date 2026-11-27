package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// The changes below are made by `setu dashboard`; the CLI shares the ones
// it can. Each re-reads the file it changes right before writing, checks
// the managed policy the same way the supervisor will, and writes
// atomically. The running supervisor applies them within seconds.

// DefaultPairFor is how long a pairing code works unless told otherwise:
// long enough to pick up the other device, short enough not to linger.
const DefaultPairFor = 15 * time.Minute

// ErrExpired is returned when enabling a profile whose keys have expired.
var ErrExpired = errors.New("its keys have expired: rotate them first")

// SetEnabled enables or disables a profile.
func SetEnabled(dir string, pol *policy.Policy, name string, enable bool) error {
	return updateProfile(dir, pol, name, func(p *Profile, now time.Time) error {
		p.Enabled = enable
		if enable && !p.Active(now) {
			return ErrExpired
		}
		return nil
	})
}

// RotateKeys replaces every key of a profile, so every old link stops
// working at once, and restarts its expiry clock.
func RotateKeys(dir string, pol *policy.Policy, name string) error {
	return updateProfile(dir, pol, name, func(p *Profile, now time.Time) error {
		if err := p.Rotate(now); err != nil {
			return err
		}
		p.Rotated = now.UTC()
		return nil
	})
}

func updateProfile(dir string, pol *policy.Policy, name string, fn func(*Profile, time.Time) error) error {
	path := ConfigPath(dir)
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	p, err := cfg.Get(name)
	if err != nil {
		return err
	}
	now := time.Now()
	if err := fn(p, now); err != nil {
		return err
	}
	if p.Enabled {
		if err := p.CheckPolicy(pol, now); err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
	}
	return Save(path, cfg)
}

// NewPairing stores a fresh pairing code for a profile, replacing any
// earlier one, and returns it. Only its PBKDF2 verifier is kept.
func NewPairing(dir, name string, d time.Duration) (code string, expires time.Time, err error) {
	code, v, err := secure.NewPairingVerifier()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = time.Now().Add(d)
	err = UpdateDevices(dir, func(ds *DeviceState) error {
		ds.Profile(name).Pairing = &Pairing{Verifier: v, ExpiresAt: expires.UTC()}
		return nil
	})
	return code, expires, err
}

// StartPairing checks that a profile pairs devices and that the policy
// allows a code for that long (capping the default at the policy's limit),
// then creates one with NewPairing.
func StartPairing(dir string, pol *policy.Policy, name string, d time.Duration) (code string, expires time.Time, err error) {
	cfg, err := Load(ConfigPath(dir))
	if err != nil {
		return "", time.Time{}, err
	}
	p, err := cfg.Get(name)
	if err != nil {
		return "", time.Time{}, err
	}
	if !p.RequireDevice {
		return "", time.Time{}, fmt.Errorf("%s does not use paired devices", name)
	}
	if d <= 0 {
		d = DefaultPairFor
		if pol.MaxPairingTime > 0 {
			d = min(d, pol.MaxPairingTime.D())
		}
	}
	if err := pol.CheckPairing(d); err != nil {
		return "", time.Time{}, err
	}
	return NewPairing(dir, name, d)
}

// RemoveDevice forgets one device of a profile. A connected client using
// it is disconnected by the supervisor within seconds.
func RemoveDevice(dir, name, fp string) error {
	fp, err := secure.NormalizeDeviceFingerprint(fp)
	if err != nil {
		return err
	}
	found := false
	err = UpdateDevices(dir, func(ds *DeviceState) error {
		if pd := ds.Profiles[name]; pd != nil && pd.Devices[fp] != nil {
			delete(pd.Devices, fp)
			found = true
		}
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("%s does not know that device", name)
	}
	return err
}
