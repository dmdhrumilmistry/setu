package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/audit"
	"github.com/dmdhrumilmistry/setu/internal/audit/audittest"
	"github.com/dmdhrumilmistry/setu/internal/host"
	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/pion/webrtc/v4"
)

// startUploadHost runs a manual-mode terminal share and returns a ready,
// authenticated control client.
func startUploadHost(t *testing.T, ctx context.Context, uploadDir string, al *audit.Logger) *deskClient {
	t.Helper()
	offers := make(chan string, 1)
	answers := make(chan string, 1)
	s, err := host.New(host.Config{
		Command:    echoCommand(),
		Manual:     true,
		ICE:        []proto.ICEServer{},
		Log:        newTlog(t),
		Audit:      al,
		UploadDir:  uploadDir,
		OnInvite:   func(inv host.Invite) { offers <- inv.Code },
		ReadAnswer: func(ctx context.Context) (string, error) { return <-answers, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	runHost(t, ctx, s)
	c, answer := newDeskClient(t, ctx, <-offers)
	answers <- answer
	select {
	case c.ctlDC = <-c.dc:
	case <-time.After(30 * time.Second):
		t.Fatal("data channel never opened")
	}
	c.expect(proto.CtlHello)
	c.send(proto.Control{T: proto.CtlAuth})
	if r := c.expect(proto.CtlReady); r.Upload != (uploadDir != "") {
		t.Fatalf("ready says upload=%v with upload dir %q", r.Upload, uploadDir)
	}
	return c
}

// uploadResult is what the host answered on an upload channel.
type uploadResult struct {
	done proto.Control
	err  string
}

// upload sends data as name on a new upload channel. announce overrides the
// announced size (to test refusals); stopAfter closes the channel after that
// many bytes.
func (c *deskClient) upload(name string, data []byte, announce int64, stopAfter int) uploadResult {
	c.t.Helper()
	dc, err := c.pc.CreateDataChannel(proto.UploadChannelLabel, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	replies := make(chan proto.Control, 4)
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		var ctl proto.Control
		if json.Unmarshal(m.Data, &ctl) == nil {
			replies <- ctl
		}
	})
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	<-opened
	hdr, _ := json.Marshal(proto.Control{T: proto.CtlUpload, Name: name, Size: announce})
	dc.SendText(string(hdr))
	wait := func() proto.Control {
		select {
		case r := <-replies:
			return r
		case <-time.After(15 * time.Second):
			c.t.Fatal("no answer on the upload channel")
			return proto.Control{}
		}
	}
	r := wait()
	if r.T == proto.CtlError {
		return uploadResult{err: r.Msg}
	}
	if r.T == proto.CtlUploadDone {
		return uploadResult{done: r}
	}
	for off := 0; off < len(data); off += 16 << 10 {
		end := min(off+16<<10, len(data))
		if stopAfter > 0 && off >= stopAfter {
			dc.Close()
			return uploadResult{err: "stopped"}
		}
		dc.Send(data[off:end])
	}
	r = wait()
	if r.T == proto.CtlError {
		return uploadResult{err: r.Msg}
	}
	return uploadResult{done: r}
}

func TestUpload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	col := audittest.New(t)
	al, err := audit.New(ctx, audit.Config{Endpoint: col.URL, Required: true}, audit.Options{Version: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "inbox")
	c := startUploadHost(t, ctx, dir, al)

	data := bytes.Repeat([]byte("setu upload test \x00\xff\n"), 30000) // ~600 KB, binary safe
	sum := sha256.Sum256(data)
	r := c.upload("../../escape attempt.bin", data, int64(len(data)), 0)
	if r.err != "" {
		t.Fatalf("upload refused: %s", r.err)
	}
	if filepath.Dir(r.done.Name) != dir || filepath.Base(r.done.Name) != "escape attempt.bin" {
		t.Fatalf("saved as %q, want inside %s", r.done.Name, dir)
	}
	if r.done.Text != hex.EncodeToString(sum[:]) {
		t.Fatalf("host sha256 %s", r.done.Text)
	}
	got, err := os.ReadFile(r.done.Name)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("saved file differs (%d bytes, %v)", len(got), err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(r.done.Name); st.Mode().Perm() != 0o600 {
			t.Fatalf("saved with mode %v", st.Mode().Perm())
		}
	}

	// The same name again gets a new file; the first is untouched.
	if r2 := c.upload("escape attempt.bin", []byte("second"), 6, 0); r2.err != "" || filepath.Base(r2.done.Name) != "escape attempt (1).bin" {
		t.Fatalf("second upload: %+v", r2)
	}
	// An empty file.
	if r3 := c.upload("empty.txt", nil, 0, 0); r3.err != "" {
		t.Fatalf("empty upload: %s", r3.err)
	}
	// Too large, and more data than announced, are refused.
	if r4 := c.upload("huge.iso", nil, host.MaxUploadSize+1, 0); !strings.Contains(r4.err, "at most") {
		t.Fatalf("oversized upload: %+v", r4)
	}
	if r5 := c.upload("liar.bin", []byte("0123456789"), 4, 0); !strings.Contains(r5.err, "more data than announced") {
		t.Fatalf("over-long upload: %+v", r5)
	}
	// An interrupted upload leaves nothing behind.
	big := bytes.Repeat([]byte{7}, 200<<10)
	c.upload("interrupted.bin", big, int64(len(big)), 64<<10)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "interrupted.bin")); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the interrupted upload left a file behind")
		}
		time.Sleep(50 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("inbox has %v, want the two uploads and the empty file", names)
	}

	if err := al.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	recs := col.Events("setu.file.upload")
	if len(recs) != 3 || audittest.Attr(recs[0], "file.hash.sha256") != hex.EncodeToString(sum[:]) {
		t.Fatalf("upload audit records: %d", len(recs))
	}
}

func TestUploadDisabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := startUploadHost(t, ctx, "", nil)
	if r := c.upload("x.txt", []byte("x"), 1, 0); !strings.Contains(r.err, "turned off") {
		t.Fatalf("upload with uploads off: %+v", r)
	}
}
