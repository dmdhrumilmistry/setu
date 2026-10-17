package service

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/update"
)

// TestRestartWhenReplacedAndIdle: a replaced binary restarts the service
// only after every profile was idle for restartIdle.
func TestRestartWhenReplacedAndIdle(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "setu")
	os.WriteFile(exe, []byte("v1"), 0o755)
	now := time.Now()
	sv := &Supervisor{Log: log.New(os.Stderr, "", 0), running: map[string]*runner{"busy": {}}}
	u := &sv.upd
	u.exe, u.stamp, u.now, u.next = exe, update.Stamp(exe), func() time.Time { return now }, now.Add(time.Hour)
	ctx := context.Background()
	if u.tick(ctx, sv) {
		t.Fatal("restart without a new binary")
	}
	os.WriteFile(exe, []byte("v2, longer"), 0o755)
	if u.tick(ctx, sv) {
		t.Fatal("restart without waiting for the idle period")
	}
	now = now.Add(restartIdle)
	if !u.tick(ctx, sv) {
		t.Fatal("no restart after the idle period")
	}
}

func TestUpdatesEnabled(t *testing.T) {
	u := &updater{exe: "/x", autoPref: true, pol: &policy.Policy{}}
	if !u.enabled("v1.0.0") || u.enabled("dev") {
		t.Error("releases update by default, development builds never")
	}
	u.autoPref = false
	if u.enabled("v1.0.0") {
		t.Error("the user turned automatic updates off")
	}
	u.pol = &policy.Policy{AutoUpdate: true}
	if !u.enabled("v1.0.0") {
		t.Error("AutoUpdate policy must override the user")
	}
	u.pol = &policy.Policy{DisableUpdate: true}
	u.autoPref = true
	if u.enabled("v1.0.0") {
		t.Error("DisableUpdate policy must win")
	}
	u.pol = &policy.Policy{UpdateVersion: "v1.2.0"}
	if !u.enabled("dev") {
		t.Error("an UpdateVersion pin applies even to development builds")
	}
}
