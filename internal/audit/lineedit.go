package audit

import (
	"time"
	"unicode/utf8"
)

// maxLineBytes bounds one reconstructed command; longer input is truncated
// (and flagged) so a huge paste cannot produce an unbounded log record.
const maxLineBytes = 8 << 10

// event is something worth recording in a keystroke stream: a submitted
// line, an unsubmitted remainder, or a control key such as ^C.
type event struct {
	control   string // "^C", "^D", "^Z"; empty for a line
	text      string
	edited    bool // cursor keys, history or completion were used: the shell may have run something else
	truncated bool
	secret    bool // typed while the terminal had echo off (a password prompt)
	submitted bool
	started   time.Time // first keystroke of the line
	at        time.Time // when the line was submitted (or the key pressed)
}

type escState int

const (
	escNone escState = iota
	escStart
	escCSI
	escSS3
)

// lineEditor reconstructs what a person typed from raw keystrokes. It is a
// best-effort model of a line editor: it understands backspace, ^U, ^W,
// bracketed paste and Enter, and flags lines where cursor movement, history
// recall or tab completion make the reconstruction uncertain.
type lineEditor struct {
	buf       []byte
	esc       escState
	csi       []byte
	paste     bool
	edited    bool
	truncated bool
	secret    bool
	started   time.Time // first keystroke of the current line
	now       time.Time // arrival time of the keystrokes being fed
}

// feed processes keystrokes that arrived at now.
func (e *lineEditor) feed(b []byte, now time.Time, emit func(event)) {
	e.now = now
	for _, c := range b {
		switch e.esc {
		case escStart:
			switch c {
			case '[':
				e.esc, e.csi = escCSI, e.csi[:0]
			case 'O':
				e.esc = escSS3
			case 0x1b:
				// ESC ESC: stay in escape state.
			default:
				e.esc = escNone // Alt+key, e.g. word movement
				e.edited = true
			}
			continue
		case escCSI:
			if c >= 0x40 && c <= 0x7e {
				switch string(e.csi) + string(c) {
				case "200~":
					e.paste = true
				case "201~":
					e.paste = false
				default:
					e.edited = true // arrows, Home/End, Delete, ...
				}
				e.esc = escNone
			} else if len(e.csi) < 32 {
				e.csi = append(e.csi, c)
			}
			continue
		case escSS3:
			e.esc = escNone
			e.edited = true // application-mode arrows
			continue
		}

		switch {
		case c == 0x1b:
			e.esc = escStart
		case c == '\r' || c == '\n':
			if e.paste {
				e.add('\n')
			} else {
				e.flush(true, emit)
			}
		case c == 0x7f || c == 0x08:
			if len(e.buf) > 0 {
				_, n := utf8.DecodeLastRune(e.buf)
				e.buf = e.buf[:len(e.buf)-n]
			}
		case c == 0x15: // ^U: kill line
			e.buf = e.buf[:0]
		case c == 0x17: // ^W: kill word
			i := len(e.buf)
			for i > 0 && e.buf[i-1] == ' ' {
				i--
			}
			for i > 0 && e.buf[i-1] != ' ' {
				i--
			}
			e.buf = e.buf[:i]
		case c == 0x03:
			e.reset()
			emit(event{control: "^C", at: e.now})
		case c == 0x04:
			if len(e.buf) == 0 {
				emit(event{control: "^D", at: e.now})
			} else {
				e.edited = true // delete-char in readline
			}
		case c == 0x1a:
			emit(event{control: "^Z", at: e.now})
		case c == '\t':
			e.add('\t')
			if !e.paste {
				e.edited = true // completion
			}
		case c < 0x20:
			if !e.paste {
				e.edited = true // ^A, ^E, ^K, ^R, ...
			}
		default:
			e.add(c)
		}
	}
}

func (e *lineEditor) add(c byte) {
	if len(e.buf) >= maxLineBytes {
		e.truncated = true
		return
	}
	if len(e.buf) == 0 && e.started.IsZero() {
		e.started = e.now
	}
	e.buf = append(e.buf, c)
}

// flush emits the current line (if any) and resets the editor.
func (e *lineEditor) flush(submitted bool, emit func(event)) {
	if len(e.buf) > 0 {
		emit(event{
			text:      string(e.buf),
			edited:    e.edited,
			truncated: e.truncated,
			secret:    e.secret,
			submitted: submitted,
			started:   e.started,
			at:        e.now,
		})
	}
	e.reset()
}

func (e *lineEditor) reset() {
	e.buf = e.buf[:0]
	e.edited, e.truncated, e.secret = false, false, false
	e.started = time.Time{}
}
