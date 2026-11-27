// Package control is the local channel between the processes that host
// shares (the service supervisor, background shares) and `setu dashboard`.
//
// Each hosting process listens on a Unix domain socket inside its own
// owner-only (0700) state directory, so only the same OS user can reach it
// (the same boundary that protects config.json and the invite links next to
// it). Where the OS reports the peer's credentials (Linux, macOS), a
// connection from another user is refused as well. The socket is never a
// network listener.
//
// The protocol is one JSON request and one JSON response per connection,
// with a deadline and a size limit. It carries no secrets: status holds
// what host.Status holds, and the only action is disconnecting a client,
// which the same user can already do by stopping the share.
package control

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/host"
)

// Operations.
const (
	OpStatus        = "status"         // every share's host.Status
	OpDisconnect    = "disconnect"     // one client (Client)
	OpDisconnectAll = "disconnect_all" // every client of the share
	OpPause         = "pause"          // stop (On) or resume admitting new clients
	OpTerminate     = "terminate"      // end the share; the service restarts a profile
	OpEnforce       = "enforce"        // apply changed block/allow lists now
)

// SocketName is the socket's file name in a state directory.
const SocketName = "control.sock"

// maxPath is below every platform's limit on a socket path (104 bytes on
// macOS, 108 on Linux and Windows).
const maxPath = 100

const (
	ioTimeout   = 5 * time.Second
	maxRequest  = 4 << 10
	maxResponse = 4 << 20
)

// Request asks a hosting process for something.
type Request struct {
	Op      string `json:"op"`
	Profile string `json:"profile,omitempty"` // which share, for the service
	Client  int    `json:"client,omitempty"`
	On      bool   `json:"on,omitempty"`
}

// Response answers a Request.
type Response struct {
	Error  string        `json:"error,omitempty"`
	Shares []host.Status `json:"shares,omitempty"`
	Count  int           `json:"count,omitempty"` // clients disconnected
}

// Handler serves requests: Shares for OpStatus, Session for the others
// (profile is "" for a background share). Every action is logged by the
// hosting process, so its own log says what the dashboard did.
type Handler interface {
	Shares() []host.Status
	Session(profile string) (*host.Session, error)
}

// pointerName is a file in the state directory naming the socket, when
// the socket had to live elsewhere because the directory's path is too long
// for one.
const pointerName = "control.path"

// Listen creates the control socket for the state directory dir: in dir
// itself, or, if that path is too long for a socket (a deep home
// directory), in a private per-user directory under the system temporary
// directory, recorded in dir/control.path. A stale socket left by a process
// that died is replaced; a live one is refused.
func Listen(dir string) (net.Listener, error) {
	path := filepath.Join(dir, SocketName)
	_ = os.Remove(filepath.Join(dir, pointerName))
	if len(path) > maxPath {
		short, err := shortDir()
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256([]byte(dir))
		path = filepath.Join(short, hex.EncodeToString(sum[:8])+".sock")
		if len(path) > maxPath {
			return nil, fmt.Errorf("no usable control socket path (%s is too long)", path)
		}
	}
	ln, err := listen(path)
	if err != nil {
		return nil, err
	}
	if path != filepath.Join(dir, SocketName) {
		if err := os.WriteFile(filepath.Join(dir, pointerName), []byte(path), 0o600); err != nil {
			ln.Close()
			return nil, err
		}
	}
	return ln, nil
}

// SocketPath is where the control socket of state directory dir is.
func SocketPath(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, pointerName)); err == nil && len(b) > 0 && len(b) <= maxPath {
		return string(b)
	}
	return filepath.Join(dir, SocketName)
}

func listen(path string) (net.Listener, error) {
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 && st.Mode().IsRegular() && st.Size() > 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another process is already listening on %s", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// The directory already keeps other users out; this is belt and braces
	// (and a no-op on Windows, where the directory's ACL does the work).
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// Serve answers connections on ln until ctx ends, then closes ln.
func Serve(ctx context.Context, ln net.Listener, h Handler, logf func(string, ...any)) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil && logf != nil && !errors.Is(err, net.ErrClosed) {
				logf("control socket: %v", err)
			}
			return
		}
		go handle(c, h, logf)
	}
}

func handle(c net.Conn, h Handler, logf func(string, ...any)) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(ioTimeout))
	if err := checkPeer(c); err != nil {
		if logf != nil {
			logf("control socket: refused a connection: %v", err)
		}
		return
	}
	var req Request
	dec := json.NewDecoder(io.LimitReader(c, maxRequest))
	dec.DisallowUnknownFields()
	var resp Response
	if err := dec.Decode(&req); err != nil {
		resp.Error = "malformed request"
	} else {
		resp = serve(req, h, logf)
	}
	_ = json.NewEncoder(c).Encode(resp)
}

func serve(req Request, h Handler, logf func(string, ...any)) Response {
	if req.Op == OpStatus {
		return Response{Shares: h.Shares()}
	}
	switch req.Op {
	case OpDisconnect, OpDisconnectAll, OpPause, OpTerminate, OpEnforce:
	default:
		return Response{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
	s, err := h.Session(req.Profile)
	if err != nil {
		return Response{Error: err.Error()}
	}
	what := req.Op
	if req.Profile != "" {
		what += " on profile " + req.Profile
	}
	if logf != nil && req.Op != OpEnforce {
		logf("dashboard: %s", what)
	}
	const reason = "disconnected from the dashboard"
	switch req.Op {
	case OpDisconnect:
		if req.Client <= 0 {
			return Response{Error: "no client given"}
		}
		if err := s.Disconnect(req.Client, reason); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{Count: 1}
	case OpDisconnectAll:
		return Response{Count: s.DisconnectAll(reason)}
	case OpPause:
		s.Pause(req.On)
	case OpTerminate:
		if err := s.Terminate("ended from the dashboard"); err != nil {
			return Response{Error: err.Error()}
		}
	case OpEnforce:
		return Response{Count: s.Enforce()}
	}
	return Response{}
}

// ErrNotRunning means nothing is listening on the socket.
var ErrNotRunning = errors.New("not running")

// Call sends req to the control socket of state directory dir and returns
// the response. An error the process reported comes back as an error too.
func Call(ctx context.Context, dir string, req Request) (Response, error) {
	path := SocketPath(dir)
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isRefused(err) {
			return Response{}, ErrNotRunning
		}
		return Response{}, err
	}
	defer c.Close()
	deadline := time.Now().Add(ioTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.SetDeadline(deadline)
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(io.LimitReader(c, maxResponse))).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("control socket: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}
