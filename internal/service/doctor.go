package service

import (
	"fmt"
	"time"
)

// Finding is one result of `setu doctor`.
type Finding struct {
	Level string `json:"level"` // LevelFail, LevelWarn or LevelInfo
	Area  string `json:"area"`  // what it is about, e.g. "profile claude"
	Msg   string `json:"message"`
	Fix   string `json:"fix,omitempty"` // a command or step that resolves it
}

// Finding levels, most severe first.
const (
	LevelFail = "fail"
	LevelWarn = "warn"
	LevelInfo = "info"
)

const (
	expirySoon   = 14 * 24 * time.Hour
	deviceUnused = 30 * 24 * time.Hour
)

// Findings checks one profile's security settings and devices.
func (p *Profile) Findings(name string, pd *ProfileDevices, now time.Time) []Finding {
	area := "profile " + name
	var out []Finding
	add := func(level, msg, fix string, args ...any) {
		out = append(out, Finding{Level: level, Area: area, Msg: msg, Fix: fmt.Sprintf(fix, args...)})
	}
	if !p.Enabled {
		add(LevelInfo, "disabled: not served", "setu service enable %s", name)
		return out
	}
	switch p.AuthMode() {
	case AuthNone:
		add(LevelFail, "anyone holding the link gets a shell: no pairing and no password", "setu service auth %s device", name)
	case AuthPassword:
		add(LevelInfo, "new devices get in with a password; paired devices are stronger (the link plus a one-time code)", "setu service auth %s device", name)
	}
	if p.ControlKey != "" {
		add(LevelWarn, "has a shared control link: the audit log cannot tell its holders apart",
			"give each person their own link (setu service key add %s NAME), then setu service rotate %s", name, name)
	}
	switch {
	case p.ExpiresAt.IsZero():
		add(LevelWarn, "keys never expire", "setu service set %s --valid-for 2160h && setu service rotate %s", name, name)
	case !now.Before(p.ExpiresAt):
		add(LevelWarn, "keys expired: the profile is not served", "setu service rotate %s", name)
	case p.ExpiresAt.Sub(now) < expirySoon:
		add(LevelWarn, "keys expire on "+p.ExpiresAt.Local().Format("2006-01-02"), "setu service rotate %s", name)
	}
	if p.IdleTimeout == 0 {
		add(LevelWarn, "a forgotten browser tab stays connected indefinitely", "setu service set %s --idle-timeout 30m", name)
	}
	if p.DeviceIdle == 0 {
		add(LevelWarn, "known devices never expire, so a lost device keeps access", "setu service set %s --device-idle 2160h", name)
	}
	if p.NoNotify {
		add(LevelInfo, "no desktop notification when someone connects", "setu service set %s --notify on", name)
	}
	if p.Desktop != nil && p.MaxClients > 1 {
		add(LevelInfo, fmt.Sprintf("up to %d people can control the desktop at once", p.MaxClients), "setu service set %s --max-clients 1", name)
	}
	if pd == nil {
		pd = &ProfileDevices{}
	}
	if p.RequireDevice && len(pd.Devices) == 0 && pd.ActivePairing(now) == nil {
		add(LevelInfo, "no device paired yet", "setu service pair %s", name)
	}
	if pd.ActivePairing(now) != nil {
		add(LevelInfo, "a pairing code is waiting until "+pd.Pairing.ExpiresAt.Local().Format("15:04"), "")
	}
	for _, fp := range pd.Fingerprints() {
		d := pd.Devices[fp]
		last := d.LastSeen
		if last.IsZero() {
			last = d.Added
		}
		if now.Sub(last) > deviceUnused {
			label := fp
			if d.Name != "" {
				label += " (" + d.Name + ")"
			}
			add(LevelWarn, fmt.Sprintf("device %s has not joined since %s", label, last.Local().Format("2006-01-02")), "setu service device remove %s %s", name, fp)
		}
	}
	return out
}
