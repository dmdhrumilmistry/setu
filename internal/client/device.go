package client

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// LocalInfo describes this device for the host's audit log. It is sent only
// to the host, inside the end-to-end encrypted data channel.
func LocalInfo(version string) *proto.ClientInfo {
	info := &proto.ClientInfo{
		App:      "setu-cli",
		Version:  version,
		DeviceID: deviceID(),
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Timezone: localZone(),
	}
	info.Hostname, _ = os.Hostname()
	if u, err := user.Current(); err == nil {
		info.User = u.Username
	}
	if lang := os.Getenv("LANG"); lang != "" {
		info.Language, _, _ = strings.Cut(lang, ".")
	}
	return info
}

// localZone is the zone abbreviation and UTC offset, e.g. "IST +05:30"
// (or $TZ when set, e.g. "Asia/Kolkata").
func localZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	return time.Now().Format("MST -07:00")
}

// deviceID returns a random id kept in the user's config directory, so the
// host's audit log can tell repeat visits from the same device apart from
// new ones. It identifies an installation, not a person, and is not derived
// from any hardware identifier.
func deviceID() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(base, "setu", "device-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); len(id) == 32 {
			return id
		}
	}
	id := secure.RandomID(16)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return id
	}
	_ = os.WriteFile(path, []byte(id+"\n"), 0o600)
	return id
}
