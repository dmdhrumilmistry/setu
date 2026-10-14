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

// DataChannelLabel is the label of the single reliable, ordered channel.
const DataChannelLabel = "setu"

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
)

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
}

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
