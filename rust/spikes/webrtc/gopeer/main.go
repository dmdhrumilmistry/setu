// Command gopeer is the pion side of the Rust WebRTC spike: the same
// library and settings setu uses today. It signals over stdin/stdout (one
// JSON session description per line), opens or accepts the "setu" data
// channel (reliable, ordered: what setu uses), and sends and/or receives a
// number of bytes, reporting throughput and both DTLS fingerprints on stderr.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

var fpRE = regexp.MustCompile(`(?m)^a=fingerprint:sha-256 (\S+)`)

func fingerprint(sdp string) string {
	m := fpRE.FindStringSubmatch(sdp)
	if m == nil {
		return "?"
	}
	return strings.ToLower(strings.ReplaceAll(m[1], ":", ""))
}

func main() {
	role := flag.String("role", "offer", "offer (setu's host role) or answer (setu's client role)")
	send := flag.Int("send", 0, "bytes to send once the channel opens")
	recv := flag.Int("recv", 0, "bytes to receive before exiting")
	flag.Parse()
	log := func(f string, a ...any) { fmt.Fprintf(os.Stderr, "[pion/"+*role+"] "+f+"\n", a...) }

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<20)
	readDesc := func() webrtc.SessionDescription {
		if !in.Scan() {
			panic("no session description on stdin")
		}
		var d webrtc.SessionDescription
		if err := json.Unmarshal(in.Bytes(), &d); err != nil {
			panic(err)
		}
		return d
	}
	writeDesc := func() {
		<-webrtc.GatheringCompletePromise(pc)
		b, _ := json.Marshal(pc.LocalDescription())
		fmt.Println(string(b))
	}
	done := make(chan struct{})
	run := func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			log("channel %q open; local fp %s remote fp %s", dc.Label(), fingerprint(pc.LocalDescription().SDP), fingerprint(pc.RemoteDescription().SDP))
			if *send > 0 {
				go sendAll(dc, *send, log)
			}
		})
		got, start := 0, time.Time{}
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			if start.IsZero() {
				start = time.Now()
			}
			got += len(m.Data)
			if *recv > 0 && got >= *recv {
				d := time.Since(start)
				log("RESULT received %d bytes in %v = %.1f MiB/s", got, d.Round(time.Millisecond), float64(got)/(1<<20)/d.Seconds())
				close(done)
			}
		})
	}
	switch *role {
	case "offer":
		dc, err := pc.CreateDataChannel("setu", nil)
		if err != nil {
			panic(err)
		}
		run(dc)
		o, _ := pc.CreateOffer(nil)
		_ = pc.SetLocalDescription(o)
		writeDesc()
		if err := pc.SetRemoteDescription(readDesc()); err != nil {
			panic(err)
		}
	default:
		pc.OnDataChannel(run)
		if err := pc.SetRemoteDescription(readDesc()); err != nil {
			panic(err)
		}
		a, _ := pc.CreateAnswer(nil)
		_ = pc.SetLocalDescription(a)
		writeDesc()
	}
	if *recv > 0 {
		<-done
		time.Sleep(200 * time.Millisecond)
	} else {
		time.Sleep(time.Hour) // the driver ends a pure sender
	}
}

// sendAll sends n bytes in 32 KiB messages with the flow control setu uses:
// pause above 1 MiB queued, resume at 256 KiB.
func sendAll(dc *webrtc.DataChannel, n int, log func(string, ...any)) {
	chunk := make([]byte, 32<<10)
	rand.Read(chunk)
	low := make(chan struct{}, 1)
	dc.SetBufferedAmountLowThreshold(256 << 10)
	dc.OnBufferedAmountLow(func() {
		select {
		case low <- struct{}{}:
		default:
		}
	})
	start := time.Now()
	for sent := 0; sent < n; sent += len(chunk) {
		for dc.BufferedAmount() > 1<<20 {
			select {
			case <-low:
			case <-time.After(50 * time.Millisecond):
			}
		}
		if err := dc.Send(chunk); err != nil {
			log("send: %v", err)
			return
		}
	}
	log("queued %d bytes in %v", n, time.Since(start).Round(time.Millisecond))
}
