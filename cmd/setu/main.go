// Command setu shares a local terminal peer-to-peer over WebRTC.
//
//	setu share [flags] [--] [command [args...]]
//	setu join  [flags] <link | code>
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/tty"
)

var version = "dev"

const usage = `setu — share your terminal peer-to-peer (WebRTC), no server of your own.

Usage:
  setu share [flags] [--] [command [args...]]   share a command (default: $SHELL; PowerShell on Windows)
  setu join  [flags] <link | code>              join from another terminal
  setu desktop [flags]                          share this computer's screen, mouse, keyboard and clipboard
  setu ps                                      list background shares
  setu link <id>                                show a background share's links (+ QR)
  setu logs <id>                                show a background share's log
  setu stop <id> | --all                        stop background share(s)
  setu service ...                              always-on shares with persistent links (see 'setu service')
  setu policy                                   show the managed (MDM) policy in force on this device
  setu device [show | reset]                    this device's key fingerprint (for auto-approval)
  setu version

Examples:
  setu share -- claude                    share Claude Code; open the link on your phone
  setu share --password --once -- claude  require a password, single use link
  setu share --view-link -- htop          extra read-only link for spectators
  setu share --manual                     no relays at all: copy/paste offer & answer
  setu share -d --password -- claude      run in the background, get your shell back
  setu share --audit-endpoint https://otel.example.com -- claude
                                          send an audit log of connections and commands
  setu join 'https://…/#k=…'              join from a terminal instead of a browser
  setu desktop --password --view-link     remote desktop in the browser, plus a watch-only link

Run 'setu share -h', 'setu desktop -h' or 'setu join -h' for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	// Windows consoles need VT processing switched on for colours and TUIs.
	restoreOut := tty.EnableVT(os.Stdout)
	restoreErr := tty.EnableVT(os.Stderr)
	var err error
	code := 0
	switch os.Args[1] {
	case "share":
		code, err = runShare(os.Args[2:])
	case "join":
		code, err = runJoin(os.Args[2:])
	case "desktop":
		code, err = runDesktop(os.Args[2:])
	case "ps", "list", "ls":
		code, err = runPS()
	case "stop":
		code, err = runStop(os.Args[2:])
	case "link", "links":
		code, err = runLink(os.Args[2:])
	case "logs", "log":
		code, err = runLogs(os.Args[2:])
	case "service", "svc":
		code, err = runService(os.Args[2:])
	case "policy":
		code, err = runPolicy(os.Args[2:])
	case "device":
		code, err = runDevice(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(versionString())
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "setu:", err)
		if code == 0 {
			code = 1
		}
	}
	restoreErr()
	restoreOut()
	os.Exit(code)
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func runShare(args []string) (int, error) { return serve("share", args) }

// runDesktop shares this computer's screen, mouse, keyboard and clipboard.
func runDesktop(args []string) (int, error) { return serve("desktop", args) }

// serve runs `setu share` or `setu desktop`: the same invites, access
// control, audit and background mode, sharing a command or the desktop.
func serve(sub string, args []string) (int, error) {
	desktopMode := sub == "desktop"
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	var relays, stun listFlag
	manual := fs.Bool("manual", false, "copy/paste signaling: no relays, you paste the answer code back here")
	viewLink := fs.Bool("view-link", false, "also create a read-only invite link")
	password := fs.Bool("password", false, "require a password (prompted; or set SETU_PASSWORD)")
	maxClients := fs.Int("max-clients", 2, "maximum concurrent clients")
	once := fs.Bool("once", false, "invite becomes invalid after the first successful join")
	approve := fs.Bool("approve", false, "ask on this terminal before admitting each client (answer a to always approve that device)")
	var trustDevices listFlag
	fs.Var(&trustDevices, "trust-device", "device key fingerprint to approve without asking (repeatable; see 'setu device'). With --approve and no terminal, only these devices may join")
	expire := fs.Duration("expire", 0, "invite stops accepting new clients after this long (e.g. 30m); 0 = never")
	fs.Var(&relays, "relay", "nostr relay URL (repeatable / comma separated); default: built-in public relays")
	fs.Var(&stun, "stun", "STUN server URL(s), e.g. stun:stun.l.google.com:19302 (default Google STUN)")
	turn := fs.String("turn", "", "TURN server URL for strict NATs, e.g. turn:turn.example.com:3478")
	turnUser := fs.String("turn-user", "", "TURN username")
	webURL := fs.String("web-url", link.DefaultWebURL, "base URL of the web client (empty = print terminal join command only)")
	headless, display, fps := new(bool), new(int), new(int)
	if desktopMode {
		display = fs.Int("display", -1, "display to share, by number (0 = first); -1 = the primary display. Clients can switch displays")
		fps = fs.Int("fps", 20, "maximum frames per second (1-60)")
	} else {
		headless = fs.Bool("headless", false, "do not attach this terminal; just serve clients")
	}
	qr := fs.Bool("qr", true, "print a QR code of the invite link")
	linkFile := fs.String("link-file", "", "also write the invite link(s) to this file (mode 0600)")
	verbose := fs.Bool("verbose", false, "verbose logging")
	auditOn := fs.Bool("audit", false, "send an audit log (connections and typed commands) to an OpenTelemetry collector; see -audit-* flags")
	auditOpts := addAuditFlags(fs, "audit-", false)
	var background bool
	fs.BoolVar(&background, "background", false, "run the share in the background and return to the shell (see 'setu ps')")
	fs.BoolVar(&background, "d", false, "shorthand for --background")
	// Internal: set by --background on the detached child process.
	sessionDir := fs.String("session-dir", "", "internal: background session directory")
	passwordStdin := fs.Bool("password-stdin", false, "read the session password from the first line of stdin")
	fs.Usage = func() {
		if desktopMode {
			fmt.Fprint(os.Stderr, "Usage: setu desktop [flags]\n\nShare this computer's screen, mouse, keyboard and clipboard with a browser.\n\n")
		} else {
			fmt.Fprint(os.Stderr, "Usage: setu share [flags] [--] [command [args...]]\n\n")
		}
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nEnvironment: SETU_PASSWORD (password), SETU_TURN_PASSWORD (TURN credential),\n"+
			"  OTEL_EXPORTER_OTLP_[LOGS_]ENDPOINT / OTEL_EXPORTER_OTLP_[LOGS_]HEADERS (audit collector and its credentials)\n")
	}
	_ = fs.Parse(args)

	command := fs.Args()
	label := "remote desktop"
	switch {
	case desktopMode && len(command) > 0:
		return 2, errors.New("setu desktop takes no command (did you mean setu share?)")
	case desktopMode:
		if *fps < 1 || *fps > 60 {
			return 2, errors.New("--fps must be between 1 and 60")
		}
		if *webURL == "" {
			return 2, errors.New("setu desktop is viewed in the browser, so --web-url cannot be empty")
		}
	default:
		if len(command) == 0 {
			command = host.DefaultShell()
		}
		label = strings.Join(command, " ")
	}

	// Managed (MDM) policy: checked here and again by a background child.
	pol, err := policy.Load()
	if err != nil {
		return 1, err
	}
	if desktopMode {
		if err := pol.CheckDesktop(*manual); err != nil {
			return 1, err
		}
	} else {
		if err := pol.CheckShare(*manual); err != nil {
			return 1, err
		}
		if err := pol.CheckCommand(resolveCommand(command)); err != nil {
			return 1, err
		}
	}
	if err := pol.CheckWebURL(*webURL); err != nil {
		return 1, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *maxClients, err = pol.Clients(*maxClients, set["max-clients"]); err != nil {
		return 1, err
	}
	if *expire, err = pol.ShareExpiry(*expire); err != nil {
		return 1, err
	}
	if !*manual {
		if relays, err = pol.Relays(relays); err != nil {
			return 1, err
		}
	}
	if pol.RequirePassword {
		*password = true // prompt (or use SETU_PASSWORD) even without --password
	}

	if background {
		if *manual {
			return 1, errors.New("--background cannot be combined with --manual (the answer is pasted into this terminal)")
		}
		if *approve && len(trustDevices) == 0 {
			return 1, errors.New("--background with --approve needs --trust-device (nobody would see the prompt)")
		}
	}
	if *sessionDir != "" {
		*headless = true
		*qr = false
		*linkFile = daemon.LinksPath(*sessionDir)
	}

	stdinTTY := tty.IsTerminal(os.Stdin) && tty.IsTerminal(os.Stdout)
	mirror := !desktopMode && !*headless && stdinTTY
	for i, fp := range trustDevices {
		n, err := secure.NormalizeDeviceFingerprint(fp)
		if err != nil {
			return 1, err
		}
		trustDevices[i] = n
	}
	if len(trustDevices) > 0 && !*approve {
		return 1, errors.New("--trust-device only matters with --approve (without it every joiner with the link is admitted)")
	}
	noPrompt := *approve && (!tty.IsTerminal(os.Stdin) || *sessionDir != "")
	if noPrompt && len(trustDevices) == 0 {
		return 1, errors.New("--approve needs an interactive terminal, or --trust-device to admit known devices only")
	}
	if *manual && !tty.IsTerminal(os.Stdin) {
		return 1, errors.New("--manual needs an interactive terminal to paste the answer")
	}

	ice := rtc.DefaultICE
	if len(stun) > 0 {
		ice = []proto.ICEServer{{URLs: stun}}
	}
	if *turn != "" {
		ice = append(ice, proto.ICEServer{URLs: []string{*turn}, Username: *turnUser, Credential: os.Getenv("SETU_TURN_PASSWORD")})
	}
	if err := rtc.ValidateICE(ice); err != nil {
		return 1, err
	}

	var auditCfg audit.Config
	if *auditOn || *auditOpts.endpoint != "" {
		c, err := auditOpts.config()
		if err != nil {
			return 1, err
		}
		auditCfg = c
	} else if *auditOpts.input == audit.InputOff {
		auditCfg.Input = audit.InputOff // so a policy can refuse it
	}
	if auditCfg, err = pol.AuditConfig(auditCfg); err != nil {
		return 1, err
	}

	pw := os.Getenv("SETU_PASSWORD")
	if *passwordStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return 1, errors.New("--password-stdin: no password on stdin")
		}
		pw = strings.TrimRight(line, "\r\n")
		if pw == "" {
			return 1, errors.New("--password-stdin: empty password")
		}
	}
	if *password && pw == "" {
		var err error
		if pw, err = promptNewPassword("session", pol.MinPassword(8)); err != nil {
			return 1, err
		}
	}
	os.Unsetenv("SETU_PASSWORD") // never leak into the shared shell
	if err := pol.CheckPassword(pw != "", len(pw)); err != nil {
		return 1, err
	}

	if background {
		return startBackground(fs, sub, label, command, pw)
	}
	if *sessionDir != "" {
		cleanup, err := daemon.Register(*sessionDir, label)
		if err != nil {
			return 1, err
		}
		defer cleanup()
	}
	os.Unsetenv("SETU_TURN_PASSWORD")

	var auditLog *audit.Logger
	if auditCfg.Enabled() {
		var err error
		auditLog, err = audit.New(context.Background(), auditCfg, audit.Options{Version: versionString(), Attributes: pol.AuditAttributes()})
		if err != nil {
			return 1, err
		}
		defer auditLog.Shutdown(context.Background())
		audit.ScrubEnv() // collector credentials stay out of the shared shell
	}

	if len(relays) == 0 {
		relays = nostr.DefaultRelays
	}
	cfg := host.Config{
		Command:        command,
		Manual:         *manual,
		Relays:         relays,
		ICE:            ice,
		Password:       pw,
		MaxClients:     *maxClients,
		Once:           *once,
		Approve:        *approve,
		TrustedDevices: trustDevices,
		NoPrompt:       noPrompt,
		Expire:         *expire,
		Mirror:         mirror,
		Verbose:        *verbose,
		Audit:          auditLog,
	}
	if desktopMode {
		cfg.Command = nil
		cfg.Desktop = &host.DesktopConfig{FPS: *fps, Display: *display}
	}

	customRelays := relays
	if strings.Join(relays, ",") == strings.Join(nostr.DefaultRelays, ",") {
		customRelays = nil
	}
	var links []string
	if !*manual {
		cs, err := secure.NewSecret()
		if err != nil {
			return 1, err
		}
		cfg.ControlSecret = &cs
		if *viewLink {
			vs, err := secure.NewSecret()
			if err != nil {
				return 1, err
			}
			cfg.ViewSecret = &vs
		}
	}

	controlTitle := "\x1b[1;31mCONTROL link\x1b[0m — anyone holding it can type into this terminal. Keep it secret:"
	viewTitle := "\x1b[1;33mVIEW-ONLY link\x1b[0m — can watch, cannot type:"
	if desktopMode {
		controlTitle = "\x1b[1;31mCONTROL link\x1b[0m - anyone holding it can see and control this computer (screen, mouse, keyboard, clipboard). Keep it secret:"
		viewTitle = "\x1b[1;33mVIEW-ONLY link\x1b[0m - can watch the screen, cannot control it:"
	}
	cfg.OnInvite = func(inv host.Invite) {
		w := os.Stderr
		fmt.Fprintf(w, "\n\x1b[1msetu %s\x1b[0m — sharing \x1b[1m%s\x1b[0m peer-to-peer\n", version, label)
		if *manual {
			if desktopMode {
				fmt.Fprintln(w, "\nManual mode: send this offer link to the joiner (or the code, to paste on the web client's home page):")
			} else {
				fmt.Fprintln(w, "\nManual mode: send this offer to the joiner (open the link, or `setu join <code>`):")
			}
			if *webURL != "" {
				fmt.Fprintf(w, "\n  %s\n", link.Build(*webURL, link.Invite{Offer: inv.Code}))
			}
			fmt.Fprintf(w, "\n  code: %s\n", inv.Code)
			fmt.Fprintln(w, "\nThen paste the joiner's answer code here and press Enter:")
			return
		}
		show := func(title string, sec *secure.Secret) {
			frag := link.Build("", link.Invite{Secret: sec.String(), Relays: customRelays})
			fmt.Fprintf(w, "\n%s\n", title)
			full := ""
			if *webURL != "" {
				full = link.Build(*webURL, link.Invite{Secret: sec.String(), Relays: customRelays})
				fmt.Fprintf(w, "  browser:  %s\n", full)
				links = append(links, full)
			} else {
				links = append(links, frag)
			}
			if !desktopMode {
				fmt.Fprintf(w, "  terminal: setu join '%s'\n", frag)
			}
			if *qr && full != "" && tty.IsTerminal(os.Stderr) {
				printQR(w, full)
			}
		}
		show(controlTitle, cfg.ControlSecret)
		if cfg.ViewSecret != nil {
			show(viewTitle, cfg.ViewSecret)
		}
		var notes []string
		if pw != "" {
			notes = append(notes, "password required")
		}
		if *once {
			notes = append(notes, "single use")
		}
		if *expire > 0 {
			notes = append(notes, "expires in "+expire.String())
		}
		if *approve {
			switch {
			case noPrompt:
				notes = append(notes, fmt.Sprintf("only %d trusted device(s) may join", len(trustDevices)))
			case len(trustDevices) > 0:
				notes = append(notes, fmt.Sprintf("host approval required (%d trusted device(s) approved automatically)", len(trustDevices)))
			default:
				notes = append(notes, "host approval required")
			}
		}
		notes = append(notes, fmt.Sprintf("max %d client(s)", *maxClients))
		if auditCfg.Enabled() {
			notes = append(notes, "audited to "+collectorHost(auditCfg.Endpoint))
		}
		fmt.Fprintf(w, "\nSecurity: %s. Compare the verification code shown on both sides.\n", strings.Join(notes, ", "))
		if *linkFile != "" {
			if err := writePrivate(*linkFile, strings.Join(links, "\n")+"\n"); err != nil {
				fmt.Fprintf(w, "could not write link file: %v\n", err)
			}
		}
		if mirror {
			fmt.Fprint(w, "\nThis terminal is attached to the session. Type <Enter> ~ . to end sharing.\nPress Enter to start…")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		} else if desktopMode {
			fmt.Fprintln(w, "\nSharing this computer's desktop; press Ctrl-C to stop.")
		} else {
			fmt.Fprintln(w, "\nServing headless; Ctrl-C to stop.")
		}
	}
	if *manual {
		lines := make(chan string)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				if t := strings.TrimSpace(sc.Text()); t != "" {
					lines <- t
				}
			}
			close(lines)
		}()
		cfg.ReadAnswer = func(ctx context.Context) (string, error) {
			select {
			case l, ok := <-lines:
				if !ok {
					return "", errors.New("stdin closed")
				}
				return l, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}

	s, err := host.New(cfg)
	if err != nil {
		return 1, err
	}
	ctx, cancel := signalContext()
	defer cancel()
	code, err := s.Run(ctx)
	fmt.Fprintf(os.Stderr, "\r\n[setu] session ended (exit code %d)\n", code)
	return code, err
}

func runJoin(args []string) (int, error) {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	var relays listFlag
	fs.Var(&relays, "relay", "override nostr relay URL(s)")
	timeout := fs.Duration("timeout", 3*time.Minute, "give up connecting after this long")
	anonymous := fs.Bool("anonymous", false, "do not send device details (hostname, OS user, device id) to the host and use a one-time device key; the host still logs your network address")
	noRemember := fs.Bool("no-remember", false, "use a one-time device key kept only in memory, so hosts cannot approve this device automatically next time")
	verbose := fs.Bool("verbose", false, "verbose logging")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: setu join [flags] <link | k=… | offer code>\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return 2, nil
	}
	opt := client.Options{
		Relays:  relays,
		Verbose: *verbose,
		Timeout: *timeout,

		Password: func() (string, error) {
			if pw := os.Getenv("SETU_PASSWORD"); pw != "" {
				return pw, nil
			}
			if !tty.IsTerminal(os.Stdin) {
				return "", errors.New("host requires a password: set SETU_PASSWORD")
			}
			fmt.Fprint(os.Stderr, "Session password: ")
			pw, err := tty.ReadPassword(os.Stdin)
			fmt.Fprintln(os.Stderr)
			return pw, err
		},
		ShowAnswer: func(code string) {
			fmt.Fprintf(os.Stderr, "\nSend this answer code back to the host (they paste it into `setu share --manual`):\n\n%s\n\nWaiting for the host…\n", code)
		},
	}
	if !*anonymous {
		opt.Info = client.LocalInfo(versionString())
	}
	key, err := client.LoadDeviceKey(!*anonymous && !*noRemember)
	if err != nil {
		return 1, fmt.Errorf("device key: %w", err)
	}
	opt.DeviceKey = key
	fmt.Fprintf(os.Stderr, "This device: %s (give it to the host to approve this device automatically)\n", key.Fingerprint())
	ctx, cancel := signalContext()
	defer cancel()
	code, err := client.Join(ctx, fs.Arg(0), opt)
	if errors.Is(err, client.ErrDetached) {
		fmt.Fprintln(os.Stderr, "\r\n[setu] detached")
		return 0, nil
	}
	if err == nil {
		fmt.Fprintf(os.Stderr, "\r\n[setu] remote command exited (code %d)\n", code)
	}
	return code, err
}

// writePrivate writes a file readable only by the owner, even if it already
// existed with wider permissions.
func writePrivate(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
