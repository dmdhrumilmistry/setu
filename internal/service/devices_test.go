package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

func newStore(t *testing.T, pol *policy.Policy) (*deviceStore, *time.Time) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &deviceStore{dir: t.TempDir(), profile: "claude", logf: t.Logf, now: func() time.Time { return now }, pol: pol}, &now
}

func setPairing(t *testing.T, d *deviceStore, v *secure.PasswordVerifier, until time.Time) {
	t.Helper()
	if err := UpdateDevices(d.dir, func(st *DeviceState) error {
		st.Profile(d.profile).Pairing = &Pairing{Verifier: v, ExpiresAt: until}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceStorePairing(t *testing.T) {
	d, now := newStore(t, nil)
	_, v, err := secure.NewPairingVerifier()
	if err != nil {
		t.Fatal(err)
	}
	setPairing(t, d, v, now.Add(15*time.Minute))
	ctl := Binding("", proto.RoleControl)
	if got := d.Pairing(); got == nil {
		t.Fatal("active pairing code not offered")
	}
	if err := d.Pair(v, "aaaa-bbbb-cccc-dddd-eeee", ctl, audit.Client{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Pair(v, "1111-2222-3333-4444-5555", ctl, audit.Client{}); err == nil {
		t.Error("a pairing code worked twice")
	}
	if d.Pairing() != nil {
		t.Error("used pairing code still offered")
	}
	if !d.Known("aaaa-bbbb-cccc-dddd-eeee", ctl) || d.Known("aaaa-bbbb-cccc-dddd-eeee", Binding("", proto.RoleView)) {
		t.Error("paired device must be known for its own link only")
	}
	st, err := os.Stat(DevicesPath(d.dir))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 && filepath.Separator == '/' {
		t.Errorf("devices.json mode %v, want owner only", st.Mode().Perm())
	}

	setPairing(t, d, v, now.Add(-time.Second))
	if d.Pairing() != nil {
		t.Error("expired pairing code offered")
	}
}

func TestDeviceStorePolicy(t *testing.T) {
	d, now := newStore(t, &policy.Policy{MaxPairingTime: policy.Duration(10 * time.Minute), MaxDevicesPerProfile: 1})
	_, v, _ := secure.NewPairingVerifier()
	setPairing(t, d, v, now.Add(time.Hour))
	if d.Pairing() != nil {
		t.Error("a pairing code beyond MaxPairingTime was offered")
	}
	ctl := Binding("", proto.RoleControl)
	if err := d.Remember("aaaa-bbbb-cccc-dddd-eeee", ctl, audit.Client{}); err != nil {
		t.Fatal(err)
	}
	if err := d.Remember("1111-2222-3333-4444-5555", ctl, audit.Client{}); err == nil {
		t.Error("MaxDevicesPerProfile not enforced")
	}
	if err := d.Remember("aaaa-bbbb-cccc-dddd-eeee", ctl, audit.Client{}); err != nil {
		t.Errorf("re-remembering a known device: %v", err)
	}
}

func TestForget(t *testing.T) {
	pd := &ProfileDevices{Devices: map[string]*Device{
		"a": {Binding: "alice", How: HowPairing},
		"b": {Binding: "alice", How: HowPassword},
		"c": {Binding: "*control", How: HowPassword},
	}}
	if n := pd.Forget("", HowPassword); n != 2 || pd.Devices["a"] == nil {
		t.Errorf("forget by how: %d, left %v", n, pd.Fingerprints())
	}
	if n := pd.Forget("alice", ""); n != 1 || len(pd.Devices) != 0 {
		t.Errorf("forget by binding: %d", n)
	}
}
