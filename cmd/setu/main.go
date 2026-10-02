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

	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/rtc"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/tty"
	"github.com/mdp/qrterminal/v3"
)

var version = "dev"

const usage = `setu — share your terminal peer-to-peer (WebRTC), no server of your own.

Usage:
  setu share [flags] [--] [command [args...]]   share a command (default: $SHELL; PowerShell on Windows)
  setu join  [flags] <link | code>              join from another terminal
  setu version

Examples:
  setu share -- claude                    share Claude Code; open the link on your phone
  setu share --password --once -- claude  require a password, single use link
  setu share --view-link -- htop          extra read-only link for spectators
  setu share --manual                     no relays at all: copy/paste offer & answer
  setu join 'https://…/#k=…'              join from a terminal instead of a browser

Run 'setu share -h' or 'setu join -h' for flags.
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
	case "version", "--version", "-v":
		fmt.Println("setu", version)
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

func runShare(args []string) (int, error) {
	fs := flag.NewFlagSet("share", flag.ExitOnError)
	var relays, stun listFlag
	manual := fs.Bool("manual", false, "copy/paste signaling: no relays, you paste the answer code back here")
	viewLink := fs.Bool("view-link", false, "also create a read-only invite link")
	password := fs.Bool("password", false, "require a password (prompted; or set SETU_PASSWORD)")
	maxClients := fs.Int("max-clients", 2, "maximum concurrent clients")
	once := fs.Bool("once", false, "invite becomes invalid after the first successful join")
	approve := fs.Bool("approve", false, "ask on this terminal before admitting each client")
	expire := fs.Duration("expire", 0, "invite stops accepting new clients after this long (e.g. 30m); 0 = never")
	fs.Var(&relays, "relay", "nostr relay URL (repeatable / comma separated); default: built-in public relays")
	fs.Var(&stun, "stun", "STUN server URL(s), e.g. stun:stun.l.google.com:19302 (default Google STUN)")
	turn := fs.String("turn", "", "TURN server URL for strict NATs, e.g. turn:turn.example.com:3478")
	turnUser := fs.String("turn-user", "", "TURN username")
	webURL := fs.String("web-url", link.DefaultWebURL, "base URL of the web client (empty = print terminal join command only)")
	headless := fs.Bool("headless", false, "do not attach this terminal; just serve clients")
	qr := fs.Bool("qr", true, "print a QR code of the invite link")
	linkFile := fs.String("link-file", "", "also write the invite link(s) to this file (mode 0600)")
	verbose := fs.Bool("verbose", false, "verbose logging")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: setu share [flags] [--] [command [args...]]\n\n")
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nEnvironment: SETU_PASSWORD (password), SETU_TURN_PASSWORD (TURN credential)\n")
	}
	_ = fs.Parse(args)

	command := fs.Args()
	if len(command) == 0 {
		command = host.DefaultShell()
	}

	stdinTTY := tty.IsTerminal(os.Stdin) && tty.IsTerminal(os.Stdout)
	mirror := !*headless && stdinTTY
	if *approve && !tty.IsTerminal(os.Stdin) {
		return 1, errors.New("--approve needs an interactive terminal")
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

	pw := os.Getenv("SETU_PASSWORD")
	if *password && pw == "" {
		if !tty.IsTerminal(os.Stdin) {
			return 1, errors.New("--password needs a terminal to prompt, or set SETU_PASSWORD")
		}
		for {
			fmt.Fprint(os.Stderr, "Choose a session password: ")
			a, err := tty.ReadPassword(os.Stdin)
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return 1, err
			}
			fmt.Fprint(os.Stderr, "Repeat password: ")
			b, err := tty.ReadPassword(os.Stdin)
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return 1, err
			}
			if a != b {
				fmt.Fprintln(os.Stderr, "passwords do not match")
				continue
			}
			if len(a) < 8 {
				fmt.Fprintln(os.Stderr, "use at least 8 characters")
				continue
			}
			pw = a
			break
		}
	}
	os.Unsetenv("SETU_PASSWORD") // never leak into the shared shell
	os.Unsetenv("SETU_TURN_PASSWORD")

	if len(relays) == 0 {
		relays = nostr.DefaultRelays
	}
	cfg := host.Config{
		Command:    command,
		Manual:     *manual,
		Relays:     relays,
		ICE:        ice,
		Password:   pw,
		MaxClients: *maxClients,
		Once:       *once,
		Approve:    *approve,
		Expire:     *expire,
		Mirror:     mirror,
		Verbose:    *verbose,
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

	cmdLine := strings.Join(command, " ")
	cfg.OnInvite = func(inv host.Invite) {
		w := os.Stderr
		fmt.Fprintf(w, "\n\x1b[1msetu %s\x1b[0m — sharing \x1b[1m%s\x1b[0m peer-to-peer\n", version, cmdLine)
		if *manual {
			fmt.Fprintln(w, "\nManual mode: send this offer to the joiner (open the link, or `setu join <code>`):")
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
			fmt.Fprintf(w, "  terminal: setu join '%s'\n", frag)
			if *qr && full != "" && tty.IsTerminal(os.Stderr) {
				qrterminal.GenerateWithConfig(full, qrterminal.Config{
					Level: qrterminal.L, Writer: w, HalfBlocks: true,
					BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
					WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, QuietZone: 1,
				})
			}
		}
		show("\x1b[1;31mCONTROL link\x1b[0m — anyone holding it can type into this terminal. Keep it secret:", cfg.ControlSecret)
		if cfg.ViewSecret != nil {
			show("\x1b[1;33mVIEW-ONLY link\x1b[0m — can watch, cannot type:", cfg.ViewSecret)
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
			notes = append(notes, "host approval required")
		}
		notes = append(notes, fmt.Sprintf("max %d client(s)", *maxClients))
		fmt.Fprintf(w, "\nSecurity: %s. Compare the verification code shown on both sides.\n", strings.Join(notes, ", "))
		if *linkFile != "" {
			if err := writePrivate(*linkFile, strings.Join(links, "\n")+"\n"); err != nil {
				fmt.Fprintf(w, "could not write link file: %v\n", err)
			}
		}
		if mirror {
			fmt.Fprint(w, "\nThis terminal is attached to the session. Type <Enter> ~ . to end sharing.\nPress Enter to start…")
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
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
