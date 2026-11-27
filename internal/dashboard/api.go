package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/control"
	"github.com/dmdhrumilmistry/setu/internal/daemon"
	"github.com/dmdhrumilmistry/setu/internal/history"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/policy"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

// stateView is everything the live pages show, polled every few seconds.
// It must never hold a secret (see the package comment).
type stateView struct {
	Version  string        `json:"version"`
	Now      time.Time     `json:"now"`
	Service  serviceView   `json:"service"`
	Policy   policyView    `json:"policy"`
	Profiles []profileView `json:"profiles"`
	Shares   []shareView   `json:"shares"`
}

type serviceView struct {
	Running     bool   `json:"running"`
	PID         int    `json:"pid,omitempty"`
	ConfigError string `json:"config_error,omitempty"`
	// LiveError: the service runs but its control socket did not answer
	// (an older version, say), so live sessions are unknown.
	LiveError string `json:"live_error,omitempty"`
}

type policyView struct {
	Managed bool   `json:"managed"`
	Source  string `json:"source,omitempty"`
	Error   string `json:"error,omitempty"`
}

type profileView struct {
	Name          string       `json:"name"`
	Label         string       `json:"label"`
	Kind          string       `json:"kind"`
	Dir           string       `json:"dir,omitempty"`
	Enabled       bool         `json:"enabled"`
	Expired       bool         `json:"expired"`
	Serving       bool         `json:"serving"`
	Connected     int          `json:"connected"`
	ExpiresAt     time.Time    `json:"expires_at,omitzero"`
	Auth          string       `json:"auth"`
	People        []personView `json:"people"`
	SharedControl bool         `json:"shared_control"`
	SharedView    bool         `json:"shared_view"`
	MaxClients    int          `json:"max_clients"`
	IdleTimeout   string       `json:"idle_timeout,omitempty"`
	Recording     bool         `json:"recording"`
	Uploads       bool         `json:"uploads"`
	DeviceIdle    string       `json:"device_idle,omitempty"`
	Problem       string       `json:"problem,omitempty"`
	Devices       []deviceView `json:"devices"`
	PairingUntil  time.Time    `json:"pairing_until,omitzero"`
}

type personView struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type deviceView struct {
	Fingerprint string    `json:"fingerprint"`
	Name        string    `json:"name,omitempty"`
	For         string    `json:"for"`
	How         string    `json:"how"`
	Added       time.Time `json:"added"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
}

type shareView struct {
	Source string       `json:"source"` // "profile:<name>" or "share:<id>"
	Type   string       `json:"type"`   // "profile" or "background"
	Name   string       `json:"name"`
	PID    int          `json:"pid,omitempty"`
	Status *host.Status `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// backend is where the dashboard reads state and makes changes; tests
// replace it.
type backend interface {
	snapshot(ctx context.Context) stateView
	policy() (*policy.Policy, error)
	setEnabled(pol *policy.Policy, name string, on bool) error
	rotate(pol *policy.Policy, name string) error
	pair(pol *policy.Policy, name string) (string, time.Time, error)
	removeDevice(name, fp string) error
	disconnect(ctx context.Context, source string, client int) error
	stopShare(id string) error
}

func (s *Server) state(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	st := s.be.snapshot(ctx)
	st.Version, st.Now = s.opt.Version, s.opt.now()
	writeJSON(w, st)
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request, _ *session) {
	rng, ok := ranges[r.URL.Query().Get("range")]
	if !ok {
		rng = ranges["7d"]
	}
	now := s.opt.now()
	evs, err := s.hist.Read(now.Add(-2 * rng.span))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("connection history: %w", err))
		return
	}
	writeJSON(w, analyze(evs, rng, now))
}

var (
	nameRE    = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	shareIDRE = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
)

func profileName(r *http.Request) (string, error) {
	n := r.PathValue("name")
	if !nameRE.MatchString(n) {
		return "", errors.New("That is not a profile name.")
	}
	return n, nil
}

// change runs a profile change with the managed policy loaded fresh. An
// unreadable policy refuses every change (fail closed), as the CLI does.
func (s *Server) change(w http.ResponseWriter, r *http.Request, what string, fn func(pol *policy.Policy, name string) (any, error)) {
	name, err := profileName(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	pol, err := s.be.policy()
	if err != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("managed policy: %w", err))
		return
	}
	out, err := fn(pol, name)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.record("profile:"+name, what)
	if out == nil {
		out = map[string]any{}
	}
	writeJSON(w, out)
}

// record logs a change in the terminal and the connection history.
func (s *Server) record(source, what string) {
	s.opt.Logf("dashboard: %s (%s)", what, source)
	_ = s.hist.Append(history.Event{Kind: history.Dashboard, Source: source, Reason: what})
}

func (s *Server) setEnabled(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	what := "profile disabled"
	if req.Enabled {
		what = "profile enabled"
	}
	s.change(w, r, what, func(pol *policy.Policy, name string) (any, error) {
		return nil, s.be.setEnabled(pol, name, req.Enabled)
	})
}

func (s *Server) rotate(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The page asks for the name to be typed; checking it here too means a
	// stray request cannot rotate the wrong profile's keys.
	if req.Confirm != r.PathValue("name") {
		writeErr(w, http.StatusBadRequest, errors.New("Type the profile's name to confirm."))
		return
	}
	s.change(w, r, "keys rotated: every old link stopped working", func(pol *policy.Policy, name string) (any, error) {
		return nil, s.be.rotate(pol, name)
	})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request, _ *session) {
	if err := readJSON(r, &struct{}{}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.change(w, r, "pairing code created", func(pol *policy.Policy, name string) (any, error) {
		code, exp, err := s.be.pair(pol, name)
		if err != nil {
			return nil, err
		}
		// The code is shown once, in this response only; the host keeps
		// just its verifier.
		return map[string]any{"code": code, "expires": exp}, nil
	})
}

func (s *Server) removeDevice(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.change(w, r, "device "+req.Fingerprint+" removed", func(_ *policy.Policy, name string) (any, error) {
		return nil, s.be.removeDevice(name, req.Fingerprint)
	})
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Source string `json:"source"`
		Client int    `json:"client"`
	}
	if err := readJSON(r, &req); err != nil || req.Client <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("The request was malformed."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.be.disconnect(ctx, req.Source, req.Client); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.record(req.Source, fmt.Sprintf("client #%d disconnected", req.Client))
	writeJSON(w, map[string]any{})
}

func (s *Server) stopShare(w http.ResponseWriter, r *http.Request, _ *session) {
	if err := readJSON(r, &struct{}{}); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := r.PathValue("id")
	if !shareIDRE.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("That is not a background share id."))
		return
	}
	if err := s.be.stopShare(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.record("share:"+id, "background share stopped")
	writeJSON(w, map[string]any{})
}

// localBackend is the real one: the service directory, the background
// share registry and their control sockets.
type localBackend struct {
	dir  string
	logf func(string, ...any)
}

func (b *localBackend) policy() (*policy.Policy, error) { return policy.Load() }

func (b *localBackend) snapshot(ctx context.Context) stateView {
	st := stateView{Profiles: []profileView{}, Shares: []shareView{}}
	pol, perr := policy.Load()
	if perr != nil {
		st.Policy.Error = perr.Error()
	} else {
		st.Policy.Managed, st.Policy.Source = pol.Managed(), pol.Source()
	}

	live := map[string]*host.Status{}
	if pid, ok := service.RunningPID(service.LockPath(b.dir)); ok {
		st.Service.Running, st.Service.PID = true, pid
		resp, err := control.Call(ctx, b.dir, control.Request{Op: control.OpStatus})
		if err != nil {
			st.Service.LiveError = "the service did not report its sessions: " + err.Error()
		}
		for i := range resp.Shares {
			live[resp.Shares[i].Profile] = &resp.Shares[i]
		}
	}

	cfg, err := service.Load(service.ConfigPath(b.dir))
	if err != nil {
		st.Service.ConfigError = err.Error()
		cfg = &service.Config{}
	}
	devs, derr := service.LoadDevices(b.dir)
	now := time.Now()
	for _, name := range cfg.Names() {
		p := cfg.Profiles[name]
		pv := profileView{
			Name: name, Label: p.Label(), Kind: proto.KindTerminal, Dir: p.Dir, Enabled: p.Enabled,
			Expired: !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt), ExpiresAt: p.ExpiresAt, Auth: p.AuthMode(),
			People: []personView{}, SharedControl: p.ControlKey != "", SharedView: p.ViewKey != "", MaxClients: p.MaxClients,
			Recording: p.RecordDir != "", Uploads: !p.NoUpload, Devices: []deviceView{},
		}
		if p.Desktop != nil {
			pv.Kind = proto.KindDesktop
		}
		if p.IdleTimeout > 0 {
			pv.IdleTimeout = time.Duration(p.IdleTimeout).String()
		}
		for _, person := range p.People() {
			pv.People = append(pv.People, personView{Name: person, Role: p.PersonalKeys[person].Role})
		}
		switch {
		case perr != nil:
			pv.Problem = "the managed policy cannot be read, so the service serves nothing"
		default:
			if err := p.Validate(); err != nil {
				pv.Problem = err.Error()
			} else if err := p.CheckPolicy(pol, now); err != nil && p.Enabled {
				pv.Problem = err.Error()
			}
			if d := pol.DeviceIdle(time.Duration(p.DeviceIdle)); d > 0 {
				pv.DeviceIdle = d.String()
			}
		}
		if ls := live[name]; ls != nil {
			pv.Serving, pv.Connected = true, len(ls.Clients)
		}
		if derr == nil {
			if pd := devs.Profiles[name]; pd != nil {
				for _, fp := range pd.Fingerprints() {
					d := pd.Devices[fp]
					pv.Devices = append(pv.Devices, deviceView{Fingerprint: fp, Name: d.Name, For: service.BindingLabel(d.Binding), How: d.How, Added: d.Added, LastSeen: d.LastSeen})
				}
				if pd.ActivePairing(now) != nil {
					pv.PairingUntil = pd.Pairing.ExpiresAt
				}
			}
		} else if pv.Problem == "" {
			pv.Problem = "devices: " + derr.Error()
		}
		st.Profiles = append(st.Profiles, pv)
	}

	names := make([]string, 0, len(live))
	for n := range live {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st.Shares = append(st.Shares, shareView{Source: "profile:" + n, Type: "profile", Name: n, Status: live[n]})
	}

	bg, err := daemon.List()
	if err != nil {
		st.Shares = append(st.Shares, shareView{Type: "background", Error: "background shares: " + err.Error()})
	}
	for _, info := range bg {
		sv := shareView{Source: "share:" + info.ID, Type: "background", Name: info.ID, PID: info.PID}
		resp, err := control.Call(ctx, info.Dir, control.Request{Op: control.OpStatus})
		switch {
		case err != nil:
			sv.Error = "did not report its state (started by an older setu?): " + err.Error()
			sv.Status = &host.Status{Command: info.Command, Started: info.Started, Clients: []host.ClientStatus{}}
		case len(resp.Shares) == 1:
			sv.Status = &resp.Shares[0]
		}
		st.Shares = append(st.Shares, sv)
	}
	return st
}

func (b *localBackend) setEnabled(pol *policy.Policy, name string, on bool) error {
	return service.SetEnabled(b.dir, pol, name, on)
}

func (b *localBackend) rotate(pol *policy.Policy, name string) error {
	return service.RotateKeys(b.dir, pol, name)
}

func (b *localBackend) pair(pol *policy.Policy, name string) (string, time.Time, error) {
	return service.StartPairing(b.dir, pol, name, 0)
}

func (b *localBackend) removeDevice(name, fp string) error {
	return service.RemoveDevice(b.dir, name, fp)
}

func (b *localBackend) disconnect(ctx context.Context, source string, client int) error {
	kind, name, _ := strings.Cut(source, ":")
	switch {
	case kind == "profile" && nameRE.MatchString(name):
		_, err := control.Call(ctx, b.dir, control.Request{Op: control.OpDisconnect, Profile: name, Client: client})
		return err
	case kind == "share" && shareIDRE.MatchString(name):
		info, err := daemon.Find(name)
		if err != nil {
			return err
		}
		if info.ID != name {
			return errors.New("There is no background share with that id.")
		}
		_, err = control.Call(ctx, info.Dir, control.Request{Op: control.OpDisconnect, Client: client})
		return err
	}
	return errors.New("There is no such share.")
}

func (b *localBackend) stopShare(id string) error {
	info, err := daemon.Find(id)
	if err != nil {
		return err
	}
	if info.ID != id {
		return errors.New("There is no background share with that id.") // no prefix matches from the page
	}
	return daemon.Stop(info)
}
