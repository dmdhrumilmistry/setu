// Package dashboard is `setu dashboard`: a local web page for watching and
// managing this computer's service profiles and background shares, with
// analytics from the connection history.
//
// It is a remote-shell control panel, so it is built to be reachable only by
// the person at this computer:
//
//   - It listens on a loopback address only, on a random port by default,
//     and only while the command runs. Nothing is always listening.
//   - Requests must name the address it listens on in their Host header,
//     which defeats DNS rebinding (a web page that points its own name at
//     127.0.0.1).
//   - The link it prints carries a 256-bit one-time token in its fragment,
//     which browsers never send to a server or put in a Referer. The page
//     trades it for a session cookie (HttpOnly, SameSite=Strict) and drops
//     it from the address bar. The token works once, so a link copied from
//     scrollback or history is useless; repeated wrong tokens shut login off.
//   - Every change needs the session's CSRF token in a header, a JSON body
//     and a same-origin Origin, and the page is served with a strict Content
//     Security Policy, no framing, no referrer and no caching.
//   - It shows no secret: no invite link, key, password verifier, resume
//     token or pairing code other than one it has just created. Links stay
//     a terminal command (`setu service link`, `setu link`).
//   - Changes go through the same code and the same managed-policy checks as
//     the CLI, and each is logged in the terminal and the connection history.
package dashboard

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

//go:embed static
var staticFS embed.FS

const (
	cookieName = "setu_dashboard"
	// sessionIdle and sessionMax bound a browser session. The page polls
	// every few seconds, so idling means the tab is closed or asleep.
	sessionIdle = 30 * time.Minute
	sessionMax  = 12 * time.Hour
	// maxLoginFailures wrong tokens turn login off for the rest of the run:
	// a 256-bit token is not guessed, so this is someone probing.
	maxLoginFailures = 5
	maxBody          = 16 << 10
)

// Options configure a Server.
type Options struct {
	// ServiceDir is the service state directory (service.Dir()).
	ServiceDir string
	Version    string
	// Logf receives one line per notable event (logins, every change).
	Logf func(format string, args ...any)

	// Tests.
	now      func() time.Time
	backend  backend
	noOrigin bool
}

// Server is one run of the dashboard.
type Server struct {
	opt   Options
	hist  *history.Store
	be    backend
	token string // one-time login token ("" once used or disabled)

	mu       sync.Mutex
	sessions map[[32]byte]*session
	failures int
	allowed  map[string]bool // Host header values that name this server
	origin   map[string]bool // Origin header values of this server
}

type session struct {
	csrf    string
	created time.Time
	seen    time.Time
}

// New prepares a server. Call Listen, then Serve.
func New(opt Options) (*Server, error) {
	if opt.ServiceDir == "" {
		return nil, errors.New("no service directory")
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.now == nil {
		opt.now = time.Now
	}
	s := &Server{opt: opt, hist: history.Open(opt.ServiceDir), token: randomToken(), sessions: map[[32]byte]*session{}}
	s.be = opt.backend
	if s.be == nil {
		s.be = &localBackend{dir: opt.ServiceDir, logf: opt.Logf}
	}
	return s, nil
}

// Listen binds addr, which must be a loopback address ("" picks a random
// port on 127.0.0.1). It returns the listener and the login URL.
func (s *Server) Listen(addr string) (net.Listener, string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, "", fmt.Errorf("dashboard address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, "", fmt.Errorf("the dashboard listens on loopback addresses only (127.0.0.1 or ::1), not %q", host)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), port))
	if err != nil {
		return nil, "", err
	}
	bound := ln.Addr().(*net.TCPAddr)
	hostport := net.JoinHostPort(bound.IP.String(), fmt.Sprint(bound.Port))
	s.mu.Lock()
	s.allowed = map[string]bool{hostport: true, net.JoinHostPort("localhost", fmt.Sprint(bound.Port)): true}
	s.origin = map[string]bool{}
	for h := range s.allowed {
		s.origin["http://"+h] = true
	}
	s.mu.Unlock()
	return ln, "http://" + hostport + "/#t=" + s.token, nil
}

// Serve answers requests on ln until ctx ends.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          nil,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Handler is the whole HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.HandleFunc("GET /{$}", s.asset(static, "index.html"))
	for _, f := range []string{"app.js", "style.css", "icon.svg"} {
		mux.HandleFunc("GET /"+f, s.asset(static, f))
	}
	mux.HandleFunc("POST /api/session", s.login)
	mux.HandleFunc("GET /api/session", s.authed(false, s.sessionInfo))
	mux.HandleFunc("POST /api/logout", s.authed(true, s.logout))
	mux.HandleFunc("GET /api/state", s.authed(false, s.state))
	mux.HandleFunc("GET /api/analytics", s.authed(false, s.analytics))
	mux.HandleFunc("POST /api/profiles/{name}/enabled", s.authed(true, s.setEnabled))
	mux.HandleFunc("POST /api/profiles/{name}/rotate", s.authed(true, s.rotate))
	mux.HandleFunc("POST /api/profiles/{name}/pair", s.authed(true, s.pair))
	mux.HandleFunc("POST /api/profiles/{name}/devices/remove", s.authed(true, s.removeDevice))
	mux.HandleFunc("POST /api/clients/disconnect", s.authed(true, s.disconnect))
	mux.HandleFunc("POST /api/shares/{id}/stop", s.authed(true, s.stopShare))
	return s.guard(mux)
}

// guard applies to every request: the Host check and security headers.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), usb=(), serial=()")
		s.mu.Lock()
		ok := s.allowed[r.Host]
		s.mu.Unlock()
		if !ok {
			// A page on another site resolving its name to 127.0.0.1
			// (DNS rebinding) arrives with its own name here.
			http.Error(w, "unexpected Host header", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) asset(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := mime.TypeByExtension(path.Ext(name))
		switch path.Ext(name) {
		case ".js":
			ct = "text/javascript; charset=utf-8"
		case ".html":
			ct = "text/html; charset=utf-8"
		case ".css":
			ct = "text/css; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
	}
}

// login trades the one-time token for a session.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if err := s.sameOrigin(r); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.mu.Lock()
	tok := s.token
	good := tok != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(tok)) == 1
	if !good {
		s.failures++
		fails := s.failures
		if fails >= maxLoginFailures && s.token != "" {
			s.token = ""
			s.mu.Unlock()
			s.opt.Logf("WARNING: %d wrong dashboard tokens: login is now off; restart `setu dashboard` for a new link", fails)
		} else {
			s.mu.Unlock()
		}
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, errors.New("This dashboard link was already used."))
		} else {
			writeErr(w, http.StatusUnauthorized, errors.New("This dashboard link is wrong or has expired."))
		}
		return
	}
	s.token = "" // one use
	id, csrf := randomToken(), randomToken()
	now := s.opt.now()
	s.sessions[hashID(id)] = &session{csrf: csrf, created: now, seen: now}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.opt.Logf("dashboard opened in a browser (the link no longer works)")
	writeJSON(w, map[string]any{"csrf": csrf, "version": s.opt.Version})
}

// sessionInfo hands a reloaded page its CSRF token again. Another site
// cannot read it: the response is same-origin only (no CORS).
func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request, sess *session) {
	writeJSON(w, map[string]any{"csrf": sess.csrf, "version": s.opt.Version})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess *session) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.mu.Lock()
		delete(s.sessions, hashID(c.Value))
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.opt.Logf("dashboard signed out")
	writeJSON(w, map[string]any{})
}

type authedHandler func(http.ResponseWriter, *http.Request, *session)

// authed requires a live session and, for changes, the CSRF header and a
// same-origin request.
func (s *Server) authed(change bool, h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, errors.New("You are not signed in."))
			return
		}
		now := s.opt.now()
		key := hashID(c.Value)
		s.mu.Lock()
		sess := s.sessions[key]
		if sess != nil && (now.Sub(sess.seen) > sessionIdle || now.Sub(sess.created) > sessionMax) {
			delete(s.sessions, key)
			sess = nil
		}
		if sess != nil {
			sess.seen = now
		}
		s.mu.Unlock()
		if sess == nil {
			writeErr(w, http.StatusUnauthorized, errors.New("This dashboard session has ended."))
			return
		}
		if change {
			if err := s.sameOrigin(r); err != nil {
				writeErr(w, http.StatusForbidden, err)
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setu-CSRF")), []byte(sess.csrf)) != 1 {
				writeErr(w, http.StatusForbidden, errors.New("The request was refused: missing or wrong CSRF token."))
				return
			}
		}
		h(w, r, sess)
	}
}

// sameOrigin requires a JSON request whose Origin (sent by every browser
// on a POST) is this server.
func (s *Server) sameOrigin(r *http.Request) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return errors.New("The request was refused: it must be JSON.")
	}
	if s.opt.noOrigin {
		return nil
	}
	o := r.Header.Get("Origin")
	s.mu.Lock()
	ok := s.origin[o]
	s.mu.Unlock()
	if !ok {
		return errors.New("The request was refused: it came from another origin.")
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return errors.New("The request was refused: it came from another site.")
	}
	return nil
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("The request was malformed.")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func randomToken() string { return base64.RawURLEncoding.EncodeToString(secure.RandomBytes(32)) }

// hashID keys sessions by a hash of the cookie, so the map holds nothing
// that could be replayed as a cookie and lookups do not leak timing.
func hashID(id string) [32]byte { return sha256.Sum256([]byte(id)) }
