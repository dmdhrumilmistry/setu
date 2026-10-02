// Package proto defines the wire messages exchanged during signaling and on
// the WebRTC data channel. The JavaScript client in web/app.js mirrors these.
package proto

// Version of the data channel protocol.
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
}
