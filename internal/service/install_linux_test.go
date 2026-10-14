package service

import (
	"strings"
	"testing"
)

func TestRenderSystemd(t *testing.T) {
	u := Unit{Exe: "/home/a b/bin/setu", Dir: "/home/a b/.config/setu/service", LogFile: "/ignored", Path: "/usr/bin:/home/a b/.local/bin:$X%y"}
	unit := renderSystemd(u)
	for _, want := range []string{
		`ExecStart="/home/a b/bin/setu" "service" "run" "--dir" "/home/a b/.config/setu/service"` + "\n",
		`Environment="PATH=/usr/bin:/home/a b/.local/bin:$$X%%y"`,
		"UMask=0077",
		"Restart=on-failure",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "--log-file") {
		t.Error("systemd unit should log to the journal, not a file")
	}
	if got := systemdQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("systemdQuote = %s", got)
	}
}
