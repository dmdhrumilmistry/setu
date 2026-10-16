package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/pion/webrtc/v4"
)

// File upload: a control client opens a data channel labelled
// proto.UploadChannelLabel for each file and speaks this protocol on it:
//
//	client -> {t:"upload", name, size}
//	host   -> {t:"upload-ready"} | {t:"error", msg}
//	client -> binary chunks (size bytes in all)
//	host   -> {t:"upload-done", name, sha256} | {t:"error", msg}
//
// The file is written to UploadDir under a sanitized name that never
// replaces an existing file, readable only by the host user; anything
// unfinished is deleted.
const (
	// MaxUploadSize caps one file.
	MaxUploadSize    = 2 << 30
	maxUploadsActive = 2 // per client
	maxUploadName    = 200
)

// DefaultUploadDir is ~/Downloads/setu.
func DefaultUploadDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads", "setu"), nil
}

type upload struct {
	p       *peer
	dc      *webrtc.DataChannel
	mu      sync.Mutex
	f       *os.File
	path    string
	name    string
	size    int64
	written int64
	hash    hashWriter
	done    bool
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

// onDataChannel accepts upload channels from ready control clients; any
// other channel a client opens is closed.
func (p *peer) onDataChannel(dc *webrtc.DataChannel) {
	if dc.Label() != proto.UploadChannelLabel {
		dc.Close()
		return
	}
	u := &upload{p: p, dc: dc, hash: sha256.New()}
	dc.OnMessage(u.onMessage)
	dc.OnClose(func() { u.finish(errors.New("upload channel closed")) })
}

func (u *upload) reply(c proto.Control) {
	b, _ := json.Marshal(c)
	_ = u.dc.SendText(string(b))
}

func (u *upload) fail(msg string) {
	u.reply(proto.Control{T: proto.CtlError, Msg: msg})
	u.finish(errors.New(msg))
	u.dc.Close()
}

func (u *upload) onMessage(m webrtc.DataChannelMessage) {
	u.mu.Lock()
	started, done := u.f != nil, u.done
	u.mu.Unlock()
	if done {
		return
	}
	if m.IsString {
		var c proto.Control
		if started || len(m.Data) > maxInputFrame || json.Unmarshal(m.Data, &c) != nil || c.T != proto.CtlUpload {
			u.fail("unexpected message")
			return
		}
		if err := u.start(c.Name, c.Size); err != nil {
			u.fail(err.Error())
			return
		}
		u.reply(proto.Control{T: proto.CtlUploadReady})
		return
	}
	if !started {
		u.fail("file data before the upload header")
		return
	}
	u.mu.Lock()
	if u.written+int64(len(m.Data)) > u.size {
		u.mu.Unlock()
		u.fail("more data than announced")
		return
	}
	_, err := u.f.Write(m.Data)
	u.hash.Write(m.Data)
	u.written += int64(len(m.Data))
	complete := u.written == u.size
	u.mu.Unlock()
	if err != nil {
		u.fail("could not write the file on the remote computer: " + err.Error())
		return
	}
	if complete {
		if err := u.complete(); err != nil {
			u.fail(err.Error())
		}
	}
}

// start checks the request and reserves the destination file.
func (u *upload) start(name string, size int64) error {
	p := u.p
	s := p.s
	if !p.isReady() || p.role != proto.RoleControl {
		return errors.New("only control clients can upload files")
	}
	if s.cfg.UploadDir == "" {
		return errors.New("file upload is turned off on this host")
	}
	if size < 0 || size > MaxUploadSize {
		return fmt.Errorf("files may be at most %d MiB", MaxUploadSize>>20)
	}
	clean := SanitizeFileName(name)
	if !p.startUpload() {
		return fmt.Errorf("at most %d uploads at a time", maxUploadsActive)
	}
	f, path, err := createUnique(s.cfg.UploadDir, clean)
	if err != nil {
		p.endUpload()
		return fmt.Errorf("could not create the file on the remote computer: %w", err)
	}
	u.mu.Lock()
	u.f, u.path, u.name, u.size = f, path, filepath.Base(path), size
	u.mu.Unlock()
	if size == 0 {
		return u.complete()
	}
	return nil
}

func (u *upload) complete() error {
	u.mu.Lock()
	f := u.f
	u.mu.Unlock()
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	u.mu.Lock()
	u.f, u.done = nil, true
	sum := hex.EncodeToString(u.hash.Sum(nil))
	name, path, size := u.name, u.path, u.size
	u.mu.Unlock()
	u.p.endUpload()
	s := u.p.s
	s.audit.FileUpload(u.p.auditClient(), name, path, size, sum)
	s.logf("client #%d uploaded %s (%d bytes, sha256 %s)", u.p.id, path, size, sum)
	u.reply(proto.Control{T: proto.CtlUploadDone, Name: path, Text: sum})
	return nil
}

// finish removes an unfinished file.
func (u *upload) finish(reason error) {
	u.mu.Lock()
	f, path, done := u.f, u.path, u.done
	u.f, u.done = nil, true
	u.mu.Unlock()
	if done || f == nil {
		return
	}
	f.Close()
	os.Remove(path)
	u.p.endUpload()
	u.p.s.logf("client #%d: upload of %s abandoned (%v)", u.p.id, filepath.Base(path), reason)
}

func (p *peer) startUpload() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.uploads >= maxUploadsActive {
		return false
	}
	p.uploads++
	return true
}

func (p *peer) endUpload() {
	p.mu.Lock()
	p.uploads--
	p.mu.Unlock()
}

// createUnique creates dir/name (mode 0600), or "name (1).ext", "name
// (2).ext"... if it exists: an upload never replaces a file.
func createUnique(dir, name string) (*os.File, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; i < 1000; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		if filepath.Dir(path) != filepath.Clean(dir) {
			return nil, "", errors.New("bad file name")
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("too many files with that name")
}

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeFileName turns a name the client chose into a safe base name:
// no directories, no control or bidi characters, no characters Windows
// forbids, no reserved device names, no leading dots, at most
// maxUploadName bytes (keeping the extension).
func SanitizeFileName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(strings.TrimSpace(b.String()), ".")
	if name == "" {
		name = "upload"
	}
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if windowsReserved[strings.ToUpper(stem)] {
		name = "_" + name
	}
	if len(name) > maxUploadName {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		cut := maxUploadName - len(ext)
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = name[:cut] + ext
	}
	if runtime.GOOS == "windows" {
		name = strings.TrimRight(name, " .")
	}
	return name
}

// uploadsAllowed tells a client with "ready" whether it may upload.
func (s *Session) uploadsAllowed(role string) bool {
	return s.cfg.UploadDir != "" && role == proto.RoleControl
}
