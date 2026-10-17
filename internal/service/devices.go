package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// Devices a profile admits live in devices.json, apart from config.json:
// the running supervisor writes them too (a device that pairs, or proves the
// password once), and keeping them out of config.json means it never races
// the CLI's edits of the profiles, and adding a device does not restart a
// profile's command. Every change is a short read-modify-write under
// devices.lock.

const devicesVersion = 1

// DeviceState is the content of devices.json.
type DeviceState struct {
	Version  int                        `json:"version"`
	Profiles map[string]*ProfileDevices `json:"profiles"`
}

// ProfileDevices are the devices one profile knows, and its pending
// pairing code.
type ProfileDevices struct {
	Devices map[string]*Device `json:"devices,omitempty"` // by device key fingerprint
	Pairing *Pairing           `json:"pairing,omitempty"`
}

// Device is a device a profile admits without a password or pairing code.
type Device struct {
	// Name describes the device (what it reported when it paired, or what
	// `device add --name` set). Informational only.
	Name string `json:"name,omitempty"`
	// Binding is the invite key the device may use: a person's name, or
	// "*control" / "*view" for the shared links.
	Binding  string    `json:"binding"`
	How      string    `json:"how"` // HowPairing, HowPassword or HowManual
	Added    time.Time `json:"added"`
	LastSeen time.Time `json:"last_seen,omitzero"`
}

// How a device became known.
const (
	HowPairing  = "pairing"  // proved a pairing code
	HowPassword = "password" // proved the profile password
	HowManual   = "manual"   // `setu service device add`
)

// Pairing is a one-time pairing code (only its verifier is stored).
type Pairing struct {
	Verifier  *secure.PasswordVerifier `json:"verifier"`
	ExpiresAt time.Time                `json:"expires_at"`
}

// Binding names the invite key a device is tied to, as the host does.
func Binding(person, role string) string {
	if person != "" {
		return person
	}
	return "*" + role
}

// BindingLabel describes a binding for people.
func BindingLabel(b string) string {
	switch b {
	case "*" + proto.RoleControl:
		return "shared control link"
	case "*" + proto.RoleView:
		return "shared view link"
	}
	return b + "'s link"
}

func DevicesPath(dir string) string { return filepath.Join(dir, "devices.json") }
func devicesLock(dir string) string { return filepath.Join(dir, "devices.lock") }

// LoadDevices reads devices.json; a missing file is empty.
func LoadDevices(dir string) (*DeviceState, error) {
	path := DevicesPath(dir)
	st := &DeviceState{Version: devicesVersion, Profiles: map[string]*ProfileDevices{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Version != devicesVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", path, st.Version)
	}
	if st.Profiles == nil {
		st.Profiles = map[string]*ProfileDevices{}
	}
	return st, nil
}

// UpdateDevices changes devices.json under its lock. fn's error aborts the
// change.
func UpdateDevices(dir string, fn func(*DeviceState) error) error {
	if err := EnsureDir(dir); err != nil {
		return err
	}
	release, err := lockFile(devicesLock(dir))
	if err != nil {
		return err
	}
	defer release()
	st, err := LoadDevices(dir)
	if err != nil {
		return err
	}
	if err := fn(st); err != nil {
		return err
	}
	for name, pd := range st.Profiles {
		if len(pd.Devices) == 0 && pd.Pairing == nil {
			delete(st.Profiles, name)
		}
	}
	st.Version = devicesVersion
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(DevicesPath(dir), b)
}

// Profile returns (creating) the devices of one profile.
func (st *DeviceState) Profile(name string) *ProfileDevices {
	pd := st.Profiles[name]
	if pd == nil {
		pd = &ProfileDevices{}
		st.Profiles[name] = pd
	}
	if pd.Devices == nil {
		pd.Devices = map[string]*Device{}
	}
	return pd
}

// Fingerprints returns the profile's device fingerprints, sorted.
func (pd *ProfileDevices) Fingerprints() []string {
	if pd == nil {
		return nil
	}
	fps := make([]string, 0, len(pd.Devices))
	for fp := range pd.Devices {
		fps = append(fps, fp)
	}
	sort.Strings(fps)
	return fps
}

// ActivePairing returns the pairing code's verifier if it is still valid.
func (pd *ProfileDevices) ActivePairing(now time.Time) *secure.PasswordVerifier {
	if pd == nil || pd.Pairing == nil || pd.Pairing.Verifier == nil || !now.Before(pd.Pairing.ExpiresAt) {
		return nil
	}
	return pd.Pairing.Verifier
}

// Forget removes devices of a profile: those bound to binding (all of
// them if binding is empty) that became known the way how (any way if how
// is empty). It returns how many were removed.
func (pd *ProfileDevices) Forget(binding, how string) int {
	n := 0
	for fp, d := range pd.Devices {
		if (binding == "" || d.Binding == binding) && (how == "" || d.How == how) {
			delete(pd.Devices, fp)
			n++
		}
	}
	return n
}

// lockFile takes an exclusive lock by creating path, waiting for a holder
// to finish. A lock older than lockStale was left by a crash and is taken
// over; holders keep it for milliseconds.
func lockFile(path string) (release func(), err error) {
	const lockStale = 30 * time.Second
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
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) > lockStale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked (remove it if no setu command is running)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// deviceStore gives one running profile access to its devices: it
// implements host.Devices and host.Pairer, reading devices.json on every
// question, so devices added or removed with the CLI apply at once.
type deviceStore struct {
	dir, profile string
	logf         func(format string, args ...any)
	now          func() time.Time
	pol          *policy.Policy // MaxPairingTime, MaxDevicesPerProfile
}

func (d *deviceStore) Known(fp, binding string) bool {
	st, err := LoadDevices(d.dir)
	if err != nil {
		d.logf("devices: %v", err)
		return false
	}
	dev := st.Profiles[d.profile]
	if dev == nil || dev.Devices[fp] == nil || dev.Devices[fp].Binding != binding {
		return false
	}
	// Record when the device was last used, at most once an hour.
	if d.now().Sub(dev.Devices[fp].LastSeen) > time.Hour {
		_ = UpdateDevices(d.dir, func(st *DeviceState) error {
			if x := st.Profile(d.profile).Devices[fp]; x != nil {
				x.LastSeen = d.now().UTC()
			}
			return nil
		})
	}
	return true
}

func (d *deviceStore) Remember(fp, binding string, c audit.Client) error {
	return d.add(fp, binding, HowPassword, c)
}

func (d *deviceStore) Pairing() *secure.PasswordVerifier {
	st, err := LoadDevices(d.dir)
	if err != nil {
		d.logf("devices: %v", err)
		return nil
	}
	pd := st.Profiles[d.profile]
	v := pd.ActivePairing(d.now())
	if v != nil && d.pol.CheckPairing(pd.Pairing.ExpiresAt.Sub(d.now())-time.Minute) != nil {
		d.logf("ignoring the pairing code: it works for longer than the managed policy allows (MaxPairingTime)")
		return nil
	}
	return v
}

func (d *deviceStore) Pair(v *secure.PasswordVerifier, fp, binding string, c audit.Client) error {
	return UpdateDevices(d.dir, func(st *DeviceState) error {
		pd := st.Profile(d.profile)
		cur := pd.ActivePairing(d.now())
		if cur == nil || !bytes.Equal(cur.Salt, v.Salt) || !bytes.Equal(cur.Key, v.Key) {
			return errors.New("pairing code used or expired")
		}
		pd.Pairing = nil // single use
		if err := d.pol.DeviceRoom(len(pd.Devices)); err != nil && pd.Devices[fp] == nil {
			return err
		}
		pd.Devices[fp] = d.device(binding, HowPairing, c)
		return nil
	})
}

func (d *deviceStore) add(fp, binding, how string, c audit.Client) error {
	return UpdateDevices(d.dir, func(st *DeviceState) error {
		pd := st.Profile(d.profile)
		if pd.Devices[fp] == nil {
			if err := d.pol.DeviceRoom(len(pd.Devices)); err != nil {
				return err
			}
		}
		pd.Devices[fp] = d.device(binding, how, c)
		return nil
	})
}

func (d *deviceStore) device(binding, how string, c audit.Client) *Device {
	now := d.now().UTC()
	name := []rune(c.Device.Summary()) // self-reported and sanitized: a label, nothing more
	if len(name) > 64 {
		name = name[:64]
	}
	return &Device{Name: strings.TrimSpace(string(name)), Binding: binding, How: how, Added: now, LastSeen: now}
}
