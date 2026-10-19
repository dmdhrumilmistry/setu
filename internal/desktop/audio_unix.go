//go:build linux || freebsd || netbsd || openbsd || dragonfly

package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// pulseAudio records the default output's monitor with parec, which talks
// to PulseAudio and to PipeWire (through pipewire-pulse), so setu needs no
// audio library of its own.
type pulseAudio struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	stderr *bytes.Buffer
	ch     chan []int16
	err    error // why ch closed
	once   sync.Once
}

const (
	pulseRate    = 48000
	pulseChunk   = pulseRate / audioPacketsPerSecond * 2 * 2 // one packet of 16-bit stereo
	pulseStartup = 3 * time.Second
)

var errNoParec = errors.New(`sharing sound needs the "parec" program (package pulseaudio-utils) and a PulseAudio or PipeWire sound server`)

func openAudio(OpenOptions) (AudioSource, error) {
	bin, err := exec.LookPath("parec")
	if err != nil {
		return nil, errNoParec
	}
	cmd := exec.Command(bin, "--device=@DEFAULT_MONITOR@", "--format=s16le",
		fmt.Sprintf("--rate=%d", pulseRate), "--channels=2", "--raw", "--latency-msec=20",
		"--client-name=setu", "--stream-name=remote desktop sound")
	cmd.Env = pulseEnv()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	a := &pulseAudio{cmd: cmd, out: out, stderr: &bytes.Buffer{}, ch: make(chan []int16, audioPacketsPerSecond)}
	cmd.Stderr = &limitedWriter{w: a.stderr, n: 4 << 10}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("capture sound: %w", err)
	}
	first, exited := make(chan struct{}), make(chan struct{})
	go a.pump(first, exited)
	// parec exits at once when there is no sound server: report that here,
	// rather than after a listener was told the sound is on.
	select {
	case <-exited:
		msg := strings.TrimSpace(a.stderr.String())
		if msg == "" {
			msg = "parec stopped; is a PulseAudio or PipeWire sound server running in this session?"
		}
		return nil, fmt.Errorf("capture sound: %s", msg)
	case <-first:
		return a, nil
	case <-time.After(pulseStartup):
		return a, nil // running, but nothing to record yet
	}
}

// pump reads whole packets from parec until it exits.
func (a *pulseAudio) pump(first, exited chan struct{}) {
	buf := make([]byte, pulseChunk)
	for n := 0; ; n++ {
		if _, err := io.ReadFull(a.out, buf); err != nil {
			_ = a.cmd.Wait()
			a.err = err
			close(a.ch)
			close(exited)
			return
		}
		if n == 0 {
			close(first)
		}
		s := make([]int16, len(buf)/2)
		for i := range s {
			s[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		select {
		case a.ch <- s:
		default: // Read is not keeping up: drop rather than lag
		}
	}
}

// pulseEnv finds the user's sound server when the process environment does
// not say (a service started outside the graphical session).
func pulseEnv() []string {
	env := os.Environ()
	if os.Getenv("XDG_RUNTIME_DIR") == "" && os.Getenv("PULSE_SERVER") == "" {
		dir := fmt.Sprintf("/run/user/%d", os.Getuid())
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			env = append(env, "XDG_RUNTIME_DIR="+dir)
		}
	}
	return env
}

func (a *pulseAudio) Format() (int, int) { return pulseRate, 2 }

func (a *pulseAudio) Read() ([]int16, error) {
	s, ok := <-a.ch
	if !ok {
		return nil, fmt.Errorf("parec stopped: %w", a.err)
	}
	return s, nil
}

func (a *pulseAudio) Close() error {
	a.once.Do(func() {
		if a.cmd.Process != nil {
			_ = a.cmd.Process.Kill()
		}
	})
	return nil
}

// limitedWriter keeps the first n bytes written to it.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
