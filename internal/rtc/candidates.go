package rtc

import (
	"net"
	"strconv"
	"strings"
)

const maxAddresses = 16

// Addresses lists the network addresses a peer advertised in its SDP:
// public (server reflexive: the address its NAT presents to the internet)
// and local (host candidates). Browsers hide local addresses behind random
// mDNS names; those are skipped. Relay candidates are the TURN server's
// address, not the peer's, so they are not included.
func Addresses(sdp string) (public, local []string) {
	seen := map[string]bool{}
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		f := strings.Fields(line)
		// candidate:<foundation> <component> <proto> <priority> <addr> <port> typ <type> ...
		if len(f) < 8 || f[6] != "typ" {
			continue
		}
		ip := net.ParseIP(f[4])
		port, err := strconv.Atoi(f[5])
		if ip == nil || err != nil || port < 0 || port > 65535 {
			continue // mDNS name or malformed
		}
		addr := net.JoinHostPort(ip.String(), f[5])
		if seen[addr] {
			continue
		}
		seen[addr] = true
		switch f[7] {
		case "srflx", "prflx":
			if len(public) < maxAddresses {
				public = append(public, addr)
			}
		case "host":
			if len(local) < maxAddresses {
				local = append(local, addr)
			}
		}
	}
	return public, local
}
