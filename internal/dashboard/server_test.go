package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/access"
	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

type fakeBackend struct {
	calls []string
}

func (f *fakeBackend) snapshot(context.Context) stateView {
	return stateView{Profiles: []profileView{{Name: "claude"}}, Shares: []shareView{}}
}
func (f *fakeBackend) policy() (*policy.Policy, error) { return nil, nil }
func (f *fakeBackend) setEnabled(_ *policy.Policy, name string, on bool) error {
	f.calls = append(f.calls, "enable:"+name)
	return nil
}
func (f *fakeBackend) rotate(_ *policy.Policy, name string) error {
	f.calls = append(f.calls, "rotate:"+name)
	return nil
}
func (f *fakeBackend) pair(_ *policy.Policy, name string) (string, time.Time, error) {
	f.calls = append(f.calls, "pair:"+name)
	return "ABCD-EFGH-JKLM", time.Now().Add(time.Minute), nil
}
func (f *fakeBackend) removeDevice(name, fp string) error {
	f.calls = append(f.calls, "remove:"+name+":"+fp)
	return nil
}
func (f *fakeBackend) disconnect(_ context.Context, source string, client int) error {
	f.calls = append(f.calls, "disconnect:"+source)
	return nil
}
func (f *fakeBackend) stopShare(id string) error {
	f.calls = append(f.calls, "stop:"+id)
	return nil
}
func (f *fakeBackend) shareOp(_ context.Context, source, op string, on bool) (int, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s:%s:%v", op, source, on))
	return 0, nil
}
func (f *fakeBackend) accessAdd(scope string, r access.Rule) error {
	f.calls = append(f.calls, "add:"+scope+":"+r.Action+":"+r.Describe())
	return nil
}
func (f *fakeBackend) accessRemove(scope, id string) error {
	f.calls = append(f.calls, "rm:"+scope+":"+id)
	return nil
}

type harness struct {
	t      *testing.T
	s      *Server
	be     *fakeBackend
	host   string
	origin string
	token  string
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	be := &fakeBackend{}
	h := &harness{t: t, be: be, now: time.Now()}
	s, err := New(Options{ServiceDir: t.TempDir(), Version: "v1.2.3", backend: be, now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	ln, link, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // requests go straight to the handler
	u, _ := url.Parse(link)
	h.s, h.host, h.origin, h.token = s, u.Host, "http://"+u.Host, strings.TrimPrefix(u.Fragment, "t=")
	return h
}

type reqOpt func(*http.Request)

func withHost(v string) reqOpt   { return func(r *http.Request) { r.Host = v } }
func withOrigin(v string) reqOpt { return func(r *http.Request) { r.Header.Set("Origin", v) } }
func withCSRF(v string) reqOpt   { return func(r *http.Request) { r.Header.Set("X-Setu-CSRF", v) } }
func withCookie(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: v}) }
}
func withType(v string) reqOpt { return func(r *http.Request) { r.Header.Set("Content-Type", v) } }

func (h *harness) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host = h.host
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", h.origin)
	}
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.s.Handler().ServeHTTP(w, r)
	return w
}

// login returns the session cookie and CSRF token.
func (h *harness) login() (cookie, csrf string) {
	h.t.Helper()
	w := h.do("POST", "/api/session", `{"token":"`+h.token+`"}`)
	if w.Code != 200 {
		h.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				h.t.Fatalf("weak session cookie: %+v", c)
			}
			cookie = c.Value
		}
	}
	var r struct{ CSRF string }
	json.Unmarshal(w.Body.Bytes(), &r)
	if cookie == "" || r.CSRF == "" {
		h.t.Fatal("login gave no session")
	}
	return cookie, r.CSRF
}

func TestListenLoopbackOnly(t *testing.T) {
	s, _ := New(Options{ServiceDir: t.TempDir()})
	for _, a := range []string{"0.0.0.0:0", "[::]:0", "192.168.1.10:0", "example.com:80"} {
		if ln, _, err := s.Listen(a); err == nil {
			ln.Close()
			t.Fatalf("listened on %s", a)
		}
	}
	ln, link, err := s.Listen("localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if !strings.HasPrefix(link, "http://127.0.0.1:") || !strings.Contains(link, "/#t=") {
		t.Fatalf("link %q", link)
	}
}

func TestHostHeaderAndHeaders(t *testing.T) {
	h := newHarness(t)
	// DNS rebinding: a page on another name pointed at 127.0.0.1.
	if w := h.do("GET", "/", "", withHost("attacker.example:"+strings.Split(h.host, ":")[1])); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host served: %d", w.Code)
	}
	w := h.do("GET", "/", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "setu dashboard") {
		t.Fatalf("index: %d", w.Code)
	}
	for k, want := range map[string]string{
		"Content-Security-Policy": "script-src 'self'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"Cache-Control":           "no-store",
	} {
		if !strings.Contains(w.Header().Get(k), want) {
			t.Fatalf("%s = %q", k, w.Header().Get(k))
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("CSP allows framing")
	}
	if w := h.do("GET", "/app.js", ""); w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("app.js: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestLoginIsOneTime(t *testing.T) {
	h := newHarness(t)
	if w := h.do("GET", "/api/state", ""); w.Code != 401 {
		t.Fatalf("state without a session: %d", w.Code)
	}
	if w := h.do("POST", "/api/session", `{"token":"`+h.token+`"}`, withOrigin("https://evil.example")); w.Code != 403 {
		t.Fatalf("cross-origin login: %d", w.Code)
	}
	if w := h.do("POST", "/api/session", `{"token":"`+h.token+`"}`, withType("text/plain")); w.Code != 403 {
		t.Fatalf("non-JSON login: %d", w.Code)
	}
	cookie, _ := h.login()
	if w := h.do("POST", "/api/session", `{"token":"`+h.token+`"}`); w.Code != 401 {
		t.Fatalf("token worked twice: %d", w.Code)
	}
	if w := h.do("GET", "/api/state", "", withCookie(cookie)); w.Code != 200 || !strings.Contains(w.Body.String(), `"claude"`) {
		t.Fatalf("state: %d %s", w.Code, w.Body)
	}
	if w := h.do("GET", "/api/state", "", withCookie("forged")); w.Code != 401 {
		t.Fatalf("forged cookie: %d", w.Code)
	}
}

func TestWrongTokensDisableLogin(t *testing.T) {
	h := newHarness(t)
	for range maxLoginFailures {
		h.do("POST", "/api/session", `{"token":"guess"}`)
	}
	if w := h.do("POST", "/api/session", `{"token":"`+h.token+`"}`); w.Code != 401 {
		t.Fatalf("login still works after %d wrong tokens: %d", maxLoginFailures, w.Code)
	}
}

func TestChangesNeedCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	body := `{"enabled":false}`
	path := "/api/profiles/claude/enabled"
	for name, opts := range map[string][]reqOpt{
		"no csrf":     {withCookie(cookie)},
		"wrong csrf":  {withCookie(cookie), withCSRF("x")},
		"foreign":     {withCookie(cookie), withCSRF(csrf), withOrigin("https://evil.example")},
		"no origin":   {withCookie(cookie), withCSRF(csrf), withOrigin("")},
		"form post":   {withCookie(cookie), withCSRF(csrf), withType("application/x-www-form-urlencoded")},
		"no session":  {withCSRF(csrf)},
		"cross site":  {withCookie(cookie), withCSRF(csrf), func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		"unknown key": nil,
	} {
		b := body
		if name == "unknown key" {
			opts, b = []reqOpt{withCookie(cookie), withCSRF(csrf)}, `{"enabled":false,"extra":1}`
		}
		if w := h.do("POST", path, b, opts...); w.Code < 400 {
			t.Fatalf("%s: change accepted (%d)", name, w.Code)
		}
	}
	if len(h.be.calls) != 0 {
		t.Fatalf("a refused request reached the backend: %v", h.be.calls)
	}
	if w := h.do("POST", path, body, withCookie(cookie), withCSRF(csrf)); w.Code != 200 {
		t.Fatalf("valid change: %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", "/api/profiles/../etc/enabled", body, withCookie(cookie), withCSRF(csrf)); w.Code == 200 {
		t.Fatal("path traversal in a profile name accepted")
	}
	if w := h.do("POST", "/api/profiles/Bad%20Name/enabled", body, withCookie(cookie), withCSRF(csrf)); w.Code != 400 {
		t.Fatalf("bad profile name: %d", w.Code)
	}
}

func TestActions(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	auth := []reqOpt{withCookie(cookie), withCSRF(csrf)}
	if w := h.do("POST", "/api/profiles/claude/rotate", `{"confirm":"other"}`, auth...); w.Code != 400 {
		t.Fatalf("rotate with the wrong confirmation: %d", w.Code)
	}
	if w := h.do("POST", "/api/profiles/claude/rotate", `{"confirm":"claude"}`, auth...); w.Code != 200 {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	w := h.do("POST", "/api/profiles/claude/pair", `{}`, auth...)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ABCD-EFGH-JKLM") {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}
	if w := h.do("POST", "/api/profiles/claude/devices/remove", `{"fingerprint":"aaaa-bbbb"}`, auth...); w.Code != 200 {
		t.Fatalf("remove: %d", w.Code)
	}
	if w := h.do("POST", "/api/clients/disconnect", `{"source":"profile:claude","client":0}`, auth...); w.Code != 400 {
		t.Fatalf("disconnect client 0: %d", w.Code)
	}
	if w := h.do("POST", "/api/clients/disconnect", `{"source":"profile:claude","client":2}`, auth...); w.Code != 200 {
		t.Fatalf("disconnect: %d", w.Code)
	}
	if w := h.do("POST", "/api/shares/ab/stop", `{}`, auth...); w.Code != 200 {
		t.Fatalf("stop: %d", w.Code)
	}
	if w := h.do("POST", "/api/shares/A..B/stop", `{}`, auth...); w.Code != 400 {
		t.Fatalf("bad share id: %d", w.Code)
	}
	want := "rotate:claude pair:claude remove:claude:aaaa-bbbb disconnect:profile:claude stop:ab"
	if got := strings.Join(h.be.calls, " "); got != want {
		t.Fatalf("backend calls %q, want %q", got, want)
	}
	// Every change is in the connection history.
	evs, _ := h.s.hist.Read(time.Time{})
	if len(evs) != 5 || evs[0].Kind != history.Dashboard {
		t.Fatalf("history %+v", evs)
	}
}

func TestSessionExpiry(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	h.now = h.now.Add(sessionIdle + time.Second)
	if w := h.do("GET", "/api/state", "", withCookie(cookie)); w.Code != 401 {
		t.Fatalf("idle session still works: %d", w.Code)
	}
	h2 := newHarness(t)
	cookie, csrf = h2.login()
	for range 30 { // keep it busy, past the absolute limit
		h2.now = h2.now.Add(sessionMax / 25)
		h2.do("GET", "/api/state", "", withCookie(cookie))
	}
	if w := h2.do("GET", "/api/state", "", withCookie(cookie)); w.Code != 401 {
		t.Fatalf("session outlived its maximum: %d", w.Code)
	}
	h3 := newHarness(t)
	cookie, csrf = h3.login()
	h3.do("POST", "/api/logout", `{}`, withCookie(cookie), withCSRF(csrf))
	if w := h3.do("GET", "/api/state", "", withCookie(cookie)); w.Code != 401 {
		t.Fatalf("session works after sign-out: %d", w.Code)
	}
}

// TestStateHasNoSecrets runs the real backend over a service directory with
// keys, a password, a pairing code and a device, and checks none of their
// secret parts reach the page.
func TestStateHasNoSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // no background shares
	now := time.Now()
	p := &service.Profile{Command: []string{"/bin/sh"}, Enabled: true, MaxClients: 1, WebURL: "https://example.invalid/", Created: now}
	if err := p.NewKeys(true, true, now); err != nil {
		t.Fatal(err)
	}
	if err := p.AddPersonalKey("alice", proto.RoleControl, now); err != nil {
		t.Fatal(err)
	}
	v, err := secure.NewPasswordVerifier("a long service password")
	if err != nil {
		t.Fatal(err)
	}
	p.Password = v
	if err := service.Save(service.ConfigPath(dir), &service.Config{Profiles: map[string]*service.Profile{"claude": p}}); err != nil {
		t.Fatal(err)
	}
	code, _, err := service.NewPairing(dir, "claude", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal((&localBackend{dir: dir}).snapshot(context.Background()))
	page := string(b)
	if !strings.Contains(page, `"claude"`) || !strings.Contains(page, `"alice"`) {
		t.Fatalf("state lacks the profile: %s", page)
	}
	enc := func(x []byte) string { s, _ := json.Marshal(x); return strings.Trim(string(s), `"`) }
	for what, secret := range map[string]string{
		"control key": p.ControlKey, "view key": p.ViewKey, "personal key": p.PersonalKeys["alice"].Key,
		"password salt": enc(v.Salt), "password key": enc(v.Key), "pairing code": code,
	} {
		if strings.Contains(page, secret) {
			t.Fatalf("state leaks the %s", what)
		}
	}
}

func TestAnalyze(t *testing.T) {
	now := time.Date(2026, 3, 10, 15, 30, 0, 0, time.Local)
	at := func(d time.Duration) time.Time { return now.Add(-d) }
	evs := []history.Event{
		{Time: at(10 * 24 * time.Hour), Kind: history.Connect, Name: "alice"}, // previous period
		{Time: at(50 * time.Hour), Kind: history.Connect, Name: "alice", Role: "control", DeviceKey: "k1", Source: "profile:a"},
		{Time: at(49 * time.Hour), Kind: history.Disconnect, Name: "alice", Seconds: 3600},
		{Time: at(2 * time.Hour), Kind: history.Connect, Name: "shared-view-link", Role: "view", Source: "share:x"},
		{Time: at(time.Hour), Kind: history.Disconnect, Name: "shared-view-link", Seconds: 600},
		{Time: at(time.Hour), Kind: history.AuthFailure},
		{Time: at(time.Hour), Kind: history.Lockout},
		{Time: at(30 * time.Minute), Kind: history.Reconnect, Name: "alice"},
		{Time: at(20 * time.Minute), Kind: history.Rejected, Reason: "blocked: device aaaa-bbbb-cccc-dddd-eeee"},
		{Time: at(time.Minute), Kind: history.SessionStart},
	}
	v := analyze(evs, ranges["7d"], now)
	tt := v.Totals
	if tt.Connections != 2 || tt.Previous != 1 || tt.View != 1 || tt.Control != 1 || tt.People != 1 || tt.Devices != 1 ||
		tt.Seconds != 4200 || tt.MedianSeconds != 2100 || tt.AuthFailures != 1 || tt.Lockouts != 1 || tt.Reconnects != 1 || tt.Rejected != 1 || tt.Blocked != 1 {
		t.Fatalf("totals %+v", tt)
	}
	if len(v.Series) != 7 || v.Series[6].View != 1 || v.Series[6].Failures != 1 || v.Series[4].Control != 1 {
		t.Fatalf("series %+v", v.Series)
	}
	if v.Who[0].Name != "alice" || !v.Who[0].Personal || v.Who[1].Personal || v.Who[0].Seconds != 3600 {
		t.Fatalf("who %+v", v.Who)
	}
	if len(v.Events) != 8 || v.Events[0].Kind != history.Rejected {
		t.Fatalf("events should be newest first without session starts: %+v", v.Events)
	}
	if h := ranges["24h"]; len(analyze(evs, h, now).Series) != 24 {
		t.Fatal("24h range is not hourly")
	}
}

func TestAccessAndShareActions(t *testing.T) {
	h := newHarness(t)
	cookie, csrf := h.login()
	auth := []reqOpt{withCookie(cookie), withCSRF(csrf)}
	w := h.do("POST", "/api/access/add", `{"scope":"profile:claude","kind":"device","value":"3F2A91C077DE0B4EA1C9","action":"block","for":"24h","note":"lost phone"}`, auth...)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "3f2a-91c0-77de-0b4e-a1c9") {
		t.Fatalf("add rule: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]string{
		"bad scope":    `{"scope":"profile:../x","kind":"person","value":"bob","action":"block"}`,
		"bad kind":     `{"scope":"profile:*","kind":"user","value":"bob","action":"block"}`,
		"bad duration": `{"scope":"profile:*","kind":"person","value":"bob","action":"block","for":"5y"}`,
		"every ip":     `{"scope":"profile:*","kind":"network","value":"0.0.0.0/0","action":"block"}`,
	} {
		if w := h.do("POST", "/api/access/add", body, auth...); w.Code != 400 {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if w := h.do("POST", "/api/access/remove", `{"scope":"share:ab12","id":"00ff"}`, auth...); w.Code != 200 {
		t.Fatalf("remove rule: %d", w.Code)
	}
	for _, a := range []string{"pause", "resume", "disconnect_all", "terminate"} {
		if w := h.do("POST", "/api/shares/action", `{"source":"profile:claude","action":"`+a+`"}`, auth...); w.Code != 200 {
			t.Fatalf("%s: %d %s", a, w.Code, w.Body)
		}
	}
	if w := h.do("POST", "/api/shares/action", `{"source":"share:ab12","action":"terminate"}`, auth...); w.Code != 400 {
		t.Fatalf("terminate a background share: %d", w.Code)
	}
	if w := h.do("POST", "/api/shares/action", `{"source":"profile:*","action":"pause"}`, auth...); w.Code != 400 {
		t.Fatalf("pause every profile at once: %d", w.Code)
	}
	if w := h.do("POST", "/api/shares/action", `{"source":"profile:claude","action":"reboot"}`, auth...); w.Code != 400 {
		t.Fatalf("unknown action: %d", w.Code)
	}
	if w := h.do("POST", "/api/people/disconnect", `{"name":"alice"}`, auth...); w.Code != 200 {
		t.Fatalf("disconnect person: %d", w.Code)
	}
	want := []string{
		"add:profile:claude:block:device 3f2a-91c0-77de-0b4e-a1c9", "enforce:profile:claude:false",
		"rm:share:ab12:00ff", "enforce:share:ab12:false",
		"pause:profile:claude:true", "pause:profile:claude:false", "disconnect_all:profile:claude:false", "terminate:profile:claude:false",
	}
	if got := strings.Join(h.be.calls, " | "); got != strings.Join(want, " | ") {
		t.Fatalf("backend calls:\n%s\nwant:\n%s", got, strings.Join(want, " | "))
	}
}
