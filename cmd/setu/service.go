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
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/service"
	"github.com/dmdhrumilmistry/setu/internal/tty"
)

const serviceUsage = `setu service - always-on shares with persistent links, started at login.

Usage:
  setu service add [flags] <name> [--] command [args...]   create a profile (prints its links once)
  setu service add --desktop [flags] <name>               an always-on remote desktop profile
  setu service list                                       list profiles
  setu service link <name> [person]                       show a profile's links (+ QR)
  setu service key add [--view] <name> <person>           give one person their own link (audited by name)
  setu service key list <name>                            list the people holding personal keys
  setu service key remove <name> <person>                 revoke one person's link
  setu service rotate [--password] <name>                 new keys: every old link stops working
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
	case "key", "keys":
		return serviceKey(rest)
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
	pol       *policy.Policy
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
	pol, err := policy.Load()
	if err != nil {
		return nil, err
	}
	return &serviceState{dir: dir, path: path, cfg: cfg, pol: pol}, nil
}

// save writes the configuration. The named profiles (the ones this command
// changed) must comply with the managed policy if they are enabled; the
// service enforces the same check before serving anything.
func (s *serviceState) save(changed ...string) error {
	for _, name := range changed {
		if p := s.cfg.Profiles[name]; p != nil && p.Enabled {
			if err := p.CheckPolicy(s.pol, time.Now()); err != nil {
				return fmt.Errorf("profile %s: %w", name, err)
			}
		}
	}
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
func profilePassword(fromStdin bool, minLen int) (*secure.PasswordVerifier, error) {
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
		if pw, err = promptNewPassword("profile", minLen); err != nil {
			return nil, err
		}
	}
	if len(pw) < minLen {
		return nil, fmt.Errorf("profile passwords need at least %d characters (their links do not expire quickly)", minLen)
	}
	return secure.NewPasswordVerifier(pw)
}

func serviceAdd(args []string) (int, error) {
	fs := flag.NewFlagSet("service add", flag.ExitOnError)
	var relays, stun listFlag
	viewLink := fs.Bool("view-link", false, "also create a shared read-only link")
	var people listFlag
	fs.Var(&people, "person", "give this person their own control link, so the audit log names them (repeatable / comma separated)")
	noShared := fs.Bool("no-shared-key", false, "no shared control link: only people added with --person (or 'setu service key add') can type")
	maxClients := fs.Int("max-clients", 1, "maximum concurrent clients")
	validFor := fs.Duration("valid-for", 90*24*time.Hour, "keys stop working after this long; 'setu service rotate' renews them (0 = never)")
	dir := fs.String("dir", "", "working directory for the command (default: the service's, usually your home)")
	noPassword := fs.Bool("no-password", false, "allow joining with the link alone (not recommended for an always-on share)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	fs.Var(&relays, "relay", "nostr relay URL(s) (default: built-in public relays)")
	fs.Var(&stun, "stun", "STUN server URL(s) (default Google STUN)")
	turn := fs.String("turn", "", "TURN server URL for strict NATs (credential from SETU_TURN_PASSWORD)")
	turnUser := fs.String("turn-user", "", "TURN username")
	webURL := fs.String("web-url", link.DefaultWebURL, "base URL of the web client")
	recordDir := fs.String("record-dir", "", "record each session's terminal output to a new file in this folder (asciicast v2)")
	idleTimeout := fs.Duration("idle-timeout", 0, "disconnect control clients after this long without input (e.g. 15m); 0 = never")
	noUpload := fs.Bool("no-upload", false, "do not accept file uploads from clients (they go to ~/Downloads/setu otherwise)")
	desktopMode := fs.Bool("desktop", false, "share this computer's desktop (screen, mouse, keyboard, clipboard) instead of a command")
	display := fs.Int("display", -1, "with --desktop: display to share, by number (0 = first); -1 = the primary display")
	fps := fs.Int("fps", 0, "with --desktop: maximum frames per second (1-60; 0 = default)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: setu service add [flags] <name> [--] command [args...]\n       setu service add --desktop [flags] <name>\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) > 1 && rest[1] == "--" {
		rest = append(rest[:1], rest[2:]...)
	}
	if (*desktopMode && len(rest) != 1) || (!*desktopMode && len(rest) < 2) {
		fs.Usage()
		return 2, nil
	}
	name, command := rest[0], rest[1:]
	if *desktopMode && (*dir != "" || *fps < 0 || *fps > 60 || *display < -1) {
		return 2, errors.New("--desktop takes no --dir, --fps must be 1-60 and --display -1 or a display number")
	}
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
	if err := st.pol.CheckService(); err != nil {
		return 1, err
	}
	if *desktopMode {
		if err := st.pol.CheckDesktop(false); err != nil {
			return 1, err
		}
	}
	// Under a managed policy, unset options default to what it allows.
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if st.pol.RequirePersonalKeys {
		if len(people) == 0 {
			return 1, &policy.Violation{Key: "RequirePersonalKeys", Msg: "name the people who may connect with --person"}
		}
		*noShared = true
	}
	if max := st.pol.MaxKeyValidity.D(); max > 0 && !set["valid-for"] {
		*validFor = min(*validFor, max)
	}
	if *idleTimeout, err = st.pol.IdleTimeout(*idleTimeout, set["idle-timeout"]); err != nil {
		return 1, err
	}
	if *maxClients, err = st.pol.Clients(*maxClients, set["max-clients"]); err != nil {
		return 1, err
	}
	// Resolve the command now: the service's PATH may differ, and a fixed
	// path cannot be hijacked by a directory added to PATH later.
	if !*desktopMode {
		exe, err := exec.LookPath(command[0])
		if err != nil {
			return 1, err
		}
		if command[0], err = filepath.Abs(exe); err != nil {
			return 1, err
		}
	}
	if *dir != "" {
		if *dir, err = filepath.Abs(*dir); err != nil {
			return 1, err
		}
	}
	if *recordDir != "" {
		if *recordDir, err = filepath.Abs(*recordDir); err != nil {
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
		NoUpload:     *noUpload,
		IdleTimeout:  service.Duration(*idleTimeout),
		RecordDir:    *recordDir,
		Created:      now.UTC(),
	}
	if *desktopMode {
		p.Command = nil
		p.Desktop = &service.DesktopProfile{FPS: *fps, Display: *display}
		if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
			// The service starts without the graphical session's environment.
			p.Desktop.X11Display, p.Desktop.XAuthority = os.Getenv("DISPLAY"), os.Getenv("XAUTHORITY")
			if os.Getenv("XDG_SESSION_TYPE") == "wayland" {
				return 1, errors.New("--desktop: Wayland sessions are not supported yet; log in with an X11 session (for example \"Ubuntu on Xorg\") and add the profile from there")
			}
			if p.Desktop.X11Display == "" {
				return 1, errors.New("--desktop: DISPLAY is not set; run this in a terminal inside your graphical (X11) session, so the service knows which display to share")
			}
		}
	}
	if !*noPassword {
		if p.Password, err = profilePassword(*passwordStdin, st.pol.MinPassword(servicePasswordMin)); err != nil {
			return 1, err
		}
	}
	if *noShared && len(people) == 0 {
		return 1, errors.New("--no-shared-key needs at least one --person")
	}
	if err := p.NewKeys(!*noShared, *viewLink, now); err != nil {
		return 1, err
	}
	for _, person := range people {
		if err := p.AddPersonalKey(person, proto.RoleControl, now); err != nil {
			return 1, err
		}
	}
	if err := p.Validate(); err != nil {
		return 1, err
	}
	st.cfg.Profiles[name] = p
	if err := st.save(name); err != nil {
		return 1, err
	}
	printProfile(name, p, "")
	if *desktopMode {
		printDesktopServiceNotes()
	}
	return 0, nil
}

// printDesktopServiceNotes says what each system needs before an
// always-on desktop profile works.
func printDesktopServiceNotes() {
	exe, _ := os.Executable()
	var note string
	switch runtime.GOOS {
	case "darwin":
		note = "On macOS the service runs setu itself (" + exe + "), not your terminal: when it starts, allow\n" +
			"\"setu\" under System Settings > Privacy & Security > Screen & System Audio Recording and Accessibility.\n" +
			"macOS ties these permissions to this exact binary, so allow them again after updating setu."
	case "windows":
		note = "The service runs in your session after you log in. Windows lets no program see or control the\n" +
			"lock screen or UAC prompts, so a locked session cannot be used remotely: keep it logged in and unlocked."
	default:
		note = "The service shares display " + os.Getenv("DISPLAY") + " (captured now). It needs an X11 session: Wayland is not supported yet,\n" +
			"and the session must be logged in while you connect."
	}
	fmt.Fprintf(os.Stderr, "\nRemote desktop: %s\nStart serving with `setu service install` (or `setu service run`).\n", note)
}

// printProfile prints a profile's links (only person's, if person is set)
// and its security settings.
func printProfile(name string, p *service.Profile, person string) {
	fmt.Fprintf(os.Stderr, "\nProfile %s: %s\n", name, p.Label())
	for _, l := range p.Links() {
		if person != "" && l.Person != person {
			continue
		}
		var title string
		switch {
		case l.Person != "" && l.Role == proto.RoleControl:
			title = fmt.Sprintf("\x1b[1;31mCONTROL link for %s\x1b[0m - give it only to %s; their commands are logged under that name:", l.Person, l.Person)
		case l.Person != "":
			title = fmt.Sprintf("\x1b[1;33mVIEW-ONLY link for %s\x1b[0m - can watch, cannot type:", l.Person)
		case l.Role == proto.RoleControl:
			title = "\x1b[1;31mSHARED CONTROL link\x1b[0m - anyone holding it can type; the audit log cannot tell holders apart:"
		default:
			title = "\x1b[1;33mSHARED VIEW-ONLY link\x1b[0m - can watch, cannot type:"
		}
		printLink(os.Stderr, title, l.URL)
	}
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", name, state, pw, p.MaxClients, exp, p.Label())
	}
	return 0, w.Flush()
}

func serviceLink(args []string) (int, error) {
	if len(args) < 1 || len(args) > 2 {
		return 2, errors.New("usage: setu service link <name> [person]")
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	p, err := st.cfg.Get(args[0])
	if err != nil {
		return 1, err
	}
	person := ""
	if len(args) == 2 {
		person = args[1]
		if _, ok := p.PersonalKeys[person]; !ok {
			return 1, fmt.Errorf("%s has no personal key on %s (see `setu service key list %s`)", person, args[0], args[0])
		}
	}
	printProfile(args[0], p, person)
	return 0, nil
}

func serviceKey(args []string) (int, error) {
	usage := errors.New("usage: setu service key add [--view] <name> <person> | key list <name> | key remove <name> <person>")
	if len(args) == 0 {
		return 2, usage
	}
	fs := flag.NewFlagSet("service key "+args[0], flag.ExitOnError)
	view := fs.Bool("view", false, "the person can watch but not type")
	_ = fs.Parse(args[1:])
	rest := fs.Args()
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	switch args[0] {
	case "add":
		if len(rest) != 2 {
			return 2, usage
		}
		p, err := st.cfg.Get(rest[0])
		if err != nil {
			return 1, err
		}
		role := proto.RoleControl
		if *view {
			role = proto.RoleView
		}
		if err := p.AddPersonalKey(rest[1], role, time.Now()); err != nil {
			return 1, err
		}
		if err := p.Validate(); err != nil {
			return 1, err
		}
		if err := st.save(rest[0]); err != nil {
			return 1, err
		}
		printProfile(rest[0], p, rest[1])
		return 0, nil
	case "remove", "rm", "revoke":
		if len(rest) != 2 {
			return 2, usage
		}
		p, err := st.cfg.Get(rest[0])
		if err != nil {
			return 1, err
		}
		if _, ok := p.PersonalKeys[rest[1]]; !ok {
			return 1, fmt.Errorf("%s has no personal key on %s", rest[1], rest[0])
		}
		delete(p.PersonalKeys, rest[1])
		if err := p.Validate(); err != nil {
			return 1, fmt.Errorf("cannot remove the last key: %w (remove the profile instead)", err)
		}
		if err := st.save(); err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stderr, "revoked %s's link to %s\n", rest[1], rest[0])
		return 0, nil
	case "list", "ls":
		if len(rest) != 1 {
			return 2, usage
		}
		p, err := st.cfg.Get(rest[0])
		if err != nil {
			return 1, err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PERSON\tROLE\tCREATED")
		if p.ControlKey != "" {
			fmt.Fprintln(w, "(shared link)\tcontrol\t-")
		}
		if p.ViewKey != "" {
			fmt.Fprintln(w, "(shared link)\tview\t-")
		}
		for _, person := range p.People() {
			k := p.PersonalKeys[person]
			fmt.Fprintf(w, "%s\t%s\t%s\n", person, k.Role, k.Created.Local().Format("2006-01-02"))
		}
		return 0, w.Flush()
	}
	return 2, usage
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
		if p.Password, err = profilePassword(*passwordStdin, st.pol.MinPassword(servicePasswordMin)); err != nil {
			return 1, err
		}
	}
	now := time.Now()
	if err := p.Rotate(now); err != nil {
		return 1, err
	}
	p.Rotated = now.UTC()
	if err := st.save(name); err != nil {
		return 1, err
	}
	fmt.Fprintln(os.Stderr, "New keys created for everyone; the old links no longer work.")
	printProfile(name, p, "")
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
	if err := st.save(name); err != nil {
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
	pol, err := policy.Load()
	if err != nil {
		return 1, err
	}
	if err := pol.CheckService(); err != nil {
		return 1, err
	}
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
	if st.pol.Managed() {
		fmt.Printf("policy:     %s (hash %s; see `setu policy`)\n", st.pol.Source(), st.pol.Hash())
	}
	effective, err := st.pol.AuditConfig(st.cfg.Audit)
	if err != nil {
		effective, _ = st.pol.AuditConfig(audit.Config{})
	}
	switch {
	case st.pol.AuditEndpoint != "":
		fmt.Printf("audit:      %s (required=%v, enforced by policy)\n", collectorHost(effective.Endpoint), effective.Required)
	case effective.Enabled():
		fmt.Printf("audit:      %s (required=%v)\n", collectorHost(effective.Endpoint), effective.Required)
	default:
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
