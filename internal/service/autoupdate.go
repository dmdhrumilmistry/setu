package service

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/update"
)

// ErrUpdated is returned by Supervisor.Run when the setu binary was replaced
// (by an automatic update or `setu upgrade`) and the supervisor stopped so
// that the caller can start the new one. It only stops while no profile has
// anyone connected or able to resume, so an update never cuts a session off.
var ErrUpdated = errors.New("setu was updated; restart the service")

const (
	updateEvery   = 6 * time.Hour
	updateJitter  = 30 * time.Minute
	firstCheckMin = 5 * time.Minute  // let the machine settle after login
	restartIdle   = 2 * time.Minute  // nobody connected for this long first
	updateTimeout = 10 * time.Minute // one check-and-install
)

// updater checks for updates in the background and restarts the service
// into a replaced binary once nothing would be interrupted.
type updater struct {
	exe, stamp string
	next       time.Time
	busy       atomic.Bool
	idleSince  time.Time

	// From the last reconcile.
	pol      *policy.Policy
	autoPref bool

	source func(*policy.Policy) update.Source // tests
	now    func() time.Time                   // tests
}

func (u *updater) start(sv *Supervisor) {
	if u.now == nil {
		u.now = time.Now
	}
	if u.source == nil {
		u.source = func(p *policy.Policy) update.Source { return update.Source{BaseURL: p.UpdateURL} }
	}
	exe, err := update.Executable()
	if err != nil {
		sv.Log.Printf("updates: %v", err)
		return
	}
	u.exe, u.stamp = exe, update.Stamp(exe)
	update.Cleanup(exe)
	u.next = u.now().Add(firstCheckMin + rand.N(10*time.Minute))
}

// enabled reports whether the service may update itself now.
func (u *updater) enabled(release string) bool {
	if u.exe == "" || u.pol == nil || u.pol.DisableUpdate {
		return false
	}
	if u.pol.UpdateVersion != "" {
		return true // the organization's pin is applied regardless
	}
	return update.Valid(release) && (u.pol.AutoUpdate || u.autoPref)
}

// tick runs on every supervisor loop. It starts an update check when one is
// due, and reports true once the binary on disk changed and every profile
// has been idle for restartIdle.
func (u *updater) tick(ctx context.Context, sv *Supervisor) bool {
	if u.exe == "" {
		return false
	}
	now := u.now()
	if now.After(u.next) && u.enabled(sv.Release) && u.busy.CompareAndSwap(false, true) {
		u.next = now.Add(updateEvery + rand.N(updateJitter))
		pol, src := u.pol, u.source(u.pol)
		go func() {
			defer u.busy.Store(false)
			cctx, cancel := context.WithTimeout(ctx, updateTimeout)
			defer cancel()
			target, err := src.Target(cctx, sv.Release, pol.UpdateVersion)
			if err == nil && target != "" {
				sv.Log.Printf("updates: installing setu %s (running %s)", target, sv.Release)
				if err = src.Apply(cctx, u.exe, target); err == nil {
					sv.Log.Printf("updates: setu %s installed; the service switches to it once nobody is connected", target)
				}
			}
			if err != nil && ctx.Err() == nil {
				sv.Log.Printf("updates: %v", err)
			}
		}()
	}
	if u.busy.Load() || update.Stamp(u.exe) == u.stamp {
		return false
	}
	for _, r := range sv.running {
		if !r.idle() {
			u.idleSince = time.Time{}
			return false
		}
	}
	if u.idleSince.IsZero() {
		u.idleSince = now
	}
	return now.Sub(u.idleSince) >= restartIdle
}
