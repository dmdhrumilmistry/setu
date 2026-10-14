package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderPlist(t *testing.T) {
	u := Unit{Exe: "/Users/a&b/bin/setu", Dir: "/Users/a&b/Library/Application Support/setu/service", LogFile: "/tmp/s.log", Path: "/usr/bin:/opt/homebrew/bin"}
	doc := renderPlist(u)
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = true
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("plist does not parse: %v\n%s", err, doc)
		}
	}
	for _, want := range []string{"<string>/Users/a&amp;b/bin/setu</string>", "<string>--log-file</string>", "<key>PATH</key>", "<key>Umask</key><integer>63</integer>"} {
		if !strings.Contains(doc, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
}
