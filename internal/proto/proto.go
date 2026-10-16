// Package proto defines the wire messages exchanged during signaling and on
// the WebRTC data channel. The JavaScript client in web/app.js mirrors these.
package proto

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Version of the data channel protocol. Adding optional fields (such as
// Control.Client) does not change it: both sides ignore unknown fields.
const Version = 1

// DataChannelLabel is the label of the reliable, ordered control channel.
const DataChannelLabel = "setu"

// ScreenChannelLabel is the second channel a desktop share opens: binary
// screen updates (see internal/desktop), host to client only, so a large
// frame never delays control messages.
const ScreenChannelLabel = "setu-screen"

// UploadChannelLabel is the label of the data channel a control client
// opens for each file it uploads (see internal/host/upload.go).
const UploadChannelLabel = "setu-upload"

// What a session shares (Control.Kind in "ready").
const (
	KindTerminal = "terminal"
	KindDesktop  = "desktop"
)

// Roles a client can be granted. The role is decided by which invite secret
// the client used, never by anything the client claims.
const (
	RoleControl = "control" // may type into the terminal
	RoleView    = "view"    // read-only
)

// Signal message types.
const (
	SigHello  = "hello"  // client -> host: I want to join
	SigOffer  = "offer"  // host -> client: SDP offer + ICE servers
	SigAnswer = "answer" // client -> host: SDP answer
	SigReject = "reject" // host -> client: not accepted (reason)
)

// ICEServer mirrors RTCIceServer.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Signal is the (encrypted, over nostr) or (plain, copy/paste) signaling
// envelope.
type Signal struct {
	Type   string      `json:"type"`
	SID    string      `json:"sid"`
	SDP    string      `json:"sdp,omitempty"`
	ICE    []ICEServer `json:"ice,omitempty"`
	TS     int64       `json:"ts"`
	Reason string      `json:"reason,omitempty"`
	// Resume (hello only) names the resume ticket a reconnecting client
	// holds (secure.ResumeID of its token), so the host admits it even when
	// the share is full or no longer accepts new clients. It is only a
	// hint: the token itself is checked on the data channel.
	Resume string `json:"resume,omitempty"`
}

// Control message types (text frames on the data channel). Binary frames are
// raw terminal bytes: host->client output, client->host keystrokes.
const (
	CtlHello  = "hello"  // host -> client: protocol version + auth challenge
	CtlAuth   = "auth"   // client -> host: challenge response
	CtlReady  = "ready"  // host -> client: authenticated, stream starts
	CtlResize = "resize" // client -> host: terminal size
	CtlError  = "error"  // host -> client: fatal error, channel closes
	CtlExit   = "exit"   // host -> client: command exited
	CtlInfo   = "info"   // host -> client: informational notice
	CtlPing   = "ping"   // client -> host: liveness probe (hosts that issue resume tokens answer)
	CtlPong   = "pong"   // host -> client: answer to ping
	// CtlVisibility (client -> host) says whether the client's page is in
	// the background (Hidden). Browsers slow or freeze background pages,
	// so the host stops expecting pings and keeps the session resumable
	// for longer once such a client's connection drops.
	CtlVisibility = "visibility"
)

// ErrResume is the "error" message a host sends when a resume token is not
// (or no longer) valid. The client should forget the token and join afresh.
const ErrResume = "this session can no longer be resumed"

// Desktop control message types.
const (
	CtlScreen        = "screen"        // host -> client: displays and the shared one
	CtlAck           = "ack"           // client -> host: frame drawn
	CtlRefresh       = "refresh"       // client -> host: repaint everything
	CtlQuality       = "quality"       // client -> host: image quality
	CtlPointer       = "pointer"       // client -> host: mouse position and buttons (control)
	CtlWheel         = "wheel"         // client -> host: scroll notches (control)
	CtlKey           = "key"           // client -> host: physical key down/up (control)
	CtlText          = "text"          // client -> host: text to type (control)
	CtlClipboard     = "clipboard"     // both ways: clipboard text, in parts (control)
	CtlClipboardGet  = "clipboard-get" // client -> host: send me the host clipboard (control)
	CtlSelectDisplay = "display"       // client -> host: share another display (control)

	// On an upload channel.
	CtlUpload      = "upload"       // client -> host: {name, size}
	CtlUploadReady = "upload-ready" // host -> client: send the data
	CtlUploadDone  = "upload-done"  // host -> client: {name: saved path, text: sha256}
)

// DisplayInfo describes one of the host's displays.
type DisplayInfo struct {
	Name    string `json:"name"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Primary bool   `json:"primary,omitempty"`
}

// Auth methods announced in the hello.
const (
	AuthNone     = "none"
	AuthPassword = "password"
)

// Control is the JSON control frame. Byte slices are base64 (std) encoded.
type Control struct {
	T     string `json:"t"`
	V     int    `json:"v,omitempty"`
	Auth  string `json:"auth,omitempty"`
	Salt  []byte `json:"salt,omitempty"`
	Iter  int    `json:"iter,omitempty"`
	Nonce []byte `json:"nonce,omitempty"`
	Proof []byte `json:"proof,omitempty"`
	Role  string `json:"role,omitempty"`
	Cols  int    `json:"cols,omitempty"`
	Rows  int    `json:"rows,omitempty"`
	Cmd   string `json:"cmd,omitempty"`
	Host  string `json:"host,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Code  *int   `json:"code,omitempty"`

	// Client describes the joining device (sent with "auth"). It is
	// self-reported: the host records it for auditing but never uses it
	// for access decisions.
	Client *ClientInfo `json:"client,omitempty"`

	// DeviceKey (65-byte uncompressed P-256 point) and DeviceSig (P1363
	// signature over secure.DeviceTranscript) prove which device is
	// joining (sent with "auth"). Unlike Client, they are verified; hosts
	// use them to approve known devices automatically.
	DeviceKey []byte `json:"device_key,omitempty"`
	DeviceSig []byte `json:"device_sig,omitempty"`

	// Resume is a resume token: sent by the host with "ready", and back by
	// a reconnecting client with "auth" instead of a password proof. It is
	// single use; every "ready" carries a fresh one.
	Resume []byte `json:"resume,omitempty"`
	// Pos is a position in the terminal output stream (bytes since the
	// command started). In "ready" it is the position of the first output
	// byte that follows; in a resuming "auth", the position the client has
	// received up to, so the host sends only what it missed.
	Pos *int64 `json:"pos,omitempty"`
	// Resumed (sent with "ready") means the output continues exactly at
	// the client's position; otherwise the client should clear its screen
	// before the replay.
	Resumed bool `json:"resumed,omitempty"`
	// Hidden (sent with "visibility"): the client is in the background.
	Hidden bool `json:"hidden,omitempty"`

	// Approval tells the client how it was admitted (sent with "ready"):
	// ApprovalTrusted, ApprovalPrompt, or empty when no approval applied.
	Approval string `json:"approval,omitempty"`

	// Kind is what the session shares (sent with "ready"); empty means a
	// terminal, for hosts that predate desktop sharing.
	Kind string `json:"kind,omitempty"`
	// OS is the host's operating system (GOOS) for desktop shares, so a
	// client can map keys between Mac and PC keyboards.
	OS string `json:"os,omitempty"`

	// Desktop fields. Coordinates are pixels of the shared display.
	Displays []DisplayInfo `json:"displays,omitempty"`
	Display  *int          `json:"display,omitempty"`
	X        int           `json:"x,omitempty"`
	Y        int           `json:"y,omitempty"`
	Buttons  int           `json:"buttons,omitempty"` // PointerEvent.buttons
	DX       int           `json:"dx,omitempty"`
	DY       int           `json:"dy,omitempty"`
	Key      string        `json:"key,omitempty"` // KeyboardEvent.code
	Down     bool          `json:"down,omitempty"`
	Text     string        `json:"text,omitempty"`
	More     bool          `json:"more,omitempty"` // clipboard: more parts follow
	Frame    uint32        `json:"frame,omitempty"`
	Quality  string        `json:"quality,omitempty"`

	// Uploads: Upload (with "ready") says this client may upload files; Name
	// and Size describe a file on an upload channel.
	Upload bool   `json:"upload,omitempty"`
	Name   string `json:"name,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// How a client was admitted when the host requires approval.
const (
	ApprovalTrusted = "trusted" // a known device, approved automatically
	ApprovalPrompt  = "prompt"  // the host answered the prompt
)

const maxInfoField = 256

// Sanitized returns a copy safe to log and print: control characters
// (terminal escape sequences, newlines that could forge log lines) are
// removed, fields are length capped, and the device id is restricted to
// [A-Za-z0-9_-]. A nil or empty info returns nil.
func (c *ClientInfo) Sanitized() *ClientInfo {
	if c == nil {
		return nil
	}
	clean := func(s string, max int) string {
		var b strings.Builder
		n := 0
		for _, r := range strings.TrimSpace(s) {
			if n >= max {
				break
			}
			if unicode.IsControl(r) || r == utf8.RuneError || unicode.Is(unicode.Bidi_Control, r) {
				continue
			}
			b.WriteRune(r)
			n++
		}
		return b.String()
	}
	id := clean(c.DeviceID, 64)
	for _, r := range id {
		if !(r == '-' || r == '_' || unicode.IsDigit(r) || (r < 128 && unicode.IsLetter(r))) {
			id = ""
			break
		}
	}
	out := &ClientInfo{
		App: clean(c.App, 32), Version: clean(c.Version, 128), DeviceID: id,
		OS: clean(c.OS, 64), OSVersion: clean(c.OSVersion, 64), Arch: clean(c.Arch, 32),
		Model: clean(c.Model, 128), Hostname: clean(c.Hostname, 255), User: clean(c.User, 128),
		UserAgent: clean(c.UserAgent, maxInfoField), Mobile: c.Mobile,
		Language: clean(c.Language, 35), Timezone: clean(c.Timezone, 64), Screen: clean(c.Screen, 32),
	}
	if *out == (ClientInfo{}) {
		return nil
	}
	return out
}

// Summary is a short human description, e.g. "laptop (linux/amd64, setu-cli)".
func (c *ClientInfo) Summary() string {
	if c == nil {
		return ""
	}
	name := c.Hostname
	if name == "" {
		name = c.Model
	}
	var parts []string
	if os := strings.TrimSpace(c.OS + " " + c.OSVersion); os != "" {
		if c.Arch != "" {
			os += "/" + c.Arch
		}
		parts = append(parts, os)
	}
	if c.App != "" {
		parts = append(parts, c.App)
	}
	switch {
	case name != "" && len(parts) > 0:
		return name + " (" + strings.Join(parts, ", ") + ")"
	case name != "":
		return name
	}
	return strings.Join(parts, ", ")
}

// ClientInfo is what a client reports about its device. Every field is
// optional; hosts ignore fields they do not know.
type ClientInfo struct {
	App       string `json:"app,omitempty"`       // "setu-cli" or "setu-web"
	Version   string `json:"version,omitempty"`   // client version
	DeviceID  string `json:"device_id,omitempty"` // random id persisted on the device / browser
	OS        string `json:"os,omitempty"`        // e.g. linux, windows, Android
	OSVersion string `json:"os_version,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Model     string `json:"model,omitempty"` // device model (UA client hints)
	Hostname  string `json:"hostname,omitempty"`
	User      string `json:"user,omitempty"` // OS account on the client device
	UserAgent string `json:"user_agent,omitempty"`
	Mobile    bool   `json:"mobile,omitempty"`
	Language  string `json:"language,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Screen    string `json:"screen,omitempty"` // WxH@ratio
}
