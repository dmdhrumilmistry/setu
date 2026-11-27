package e2e

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/client"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/link"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
)

// floodEnd marks the end of floodCommand's output. It holds dots, which
// base64 never produces, so the random data cannot contain it by chance.
const floodEnd = "END.OF.FLOOD"

// floodCommand prints mib MiB of random data as base64 (4/3 of that on
// the wire, in 200-column lines) as fast as it can after a line of input,
// then floodEnd.
func floodCommand(mib int) []string {
	return []string{"sh", "-c", "read go; head -c " + itoa(mib<<20) + " /dev/urandom | base64 -w 200; echo END.OF.FLOOD; sleep 60"}
}

func itoa(n int) string {
	var b []byte
	for n > 0 || len(b) == 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// readUntil reads c's output until want appears and returns how much it read.
func readUntil(t *testing.T, ctx context.Context, c *client.Conn, want string) int {
	t.Helper()
	n := 0
	tail := []byte{}
	for {
		select {
		case b := <-c.Out:
			n += len(b)
			tail = append(tail, b...)
			if bytes.Contains(tail, []byte(want)) {
				return n
			}
			if len(tail) > 64 {
				tail = tail[len(tail)-64:]
			}
		case <-c.Closed:
			t.Fatalf("disconnected after %d bytes, before %q", n, want)
		case <-ctx.Done():
			t.Fatalf("timed out after %d bytes, before %q", n, want)
		}
	}
}

// TestFastOutputBackpressure: output faster than the data channel carries
// it must arrive complete, the command slowed down to the client's pace,
// never with the client dropped as too slow.
func TestFastOutputBackpressure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh, head and base64")
	}
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	ctl, _ := startHost(t, ctx, r.URL(), func(c *host.Config) { c.Command, c.Verbose = floodCommand(32), false })
	c, err := dial(t, ctx, ctl, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	c.Write([]byte("go\r"))
	n := readUntil(t, ctx, c, floodEnd)
	// base64 makes 4 bytes of every 3 (plus newlines and CRs).
	if n < (32<<20)*4/3 {
		t.Fatalf("got %d bytes; output was lost", n)
	}
	t.Logf("%d MiB in %v", n>>20, time.Since(start).Round(time.Millisecond))
}

// TestStuckClientDropped: a client that stops reading holds the command for
// at most stallLimit; then the host disconnects it and the others keep going.
func TestStuckClientDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh, head and base64")
	}
	t.Parallel()
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cs, _ := secure.NewSecret()
	vs, _ := secure.NewSecret()
	s, err := host.New(host.Config{Command: floodCommand(64), ControlSecret: &cs, ViewSecret: &vs, Relays: []string{r.URL()},
		ICE: []proto.ICEServer{}, MaxClients: 3, Log: newTlog(t)})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	time.Sleep(300 * time.Millisecond)
	good, err := dial(t, ctx, link.Build("", link.Invite{Secret: cs.String(), Relays: []string{r.URL()}}), "")
	if err != nil {
		t.Fatal(err)
	}
	defer good.Close()
	stuck, err := dial(t, ctx, link.Build("", link.Invite{Secret: vs.String(), Relays: []string{r.URL()}}), "")
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.Close() // never reads its output, so its receive window fills
	good.Write([]byte("go\r"))
	readUntil(t, ctx, good, floodEnd)
	deadline := time.Now().Add(15 * time.Second)
	for len(s.Status().Clients) != 1 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if st := s.Status(); len(st.Clients) != 1 || st.Clients[0].Role != proto.RoleControl {
		t.Fatalf("the stuck client was not disconnected: %+v", st.Clients)
	}
}

// BenchmarkTerminalThroughput measures terminal output end to end: a real
// PTY on the host, WebRTC (DTLS/SCTP) and the Go client. One iteration is
// one session moving 32 MiB of random data as base64 (~43 MiB on the
// wire), so run it with -benchtime=Nx.
func BenchmarkTerminalThroughput(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("uses sh, head and base64")
	}
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		func() {
			r := relaytest.New()
			defer r.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			cs, _ := secure.NewSecret()
			s, err := host.New(host.Config{Command: floodCommand(32), ControlSecret: &cs, Relays: []string{r.URL()},
				ICE: []proto.ICEServer{}, MaxClients: 1, Log: io.Discard})
			if err != nil {
				b.Fatal(err)
			}
			hctx, stop := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() { defer close(done); _, _ = s.Run(hctx) }()
			defer func() { stop(); <-done }()
			time.Sleep(300 * time.Millisecond)
			c, err := client.Dial(ctx, link.Build("", link.Invite{Secret: cs.String(), Relays: []string{r.URL()}}), client.Options{Timeout: 30 * time.Second, Logf: func(string, ...any) {}})
			if err != nil {
				b.Fatal(err)
			}
			defer c.Close()
			b.StartTimer()
			c.Write([]byte("go\r"))
			n := 0
			tail := []byte{}
			for !bytes.Contains(tail, []byte(floodEnd)) {
				select {
				case m := <-c.Out:
					n += len(m)
					tail = append(tail, m...)
					if len(tail) > 64 {
						tail = tail[len(tail)-64:]
					}
				case <-c.Closed:
					b.Fatalf("disconnected after %d bytes", n)
				case <-ctx.Done():
					b.Fatalf("timed out after %d bytes", n)
				}
			}
			b.StopTimer()
			b.SetBytes(int64(n))
		}()
	}
}
