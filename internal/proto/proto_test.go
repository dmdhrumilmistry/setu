package proto

import (
	"strings"
	"testing"
)

func TestClientInfoSanitized(t *testing.T) {
	in := &ClientInfo{
		App:       "setu-cli",
		Hostname:  "laptop\x1b[31m\nFAKE [setu] client #9 connected",
		User:      "  dhrumil\r\n",
		DeviceID:  "../../etc/passwd",
		UserAgent: strings.Repeat("A", 5000),
		OS:        "linux‮", // bidi override
		Mobile:    true,
	}
	out := in.Sanitized()
	if strings.ContainsAny(out.Hostname, "\x1b\n\r") || out.Hostname != "laptop[31mFAKE [setu] client #9 connected" {
		t.Errorf("hostname %q", out.Hostname)
	}
	if out.User != "dhrumil" || out.OS != "linux" || out.DeviceID != "" {
		t.Errorf("user %q os %q device id %q", out.User, out.OS, out.DeviceID)
	}
	if len([]rune(out.UserAgent)) != maxInfoField || !out.Mobile {
		t.Errorf("user agent length %d", len(out.UserAgent))
	}
	if in.Hostname == out.Hostname {
		t.Error("Sanitized must not modify its receiver")
	}
	if (&ClientInfo{DeviceID: "0123456789abcdef0123456789abcdef"}).Sanitized().DeviceID == "" {
		t.Error("valid device id dropped")
	}
	var none *ClientInfo
	if none.Sanitized() != nil || (&ClientInfo{App: " \x07 "}).Sanitized() != nil {
		t.Error("empty info should sanitize to nil")
	}
}

func TestClientInfoSummary(t *testing.T) {
	cases := map[string]*ClientInfo{
		"laptop (linux/amd64, setu-cli)": {Hostname: "laptop", OS: "linux", Arch: "amd64", App: "setu-cli"},
		"Pixel 8 (Android 15, setu-web)": {Model: "Pixel 8", OS: "Android", OSVersion: "15", App: "setu-web"},
		"Windows, setu-web":              {OS: "Windows", App: "setu-web"},
		"":                               nil,
	}
	for want, c := range cases {
		if got := c.Summary(); got != want {
			t.Errorf("Summary(%+v) = %q, want %q", c, got, want)
		}
	}
}
