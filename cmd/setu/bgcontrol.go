package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dmdhrumilmistry/setu/internal/access"
	"github.com/dmdhrumilmistry/setu/internal/control"
	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

// dashboardHooks gives a background share (`setu share -d`) what `setu
// dashboard` reads: connection history in the service directory, and a
// control socket in the share's own 0700 session directory. Neither is
// needed to share, so problems are logged and the share goes on.
//
// It sets cfg.Events; call serve once the session exists, and stop after
// it has ended, so its last events are written.
func dashboardHooks(cfg *host.Config, sessionDir string) (serve func(context.Context, *host.Session), stop func()) {
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, "[setu] "+format+"\n", args...) }
	hctx, hcancel := context.WithCancel(context.Background())
	stop = hcancel
	if dir, err := service.Dir(); err != nil {
		logf("connection history unavailable: %v", err)
	} else if err := service.EnsureDir(dir); err != nil {
		logf("connection history unavailable: %v", err)
	} else {
		write, done := history.Open(dir).Writer(hctx, logf)
		src := "share:" + filepath.Base(sessionDir)
		cfg.Events = func(e history.Event) { e.Source = src; write(e) }
		stop = func() { hcancel(); <-done }
	}
	// The share's block and allow lists: a file in its own directory, so
	// they end with it.
	cfg.Access = access.NewChecker(access.Path(sessionDir), access.AllScope).Check
	serve = func(ctx context.Context, s *host.Session) {
		ln, err := control.Listen(sessionDir)
		if err != nil {
			logf("dashboard control socket unavailable: %v", err)
			return
		}
		go control.Serve(ctx, ln, oneShare{s}, logf)
	}
	return serve, stop
}

// oneShare is the control.Handler of a background share.
type oneShare struct{ s *host.Session }

func (o oneShare) Shares() []host.Status { return []host.Status{o.s.Status()} }

func (o oneShare) Session(profile string) (*host.Session, error) {
	if profile != "" {
		return nil, errors.New("this is a background share, not a service profile")
	}
	return o.s, nil
}
