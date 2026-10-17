package e2e

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// dialSecret joins with a device key; secret answers both a password and a
// pairing prompt, and asked counts the prompts.
func dialSecret(ctx context.Context, t *testing.T, inv string, key *secure.DeviceKey, secret string, asked *int) (*client.Conn, error) {
	ask := func() (string, error) {
		*asked++
		if secret == "" {
			return "", errors.New("no secret to give")
		}
		return secret, nil
	}
	return client.Dial(ctx, inv, client.Options{Timeout: 30 * time.Second, Logf: t.Logf, DeviceKey: key, Password: ask, PairingCode: ask})
}

// TestPasswordOncePerDevice: a device that gave the password is not asked
// again, but only through the same invite; other devices still are.
func TestPasswordOncePerDevice(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctl, view := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Password = "s3cret-pass" })
	phone, _ := secure.NewDeviceKey()

	asked := 0
	c, err := dialSecret(ctx, t, ctl, phone, "s3cret-pass", &asked)
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || c.Auth != proto.AuthPassword {
		t.Errorf("first join: asked %d times, auth %q", asked, c.Auth)
	}
	waitFor(t, c, "READY")
	c.Close()

	asked = 0
	c, err = dialSecret(ctx, t, ctl, phone, "", &asked)
	if err != nil {
		t.Fatalf("remembered device asked for the password again: %v", err)
	}
	if asked != 0 || c.Auth != proto.AuthDevice {
		t.Errorf("second join: asked %d times, auth %q", asked, c.Auth)
	}
	c.Close()

	if _, err := dialSecret(ctx, t, view, phone, "", &asked); err == nil {
		t.Error("the device was remembered for the control link, but skipped the password on the view link")
	}
	laptop, _ := secure.NewDeviceKey()
	if _, err := dialSecret(ctx, t, ctl, laptop, "wrong-password", &asked); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("unknown device with a wrong password: %v", err)
	}
	// No device key: the password, as before.
	if c, err := dial(t, ctx, ctl, "s3cret-pass"); err != nil {
		t.Errorf("client without a device key: %v", err)
	} else {
		c.Close()
	}
}

func TestPasswordEveryJoin(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Password, c.PasswordEveryJoin = "s3cret-pass", true })
	phone, _ := secure.NewDeviceKey()
	asked := 0
	c, err := dialSecret(ctx, t, ctl, phone, "s3cret-pass", &asked)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if _, err := dialSecret(ctx, t, ctl, phone, "", &asked); err == nil {
		t.Fatal("password skipped despite PasswordEveryJoin")
	}
	if asked != 2 {
		t.Errorf("asked %d times, want 2", asked)
	}
}

// pairStore is a host.Devices and host.Pairer kept in memory.
type pairStore struct {
	mu      sync.Mutex
	known   map[string]string
	pairing *secure.PasswordVerifier
}

func (p *pairStore) Known(fp, binding string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.known[fp]
	return ok && b == binding
}

func (p *pairStore) Remember(fp, binding string, _ audit.Client) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known[fp] = binding
	return nil
}

func (p *pairStore) Pairing() *secure.PasswordVerifier {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pairing
}

func (p *pairStore) Pair(v *secure.PasswordVerifier, fp, binding string, _ audit.Client) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pairing != v {
		return errors.New("used")
	}
	p.pairing = nil
	p.known[fp] = binding
	return nil
}

func (p *pairStore) forget(fp string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.known, fp)
}

// TestRequireDevice: only paired devices get in; a pairing code works once
// and is typed in any case and grouping; removing a device locks it out.
func TestRequireDevice(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code, v, err := secure.NewPairingVerifier()
	if err != nil {
		t.Fatal(err)
	}
	store := &pairStore{known: map[string]string{}, pairing: v}
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) {
		c.RequireDevice, c.Devices, c.Pairing, c.Profile = true, store, store, "claude"
	})
	phone, _ := secure.NewDeviceKey()
	laptop, _ := secure.NewDeviceKey()

	asked := 0
	if _, err := dialSecret(ctx, t, ctl, nil, code, &asked); err == nil {
		t.Error("a client without a device key paired")
	}
	if _, err := dialSecret(ctx, t, ctl, laptop, "AAAA-AAAA-AAAA", &asked); err == nil {
		t.Error("a wrong pairing code was accepted")
	}
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	c, err := dialSecret(ctx, t, ctl, phone, typed, &asked)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	if c.Auth != proto.AuthPairing {
		t.Errorf("auth %q, want %q", c.Auth, proto.AuthPairing)
	}
	waitFor(t, c, "READY")
	c.Close()

	asked = 0
	if c, err = dialSecret(ctx, t, ctl, phone, "", &asked); err != nil || asked != 0 || c.Auth != proto.AuthDevice {
		t.Fatalf("paired device rejoining: err %v, asked %d", err, asked)
	}
	if _, err := dialSecret(ctx, t, ctl, laptop, code, &asked); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Errorf("the used pairing code let a second device in: %v", err)
	}

	store.forget(phone.Fingerprint())
	deadline := time.After(20 * time.Second)
	select {
	case <-c.Closed:
	case <-deadline:
		t.Fatal("a removed device stayed connected")
	}
	if _, err := dialSecret(ctx, t, ctl, phone, "", &asked); err == nil {
		t.Error("a removed device joined again")
	}
}

// TestPairingLinkAndAlerts: a pairing link (p=) pairs without typing the
// code, and the host is notified of the pairing and of later connections.
func TestPairingLinkAndAlerts(t *testing.T) {
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code, v, err := secure.NewPairingVerifier()
	if err != nil {
		t.Fatal(err)
	}
	store := &pairStore{known: map[string]string{}, pairing: v}
	var mu sync.Mutex
	var titles []string
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) {
		c.RequireDevice, c.Devices, c.Pairing, c.Profile = true, store, store, "claude"
		c.Notify = func(title, body string) {
			mu.Lock()
			defer mu.Unlock()
			titles = append(titles, title)
		}
	})
	inv, err := link.Parse(ctl)
	if err != nil {
		t.Fatal(err)
	}
	inv.Pairing = code
	phone, _ := secure.NewDeviceKey()
	asked := 0
	c, err := dialSecret(ctx, t, link.Build("", inv), phone, "", &asked)
	if err != nil {
		t.Fatalf("pairing link: %v", err)
	}
	if asked != 0 || c.Auth != proto.AuthPairing {
		t.Errorf("pairing link: asked %d times, auth %q", asked, c.Auth)
	}
	c.Close()
	time.Sleep(300 * time.Millisecond)
	if c, err = dialSecret(ctx, t, ctl, phone, "", &asked); err != nil {
		t.Fatal(err)
	}
	c.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(titles) != 2 || titles[0] != "setu: new device paired" || !strings.Contains(titles[1], "connected") {
		t.Errorf("notifications %q", titles)
	}
}
