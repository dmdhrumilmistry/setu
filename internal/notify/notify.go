// Package notify shows desktop notifications on the host, so whoever sits at
// the machine notices when someone connects to it, even when no terminal is
// watching (the service, background shares).
//
// Text is passed to the notifier as separate arguments or environment
// variables, never spliced into a script or shell command, so a client's
// self-reported device name cannot inject anything. Delivery is best effort:
// a missing notifier or a locked session never affects the share.
package notify

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Func shows one notification.
type Func func(title, body string)

const (
	maxTitle = 64
	maxBody  = 240
	// minGap rate-limits notifications, so a burst of connection attempts
	// cannot flood the desktop.
	minGap = 2 * time.Second
)

// Desktop returns a Func for this system's notification center, or nil if
// it has none setu knows how to use.
func Desktop() Func {
	cmd := command()
	if cmd == nil {
		return nil
	}
	var mu sync.Mutex
	var last time.Time
	return func(title, body string) {
		mu.Lock()
		if time.Since(last) < minGap {
			mu.Unlock()
			return
		}
		last = time.Now()
		mu.Unlock()
		name, args, env := cmd(clean(title, maxTitle), clean(body, maxBody))
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, name, args...)
			c.Env = append(os.Environ(), env...)
			_ = c.Run()
		}()
	}
}

type builder func(title, body string) (name string, args, env []string)

func command() builder {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return nil
		}
		return func(title, body string) (string, []string, []string) {
			// The text arrives as `argv` of the AppleScript handler.
			return "osascript", []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body}, nil
		}
	case "windows":
		return func(title, body string) (string, []string, []string) {
			// A tray balloon via Windows Forms; the text comes from the
			// environment, so it is never parsed as PowerShell.
			script := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; ` +
				`$n = New-Object System.Windows.Forms.NotifyIcon; $n.Icon = [System.Drawing.SystemIcons]::Information; $n.Visible = $true; ` +
				`$n.ShowBalloonTip(10000, $env:SETU_NOTIFY_TITLE, $env:SETU_NOTIFY_BODY, 'Info'); Start-Sleep -Seconds 8; $n.Dispose()`
			return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script},
				[]string{"SETU_NOTIFY_TITLE=" + title, "SETU_NOTIFY_BODY=" + body}
		}
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return nil
		}
		return func(title, body string) (string, []string, []string) {
			return "notify-send", []string{"--app-name=setu", "--", title, body}, nil
		}
	}
}

// clean drops control characters and caps the length.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(strings.TrimSpace(s)); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return strings.TrimSpace(s)
}
