package service

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestRenderTask(t *testing.T) {
	u := Unit{Exe: `C:\Program Files\setu\setu.exe`, Dir: `C:\Users\a b\AppData\Local\setu\service`, LogFile: `C:\Users\a b\AppData\Local\setu\service\service.log`}
	doc := renderTask(u, `DOMAIN\a&b`)
	var task struct {
		Settings struct {
			ExecutionTimeLimit string
			RunLevel           string
		}
		Principals struct {
			Principal struct {
				UserId   string
				RunLevel string
			}
		}
		Actions struct {
			Exec struct {
				Command   string
				Arguments string
			}
		}
	}
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	if err := dec.Decode(&task); err != nil {
		t.Fatalf("task XML does not parse: %v\n%s", err, doc)
	}
	if task.Settings.ExecutionTimeLimit != "PT0S" {
		t.Error("task would be killed after the default 72 hours")
	}
	if task.Principals.Principal.UserId != `DOMAIN\a&b` || task.Principals.Principal.RunLevel != "LeastPrivilege" {
		t.Errorf("principal %+v", task.Principals.Principal)
	}
	if task.Actions.Exec.Command != u.Exe {
		t.Errorf("command %q", task.Actions.Exec.Command)
	}
	want := `service run --dir "C:\Users\a b\AppData\Local\setu\service" --log-file "C:\Users\a b\AppData\Local\setu\service\service.log" --no-console`
	if task.Actions.Exec.Arguments != want {
		t.Errorf("arguments\n got %s\nwant %s", task.Actions.Exec.Arguments, want)
	}
	if b := utf16File("A"); len(b) != 4 || b[0] != 0xFF || b[1] != 0xFE || b[2] != 'A' {
		t.Errorf("utf16 encoding % x", b)
	}
}
