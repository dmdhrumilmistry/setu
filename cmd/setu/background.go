package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/tty"
	"github.com/mdp/qrterminal/v3"
)

// Flags the parent handles itself and must not forward to the background
// child (which gets -headless, -session-dir and -password-stdin instead).
var notForwarded = map[string]bool{
	"background": true, "d": true, "session-dir": true, "password-stdin": true,
	"password": true, "headless": true, "qr": true, "link-file": true,
}

// startBackground re-runs `setu share` detached from this terminal and
// prints its invite links once it is serving.
func startBackground(fs *flag.FlagSet, command []string, password string) (int, error) {
	var forwarded []string
	fs.Visit(func(f *flag.Flag) {
		if !notForwarded[f.Name] {
			forwarded = append(forwarded, "-"+f.Name+"="+f.Value.String())
		}
	})
	childArgs := func(dir string) []string {
		args := []string{"share", "-session-dir", dir}
		if password != "" {
			args = append(args, "-password-stdin")
		}
		args = append(args, forwarded...)
		return append(append(args, "--"), command...)
	}
	fmt.Fprintf(os.Stderr, "starting %q in the background…\n", strings.Join(command, " "))
	info, err := daemon.Spawn(childArgs, password, 45*time.Second)
	if err != nil {
		return 1, err
	}
	if err := printLinks(os.Stderr, info.Dir); err != nil {
		return 1, err
	}
	if password != "" {
		fmt.Fprintln(os.Stderr, "\nSecurity: password required. Compare the verification code shown on both sides.")
	}
	fmt.Fprintf(os.Stderr, `
Running in the background as %[1]s (pid %[2]d). This terminal is free; the
share keeps running after you close it.

  setu ps          list background shares
  setu link %[1]s  show these links again
  setu logs %[1]s  connection log (who joined, verification codes)
  setu stop %[1]s  end the share
`, info.ID, info.PID)
	return 0, nil
}

func printLinks(w io.Writer, dir string) error {
	b, err := os.ReadFile(daemon.LinksPath(dir))
	if err != nil {
		return fmt.Errorf("read links: %w", err)
	}
	titles := []string{
		"\x1b[1;31mCONTROL link\x1b[0m — anyone holding it can type into the shared terminal. Keep it secret:",
		"\x1b[1;33mVIEW-ONLY link\x1b[0m — can watch, cannot type:",
	}
	for i, l := range strings.Fields(string(b)) {
		if i < len(titles) {
			fmt.Fprintf(w, "\n%s\n", titles[i])
		}
		if strings.HasPrefix(l, "http") {
			fmt.Fprintf(w, "  browser:  %s\n", l)
			if j := strings.IndexByte(l, '#'); j >= 0 {
				fmt.Fprintf(w, "  terminal: setu join '%s'\n", l[j+1:])
			}
			if f, ok := w.(*os.File); ok && tty.IsTerminal(f) {
				printQR(w, l)
			}
		} else {
			fmt.Fprintf(w, "  terminal: setu join '%s'\n", l)
		}
	}
	return nil
}

func printQR(w io.Writer, s string) {
	qrterminal.GenerateWithConfig(s, qrterminal.Config{
		Level: qrterminal.L, Writer: w, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
		WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, QuietZone: 1,
	})
}

func runPS() (int, error) {
	list, err := daemon.List()
	if err != nil {
		return 1, err
	}
	if len(list) == 0 {
		fmt.Println("no background shares running (start one with `setu share -d -- <command>`)")
		return 0, nil
	}
	fmt.Printf("%-10s %-8s %-10s %s\n", "ID", "PID", "UPTIME", "COMMAND")
	for _, s := range list {
		fmt.Printf("%-10s %-8d %-10s %s\n", s.ID, s.PID, time.Since(s.Started).Round(time.Second), s.Command)
	}
	return 0, nil
}

func oneSession(args []string, cmd string) (daemon.Info, error) {
	if len(args) != 1 {
		return daemon.Info{}, fmt.Errorf("usage: setu %s <id>", cmd)
	}
	return daemon.Find(args[0])
}

func runLink(args []string) (int, error) {
	s, err := oneSession(args, "link")
	if err != nil {
		return 1, err
	}
	return 0, printLinks(os.Stdout, s.Dir)
}

func runLogs(args []string) (int, error) {
	s, err := oneSession(args, "logs")
	if err != nil {
		return 1, err
	}
	f, err := os.Open(daemon.LogPath(s.Dir))
	if err != nil {
		return 1, err
	}
	defer f.Close()
	_, err = io.Copy(os.Stdout, f)
	return 0, err
}

func runStop(args []string) (int, error) {
	if len(args) == 1 && (args[0] == "--all" || args[0] == "-all") {
		list, err := daemon.List()
		if err != nil {
			return 1, err
		}
		for _, s := range list {
			if err := daemon.Stop(s); err != nil {
				return 1, fmt.Errorf("stop %s: %w", s.ID, err)
			}
			fmt.Printf("stopped %s (%s)\n", s.ID, s.Command)
		}
		if len(list) == 0 {
			fmt.Println("nothing to stop")
		}
		return 0, nil
	}
	s, err := oneSession(args, "stop <id> | --all")
	if err != nil {
		return 1, err
	}
	if err := daemon.Stop(s); err != nil {
		return 1, err
	}
	fmt.Printf("stopped %s (%s)\n", s.ID, s.Command)
	return 0, nil
}

// versionString includes the commit the binary was built from, so it is easy
// to tell whether an installed setu is current.
func versionString() string {
	v := version
	var rev, at string
	dirty := false
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version // e.g. go install …@latest pseudo-version
		}
		for _, kv := range bi.Settings {
			switch kv.Key {
			case "vcs.revision":
				rev = kv.Value
			case "vcs.time":
				at = kv.Value
			case "vcs.modified":
				dirty = kv.Value == "true"
			}
		}
	}
	out := "setu " + v
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if dirty {
			rev += "+dirty"
		}
		out += " (" + rev + " " + at + ")"
	}
	return out + " " + runtime.GOOS + "/" + runtime.GOARCH
}
