package audit

import (
	"reflect"
	"testing"
)

func feedAll(chunks ...string) []event {
	var ed lineEditor
	var out []event
	for _, c := range chunks {
		ed.feed([]byte(c), func(e event) { out = append(out, e) })
	}
	return out
}

func TestLineEditor(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []event
	}{
		{"simple", []string{"ls -la\r"}, []event{{text: "ls -la", submitted: true}}},
		{"keystrokes split across writes", []string{"l", "s", "\r"}, []event{{text: "ls", submitted: true}}},
		{"backspace", []string{"lss\x7f -l\r"}, []event{{text: "ls -l", submitted: true}}},
		{"utf8 backspace", []string{"caf\xc3\xa9\x7fe\r"}, []event{{text: "cafe", submitted: true}}},
		{"ctrl-u", []string{"rm -rf /\x15echo hi\r"}, []event{{text: "echo hi", submitted: true}}},
		{"ctrl-w", []string{"git push origin\x17main\r"}, []event{{text: "git push main", submitted: true}}},
		{"ctrl-c resets", []string{"sleep 100\x03"}, []event{{control: "^C"}}},
		{"empty enter ignored", []string{"\r\r\n"}, nil},
		{"arrow marks edited", []string{"\x1b[Als\r"}, []event{{text: "ls", edited: true, submitted: true}}},
		{"ss3 arrow marks edited", []string{"\x1bOAls\r"}, []event{{text: "ls", edited: true, submitted: true}}},
		{"tab completion marks edited", []string{"cd Doc\t\r"}, []event{{text: "cd Doc\t", edited: true, submitted: true}}},
		{
			"bracketed paste keeps newlines until Enter",
			[]string{"\x1b[200~line one\nline two\x1b[201~", "\r"},
			[]event{{text: "line one\nline two", submitted: true}},
		},
		{"ctrl-d on empty line", []string{"\x04"}, []event{{control: "^D"}}},
		{"edited flag resets per line", []string{"\x1b[Da\rb\r"}, []event{{text: "a", edited: true, submitted: true}, {text: "b", submitted: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedAll(tc.in...); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestLineEditorTruncates(t *testing.T) {
	big := make([]byte, maxLineBytes+100)
	for i := range big {
		big[i] = 'a'
	}
	got := feedAll(string(big), "\r")
	if len(got) != 1 || len(got[0].text) != maxLineBytes || !got[0].truncated {
		t.Fatalf("len=%d truncated=%v", len(got[0].text), got[0].truncated)
	}
}

func TestLineEditorFlushUnsubmitted(t *testing.T) {
	var ed lineEditor
	var out []event
	ed.feed([]byte("half typed"), func(e event) { out = append(out, e) })
	ed.flush(false, func(e event) { out = append(out, e) })
	if len(out) != 1 || out[0].submitted || out[0].text != "half typed" {
		t.Fatalf("%+v", out)
	}
}
