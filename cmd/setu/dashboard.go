package main

import (
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/dashboard"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

// runDashboard serves the local dashboard until interrupted.
func runDashboard(args []string) (int, error) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:0", "loopback address to listen on (port 0 picks a free one)")
	noOpen := fs.Bool("no-open", false, "print the link instead of opening it in the default browser")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: setu dashboard [--addr 127.0.0.1:0] [--no-open]

Opens a local web page that shows who is connected to this computer's service
profiles and background shares, with connection analytics, and lets you
disconnect clients, stop shares, enable or disable profiles, rotate keys and
pair or remove devices. It listens on a loopback address only, until you press
Ctrl-C, and its link works once.

`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() > 0 {
		fs.Usage()
		return 2, nil
	}
	dir, err := service.Dir()
	if err != nil {
		return 1, err
	}
	if err := service.EnsureDir(dir); err != nil {
		return 1, err
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
	}
	srv, err := dashboard.New(dashboard.Options{ServiceDir: dir, Version: version, Logf: logf})
	if err != nil {
		return 1, err
	}
	ln, url, err := srv.Listen(*addr)
	if err != nil {
		return 1, err
	}
	ctx, cancel := signalContext()
	defer cancel()

	fmt.Fprintf(os.Stderr, "\n\x1b[1msetu dashboard\x1b[0m on %s\n\n", ln.Addr())
	fmt.Fprintf(os.Stderr, "Open this link in a browser on this computer. It works once, and only until you stop the dashboard:\n\n  %s\n\n", url)
	if !*noOpen {
		if cleanup, err := openBrowser(url); err != nil {
			fmt.Fprintf(os.Stderr, "(could not open a browser: %v)\n\n", err)
		} else {
			defer cleanup()
		}
	}
	fmt.Fprintln(os.Stderr, "Press Ctrl-C to stop.")
	err = srv.Serve(ctx, ln)
	fmt.Fprintln(os.Stderr, "\ndashboard stopped")
	return 0, err
}

// openBrowser opens url in the default browser without putting it on a
// command line, where other users of this computer could read it (ps):
// the browser is pointed at an owner-only file that redirects to it.
func openBrowser(url string) (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "setu-dashboard-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	page := filepath.Join(dir, "open.html")
	body := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><meta name="referrer" content="no-referrer"><meta http-equiv="refresh" content="0;url=%s"><title>setu dashboard</title><p>Opening the setu dashboard…</p>`, html.EscapeString(url))
	if err := os.WriteFile(page, []byte(body), 0o600); err != nil {
		cleanup()
		return nil, err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", page)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", page)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			cleanup()
			return nil, fmt.Errorf("no graphical session")
		}
		cmd = exec.Command("xdg-open", page)
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	// The browser reads the file within moments; do not leave the token
	// on disk longer than that.
	t := time.AfterFunc(time.Minute, cleanup)
	return func() { t.Stop(); cleanup() }, nil
}
