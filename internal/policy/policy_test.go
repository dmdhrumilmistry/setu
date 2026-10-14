package policy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
)

func mustParse(t *testing.T, s string) *Policy {
	t.Helper()
	p, err := Parse([]byte(s), "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func isViolation(err error, key string) bool {
	var v *Violation
	return errors.As(err, &v) && v.Key == key
}

func TestParseStrict(t *testing.T) {
	for _, bad := range []string{
		`{"DisableShre": true}`,         // typo must not silently allow
		`{"MaxClients": -1}`,            // negative
		`{"AuditInput": "everything"}`,  // bad enum
		`{"AuditRequired": true}`,       // audit settings without endpoint
		`{"MaxKeyValidity": "forever"}`, // bad duration
		`not json`,
	} {
		if _, err := Parse([]byte(bad), "test"); err == nil {
			t.Errorf("Parse(%s) accepted", bad)
		}
	}
	p := mustParse(t, `{"MaxKeyValidity": "720h", "MaxShareDuration": 3600}`)
	if p.MaxKeyValidity.D() != 720*time.Hour || p.MaxShareDuration.D() != time.Hour {
		t.Errorf("durations %v %v", p.MaxKeyValidity.D(), p.MaxShareDuration.D())
	}
	if !p.Managed() || p.Hash() == "" {
		t.Error("parsed policy should be managed with a hash")
	}
	var none *Policy
	if none.Managed() || (&Policy{}).Managed() {
		t.Error("nil / empty policy reported as managed")
	}
}

func TestProfilePrefsIgnorePayloadKeys(t *testing.T) {
	p, err := parseProfilePrefs([]byte(`{"PayloadUUID":"x","PayloadIdentifier":"y","PayloadVersion":1,"DisableShare":true}`), "profile")
	if err != nil || !p.DisableShare {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := parseProfilePrefs([]byte(`{"PayloadUUID":"x","Bogus":1}`), "profile"); err == nil {
		t.Error("unknown key in a profile accepted")
	}
}

func TestChecks(t *testing.T) {
	p := mustParse(t, `{
		"DisableManual": true,
		"RequirePassword": true, "MinPasswordLength": 14,
		"MaxClients": 2, "MaxShareDuration": "1h", "MaxKeyValidity": "720h",
		"AllowedCommands": ["claude", "/usr/bin/bash"],
		"AllowedRelays": ["wss://relay.corp.example"],
		"AllowedWebURLs": ["https://setu.corp.example/"]
	}`)
	if err := p.CheckShare(false); err != nil {
		t.Error(err)
	}
	if !isViolation(p.CheckShare(true), "DisableManual") {
		t.Error("manual allowed")
	}
	for cmd, ok := range map[string]bool{
		"claude": true, "/opt/bin/claude": true,
		"/usr/bin/bash": true, "/bin/bash": false, "bash": false, "zsh": false,
	} {
		if err := p.CheckCommand([]string{cmd}); (err == nil) != ok {
			t.Errorf("CheckCommand(%s) = %v, want allowed=%v", cmd, err, ok)
		}
	}
	if runtime.GOOS == "windows" { // npm shims resolve to claude.cmd
		if err := p.CheckCommand([]string{`C:\Users\a\AppData\Roaming\npm\claude.cmd`}); err != nil {
			t.Error(err)
		}
	}
	if r, err := p.Relays(nil); err != nil || len(r) != 1 || r[0] != "wss://relay.corp.example" {
		t.Errorf("default relays %v %v", r, err)
	}
	if _, err := p.Relays([]string{"wss://relay.damus.io"}); !isViolation(err, "AllowedRelays") {
		t.Error("foreign relay allowed")
	}
	if p.CheckWebURL("https://evil.example/") == nil || p.CheckWebURL("") != nil || p.CheckWebURL("https://setu.corp.example/") != nil {
		t.Error("web URL allow list")
	}
	if !isViolation(p.CheckPassword(false, 0), "RequirePassword") || !isViolation(p.CheckPassword(true, 8), "MinPasswordLength") || p.CheckPassword(true, 20) != nil {
		t.Error("password checks")
	}
	if p.MinPassword(8) != 14 || p.MinPassword(20) != 20 {
		t.Error("MinPassword")
	}
	if n, err := p.Clients(5, false); n != 2 || err != nil {
		t.Errorf("default clients clamped to %d, %v", n, err)
	}
	if _, err := p.Clients(5, true); !isViolation(err, "MaxClients") {
		t.Error("explicit clients above cap allowed")
	}
	if d, err := p.ShareExpiry(0); d != time.Hour || err != nil {
		t.Errorf("no expiry became %v %v", d, err)
	}
	if _, err := p.ShareExpiry(2 * time.Hour); !isViolation(err, "MaxShareDuration") {
		t.Error("long share allowed")
	}
	if !isViolation(p.CheckKeyValidity(0), "MaxKeyValidity") || p.CheckKeyValidity(24*time.Hour) != nil {
		t.Error("key validity")
	}

	var none *Policy // no policy allows everything
	if none.CheckShare(true) != nil || none.CheckCommand([]string{"x"}) != nil || none.CheckPassword(false, 0) != nil {
		t.Error("nil policy restricted something")
	}
}

func TestDisable(t *testing.T) {
	p := mustParse(t, `{"DisableShare": true, "DisableService": true}`)
	if !isViolation(p.CheckShare(false), "DisableShare") || !isViolation(p.CheckService(), "DisableService") {
		t.Error("disable switches")
	}
	if !strings.Contains(p.CheckService().Error(), "organization's policy") {
		t.Error("violation message should say it is a policy decision")
	}
}

func TestAuditConfig(t *testing.T) {
	p := mustParse(t, `{
		"AuditEndpoint": "https://otel.corp.example", "AuditHeaders": {"Authorization": "Bearer s3cret"},
		"AuditRequired": true, "AuditRedact": ["corp-[0-9]+"],
		"ResourceAttributes": {"device.serial": "C02XYZ"}
	}`)
	c, err := p.AuditConfig(audit.Config{})
	if err != nil || c.Endpoint != "https://otel.corp.example" || !c.Required || c.Headers["Authorization"] != "Bearer s3cret" {
		t.Fatalf("forced audit %+v %v", c, err)
	}
	c, err = p.AuditConfig(audit.Config{Endpoint: "https://otel.corp.example", Redact: []string{"mine"}})
	if err != nil || len(c.Redact) != 2 {
		t.Errorf("user redaction not merged: %+v %v", c, err)
	}
	if _, err := p.AuditConfig(audit.Config{Endpoint: "https://elsewhere.example"}); !isViolation(err, "AuditEndpoint") {
		t.Error("user redirected the audit log")
	}
	if _, err := p.AuditConfig(audit.Config{Input: audit.InputOff}); !isViolation(err, "AuditInput") {
		t.Error("user turned command auditing off")
	}
	attrs := p.AuditAttributes()
	if attrs["device.serial"] != "C02XYZ" || attrs["setu.policy.hash"] != p.Hash() || attrs["setu.policy.source"] != "test" {
		t.Errorf("audit attributes %v", attrs)
	}
	if r := p.Redacted(); r.AuditHeaders["Authorization"] != "[hidden]" || p.AuditHeaders["Authorization"] != "Bearer s3cret" {
		t.Error("Redacted must hide header values without changing the policy")
	}
	user := audit.Config{Endpoint: "https://mine.example"}
	if c, err := (&Policy{}).AuditConfig(user); err != nil || c.Endpoint != user.Endpoint {
		t.Error("no policy must leave the user's audit settings alone")
	}
}

func TestLoadFileRefusesUserOwned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ownership check needs a file owned by a non-administrator")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"DisableShare": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "not root") {
		t.Fatalf("user-owned policy file accepted: %v", err)
	}
}
