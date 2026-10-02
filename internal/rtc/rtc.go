// Package rtc wraps the pion/webrtc bits shared by host and client.
package rtc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/pion/logging"
	"github.com/pion/webrtc/v4"
)

// DefaultICE uses Google's public STUN servers. STUN only reveals your public
// address to Google; no terminal data ever flows through it.
var DefaultICE = []proto.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302", "stun:stun1.l.google.com:19302"}},
}

// GatherTimeout bounds how long we wait for ICE candidate gathering. We do
// non-trickle ICE so every signaling message is self contained.
const GatherTimeout = 6 * time.Second

func toPion(servers []proto.ICEServer) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	return out
}

// ValidateICE rejects ICE server URLs that are not stun:/turn:/turns:.
func ValidateICE(servers []proto.ICEServer) error {
	for _, s := range servers {
		for _, u := range s.URLs {
			if !(strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:")) {
				return fmt.Errorf("invalid ICE server URL %q", u)
			}
		}
	}
	return nil
}

// NewPeer creates a PeerConnection configured for data channels only.
func NewPeer(ice []proto.ICEServer) (*webrtc.PeerConnection, error) {
	if err := ValidateICE(ice); err != nil {
		return nil, err
	}
	var se webrtc.SettingEngine
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	if os.Getenv("SETU_DEBUG_WEBRTC") != "" {
		lf.DefaultLogLevel = logging.LogLevelDebug
	}
	se.LoggerFactory = lf
	se.SetICETimeouts(10*time.Second, 30*time.Second, 2*time.Second)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))
	return api.NewPeerConnection(webrtc.Configuration{ICEServers: toPion(ice)})
}

// WaitGathered waits for ICE gathering to finish (or the timeout) and returns
// the local SDP including every candidate found so far.
func WaitGathered(ctx context.Context, pc *webrtc.PeerConnection, done <-chan struct{}) (string, error) {
	t := time.NewTimer(GatherTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	ld := pc.LocalDescription()
	if ld == nil {
		return "", fmt.Errorf("no local description")
	}
	return ld.SDP, nil
}

// Offer creates an offer and returns the gathered SDP.
func Offer(ctx context.Context, pc *webrtc.PeerConnection) (string, error) {
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return "", err
	}
	return WaitGathered(ctx, pc, done)
}

// Answer applies a remote offer and returns the gathered answer SDP.
func Answer(ctx context.Context, pc *webrtc.PeerConnection, offerSDP string) (string, error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", fmt.Errorf("invalid offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	done := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	return WaitGathered(ctx, pc, done)
}
