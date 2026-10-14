package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/service"
	"github.com/dmdhrumilmistry/setu/internal/tty"
)

const serviceUsage = `setu service - always-on shares with persistent links, started at login.

Usage:
  setu service add [flags] <name> [--] command [args...]   create a profile (prints its links once)
  setu service list                                       list profiles
  setu service link <name>                                show a profile's links (+ QR)
  setu service rotate [--password] <name>                 new keys: old links stop working
  setu service enable|disable <name>                      serve / stop serving a profile
  setu service remove <name>                              delete a profile and its keys
  setu service audit [flags] | --off                      send an audit log to an OpenTelemetry collector
  setu service install [--no-start]                       start the service at login (systemd / launchd / Task Scheduler)
  setu service uninstall                                  remove the autostart entry and stop the service
  setu service status                                     autostart, supervisor and profile state
  setu service run [--dir D] [--log-file F]               run the supervisor in the foreground

The running service picks up changes within a few seconds; no restart needed.
Profiles need a password by default, because their links stay valid.
`

const servicePasswordMin = 12

func runService(args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, serviceUsage)
		return 2, nil
	}
	rest := args[1:]
	switch args[0] {
	case "add", "create":
		return serviceAdd(rest)
	case "list", "ls":
		return serviceList()
	case "link", "links":
		return serviceLink(rest)
	case "rotate":
		return serviceRotate(rest)
	case "enable", "disable":
		return serviceEnable(rest, args[0] == "enable")
	case "remove", "rm", "delete":
		return serviceRemove(rest)
	case "audit":
		return serviceAudit(rest)
	case "install":
		return serviceInstall(rest)
	case "uninstall":
		return 0, service.Uninstall()
	case "status":
		return serviceStatus()
	case "run":
		return serviceRun(rest)
	case "help", "-h", "--help":
		fmt.Print(serviceUsage)
		return 0, nil
	}
	return 2, fmt.Errorf("unknown service command %q\n\n%s", args[0], serviceUsage)
}

// serviceState loads the configuration for a read-modify-write.
type serviceState struct {
	dir, path string
	cfg       *service.Config
}

func loadService() (*serviceState, error) {
	dir, err := service.Dir()
	if err != nil {
		return nil, err
	}
	path := service.ConfigPath(dir)
	cfg, err := service.Load(path)
	if err != nil {
		return nil, err
	}
	return &serviceState{dir: dir, path: path, cfg: cfg}, nil
}

func (s *serviceState) save() error {
	if err := service.Save(s.path, s.cfg); err != nil {
		return err
	}
	if _, ok := service.RunningPID(service.LockPath(s.dir)); ok {
		fmt.Fprintln(os.Stderr, "\nSaved. The running service applies this within a few seconds.")
	} else {
		fmt.Fprintln(os.Stderr, "\nSaved. The service is not running: start it with `setu service install` (or `setu service run`).")
	}
	return nil
}

// promptNewPassword asks for a new password twice on the terminal.
func promptNewPassword(what string, minLen int) (string, error) {
	if !tty.IsTerminal(os.Stdin) {
		return "", errors.New("a password is needed: run in a terminal, or set SETU_PASSWORD")
	}
	for {
		fmt.Fprintf(os.Stderr, "Choose a %s password: ", what)
		a, err := tty.ReadPassword(os.Stdin)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Repeat password: ")
		b, err := tty.ReadPassword(os.Stdin)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if a != b {
			fmt.Fprintln(os.Stderr, "passwords do not match")
			continue
		}
		if len(a) < minLen {
			fmt.Fprintf(os.Stderr, "use at least %d characters\n", minLen)
			continue
		}
		return a, nil
	}
}

// profilePassword gets a password for a service profile from SETU_PASSWORD,
// one line of stdin, or a prompt, and returns only its verifier.
func profilePassword(fromStdin bool) (*secure.PasswordVerifier, error) {
	pw := os.Getenv("SETU_PASSWORD")
	os.Unsetenv("SETU_PASSWORD")
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return nil, errors.New("--password-stdin: no password on stdin")
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if pw == "" {
		var err error
		if pw, err = promptNewPassword("profile", servicePasswordMin); err != nil {
			return nil, err
		}
	}
	if len(pw) < servicePasswordMin {
		return nil, fmt.Errorf("profile passwords need at least %d characters (their links do not expire quickly)", servicePasswordMin)
	}
	return secure.NewPasswordVerifier(pw)
}

func serviceAdd(args []string) (int, error) {
	fs := flag.NewFlagSet("service add", flag.ExitOnError)
	var relays, stun listFlag
	viewLink := fs.Bool("view-link", false, "also create a read-only link")
	maxClients := fs.Int("max-clients", 1, "maximum concurrent clients")
	validFor := fs.Duration("valid-for", 90*24*time.Hour, "keys stop working after this long; `setu service rotate` renews them (0 = never)")
	dir := fs.String("dir", "", "working directory for the command (default: the service's, usually your home)")
	noPassword := fs.Bool("no-password", false, "allow joining with the link alone (not recommended for an always-on share)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	fs.Var(&relays, "relay", "nostr relay URL(s) (default: built-in public relays)")
	fs.Var(&stun, "stun", "STUN server URL(s) (default Google STUN)")
	turn := fs.String("turn", "", "TURN server URL for strict NATs (credential from SETU_TURN_PASSWORD)")
	turnUser := fs.String("turn-user", "", "TURN username")
	webURL := fs.String("web-url", link.DefaultWebURL, "base URL of the web client")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: setu service add [flags] <name> [--] command [args...]\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) > 1 && rest[1] == "--" {
		rest = append(rest[:1], rest[2:]...)
	}
	if len(rest) < 2 {
		fs.Usage()
		return 2, nil
	}
	name, command := rest[0], rest[1:]
	if err := service.ValidName(name); err != nil {
		return 1, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	if _, exists := st.cfg.Profiles[name]; exists {
		return 1, fmt.Errorf("profile %q already exists (use `setu service rotate` or `setu service remove`)", name)
	}
	// Resolve the command now: the service's PATH may differ, and a fixed
	// path cannot be hijacked by a directory added to PATH later.
	exe, err := exec.LookPath(command[0])
	if err != nil {
		return 1, err
	}
	if command[0], err = filepath.Abs(exe); err != nil {
		return 1, err
	}
	if *dir != "" {
		if *dir, err = filepath.Abs(*dir); err != nil {
			return 1, err
		}
	}

	now := time.Now()
	p := &service.Profile{
		Command:      command,
		Dir:          *dir,
		Enabled:      true,
		MaxClients:   *maxClients,
		ValidFor:     service.Duration(*validFor),
		Relays:       relays,
		STUN:         stun,
		TURN:         *turn,
		TURNUser:     *turnUser,
		TURNPassword: os.Getenv("SETU_TURN_PASSWORD"),
		WebURL:       *webURL,
		Created:      now.UTC(),
	}
	if !*noPassword {
		if p.Password, err = profilePassword(*passwordStdin); err != nil {
			return 1, err
		}
	}
	if err := p.NewKeys(*viewLink, now); err != nil {
		return 1, err
	}
	if err := p.Validate(); err != nil {
		return 1, err
	}
	st.cfg.Profiles[name] = p
	if err := st.save(); err != nil {
		return 1, err
	}
	printProfile(name, p)
	return 0, nil
}

func printProfile(name string, p *service.Profile) {
	fmt.Fprintf(os.Stderr, "\nProfile %s: %s\n", name, strings.Join(p.Command, " "))
	printLinkList(os.Stderr, p.Links())
	var notes []string
	if p.Password != nil {
		notes = append(notes, "password required")
	} else {
		notes = append(notes, "\x1b[1;31mno password: the link alone grants a shell\x1b[0m")
	}
	if !p.ExpiresAt.IsZero() {
		notes = append(notes, "keys expire "+p.ExpiresAt.Local().Format("2006-01-02 15:04"))
	}
	notes = append(notes, fmt.Sprintf("max %d client(s)", p.MaxClients))
	fmt.Fprintf(os.Stderr, "\nSecurity: %s. Compare the verification code shown on both sides.\n", strings.Join(notes, ", "))
}

func oneName(args []string, cmd string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: setu service %s <name>", cmd)
	}
	return args[0], nil
}

func serviceList() (int, error) {
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	if len(st.cfg.Profiles) == 0 {
		fmt.Println("no service profiles (create one with `setu service add <name> -- <command>`)")
		return 0, nil
	}
	now := time.Now()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tPASSWORD\tCLIENTS\tEXPIRES\tCOMMAND")
	for _, name := range st.cfg.Names() {
		p := st.cfg.Profiles[name]
		state := "enabled"
		switch {
		case !p.Enabled:
			state = "disabled"
		case !p.Active(now):
			state = "expired"
		}
		pw, exp := "no", "never"
		if p.Password != nil {
			pw = "yes"
		}
		if !p.ExpiresAt.IsZero() {
			exp = p.ExpiresAt.Local().Format("2006-01-02")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", name, state, pw, p.MaxClients, exp, strings.Join(p.Command, " "))
	}
	return 0, w.Flush()
}

func serviceLink(args []string) (int, error) {
	name, err := oneName(args, "link")
	if err != nil {
		return 2, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	p, err := st.cfg.Get(name)
	if err != nil {
		return 1, err
	}
	printProfile(name, p)
	return 0, nil
}

func serviceRotate(args []string) (int, error) {
	fs := flag.NewFlagSet("service rotate", flag.ExitOnError)
	newPassword := fs.Bool("password", false, "also set a new password")
	passwordStdin := fs.Bool("password-stdin", false, "read the new password from the first line of stdin")
	_ = fs.Parse(args)
	name, err := oneName(fs.Args(), "rotate")
	if err != nil {
		return 2, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	p, err := st.cfg.Get(name)
	if err != nil {
		return 1, err
	}
	if *newPassword || *passwordStdin {
		if p.Password, err = profilePassword(*passwordStdin); err != nil {
			return 1, err
		}
	}
	now := time.Now()
	if err := p.NewKeys(p.ViewKey != "", now); err != nil {
		return 1, err
	}
	p.Rotated = now.UTC()
	if err := st.save(); err != nil {
		return 1, err
	}
	fmt.Fprintln(os.Stderr, "New keys created; the old links no longer work.")
	printProfile(name, p)
	return 0, nil
}

func serviceEnable(args []string, enable bool) (int, error) {
	cmd := "disable"
	if enable {
		cmd = "enable"
	}
	name, err := oneName(args, cmd)
	if err != nil {
		return 2, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	p, err := st.cfg.Get(name)
	if err != nil {
		return 1, err
	}
	p.Enabled = enable
	if enable && !p.Active(time.Now()) {
		return 1, fmt.Errorf("profile %q has expired keys: run `setu service rotate %s`", name, name)
	}
	if err := st.save(); err != nil {
		return 1, err
	}
	return 0, nil
}

func serviceRemove(args []string) (int, error) {
	name, err := oneName(args, "remove")
	if err != nil {
		return 2, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	if _, err := st.cfg.Get(name); err != nil {
		return 1, err
	}
	delete(st.cfg.Profiles, name)
	if err := st.save(); err != nil {
		return 1, err
	}
	fmt.Fprintf(os.Stderr, "removed %s; its links no longer work\n", name)
	return 0, nil
}

func serviceAudit(args []string) (int, error) {
	fs := flag.NewFlagSet("service audit", flag.ExitOnError)
	opts := addAuditFlags(fs, "", true)
	off := fs.Bool("off", false, "stop auditing")
	headersStdin := fs.Bool("headers-stdin", false, "read collector headers (Name=value,...) from stdin instead of OTEL_EXPORTER_OTLP_HEADERS")
	noVerify := fs.Bool("no-verify", false, "save without sending a test record to the collector")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: setu service audit [flags] | --off\n\nCredentials come from OTEL_EXPORTER_OTLP_HEADERS or --headers-stdin and are stored in the\nservice config (owner-only file), never on the command line.\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	if *off {
		st.cfg.Audit = audit.Config{}
		fmt.Fprintln(os.Stderr, "auditing disabled")
		return 0, st.save()
	}
	if fs.NFlag() == 0 && st.cfg.Audit.Enabled() {
		a := st.cfg.Audit
		fmt.Printf("endpoint: %s\ninput: %s\nrequired: %v\nheaders: %d set\n", a.Endpoint, orDefault(a.Input, audit.InputLines), a.Required, len(a.Headers))
		return 0, nil
	}
	c, err := opts.config()
	if err != nil {
		return 1, err
	}
	if *headersStdin {
		h, err := readHeadersStdin()
		if err != nil {
			return 1, err
		}
		c.Headers = h
	}
	if !*noVerify {
		probe := c
		probe.Required = true
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		l, err := audit.New(ctx, probe, audit.Options{Version: versionString()})
		cancel()
		if err != nil {
			return 1, fmt.Errorf("%w\n(fix the collector settings, or save anyway with --no-verify)", err)
		}
		_ = l.Shutdown(context.Background())
		fmt.Fprintf(os.Stderr, "collector %s accepted a test record\n", collectorHost(c.Endpoint))
	}
	st.cfg.Audit = c
	return 0, st.save()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func serviceInstall(args []string) (int, error) {
	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	noStart := fs.Bool("no-start", false, "register autostart without starting the service now")
	_ = fs.Parse(args)
	dir, err := service.Dir()
	if err != nil {
		return 1, err
	}
	if err := service.EnsureDir(dir); err != nil {
		return 1, err
	}
	exe, err := os.Executable()
	if err != nil {
		return 1, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return 1, err
	}
	if strings.Contains(exe, "go-build") {
		return 1, errors.New("this binary is temporary (go run); install from a built setu binary")
	}
	u := service.Unit{Exe: exe, Dir: dir, LogFile: service.LogPath(dir)}
	if runtime.GOOS != "windows" {
		u.Path = os.Getenv("PATH")
	}
	msg, err := service.Install(u, !*noStart)
	if err != nil {
		return 1, err
	}
	fmt.Println(msg)
	if cfg, err := service.Load(service.ConfigPath(dir)); err == nil && len(cfg.Profiles) == 0 {
		fmt.Println("\nNo profiles yet: add one with `setu service add <name> -- <command>`.")
	}
	return 0, nil
}

func serviceStatus() (int, error) {
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	fmt.Println("autostart: ", service.AutostartStatus())
	if pid, ok := service.RunningPID(service.LockPath(st.dir)); ok {
		fmt.Printf("supervisor: running (pid %d)\n", pid)
	} else {
		fmt.Println("supervisor: not running")
	}
	if st.cfg.Audit.Enabled() {
		fmt.Printf("audit:      %s (required=%v)\n", collectorHost(st.cfg.Audit.Endpoint), st.cfg.Audit.Required)
	} else {
		fmt.Println("audit:      off")
	}
	fmt.Println("state dir: ", st.dir)
	fmt.Println()
	return serviceList()
}

func serviceRun(args []string) (int, error) {
	fs := flag.NewFlagSet("service run", flag.ExitOnError)
	dir := fs.String("dir", "", "service state directory (default: the per-user one)")
	logFile := fs.String("log-file", "", "append the supervisor log to this file instead of stderr")
	noConsole := fs.Bool("no-console", false, "Windows: release the console window (used by the scheduled task)")
	_ = fs.Parse(args)
	if *dir == "" {
		d, err := service.Dir()
		if err != nil {
			return 1, err
		}
		*dir = d
	}
	if err := service.EnsureDir(*dir); err != nil {
		return 1, err
	}
	out := os.Stderr
	if *logFile != "" {
		f, err := service.OpenLog(*logFile)
		if err != nil {
			return 1, err
		}
		defer f.Close()
		out = f
	}
	if *noConsole {
		service.DetachConsole()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sv := &service.Supervisor{Dir: *dir, Version: versionString(), Log: log.New(out, "", log.LstdFlags)}
	if err := sv.Run(ctx); err != nil {
		fmt.Fprintln(out, "setu service:", err)
		return 1, err
	}
	return 0, nil
}
