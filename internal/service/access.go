package service

import (
	"context"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/access"
	"github.com/dmdhrumilmistry/setu/internal/control"
)

// EnforceAccess asks the running service to apply changed block and allow
// lists at once (it also does so by itself within seconds): profile is a
// profile name, or access.AllScope for every profile. It reports how many
// clients were disconnected; a service that is not running is no error.
func EnforceAccess(dir, profile string) (int, error) {
	if _, ok := RunningPID(LockPath(dir)); !ok {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	names := []string{profile}
	if profile == access.AllScope {
		resp, err := control.Call(ctx, dir, control.Request{Op: control.OpStatus})
		if err != nil {
			return 0, err
		}
		names = names[:0]
		for _, s := range resp.Shares {
			names = append(names, s.Profile)
		}
	}
	n := 0
	for _, name := range names {
		resp, err := control.Call(ctx, dir, control.Request{Op: control.OpEnforce, Profile: name})
		if err != nil && profile != access.AllScope {
			return n, err
		}
		n += resp.Count
	}
	return n, nil
}
