package desktop

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestAudioProbe captures this computer's sound for real while it plays a
// sound. It needs a desktop session (and on macOS the Screen Recording
// permission), so it runs only when SETU_AUDIO_PROBE names a command that
// makes a noise, e.g. SETU_AUDIO_PROBE="afplay /System/Library/Sounds/Glass.aiff".
func TestAudioProbe(t *testing.T) {
	cmd := os.Getenv("SETU_AUDIO_PROBE")
	if cmd == "" {
		t.Skip("set SETU_AUDIO_PROBE to a command that plays a sound")
	}
	src, err := OpenAudio(OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	rate, ch := src.Format()
	t.Logf("format: %d Hz, %d channel(s)", rate, ch)
	play := exec.Command("sh", "-c", cmd)
	if err := play.Start(); err != nil {
		t.Fatal(err)
	}
	defer play.Wait()
	deadline := time.Now().Add(5 * time.Second)
	var total, loud int
	for time.Now().Before(deadline) {
		s, err := src.Read()
		if err != nil {
			t.Fatal(err)
		}
		total += len(s)
		if !silent(s) {
			loud += len(s)
		}
		if loud > rate*ch/4 {
			break
		}
	}
	t.Logf("read %d samples, %d in audible buffers", total, loud)
	if loud == 0 {
		t.Fatal("captured only silence")
	}
}
