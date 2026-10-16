package desktop

import "testing"

// TestKeyTable checks that no two keys share a code on any host platform,
// so every key the browser sends lands on exactly one physical key.
func TestKeyTable(t *testing.T) {
	evdev, scan, mac := map[uint16]string{}, map[uint16]string{}, map[uint16]string{}
	for code, k := range keys {
		for _, c := range []struct {
			seen map[uint16]string
			v    uint16
			skip bool
		}{{evdev, k.evdev, false}, {scan, k.scan, k.scan == 0}, {mac, k.mac, false}} {
			if c.skip {
				continue
			}
			if other, dup := c.seen[c.v]; dup {
				t.Errorf("%s and %s share code %#x", code, other, c.v)
			}
			c.seen[c.v] = code
		}
	}
	for _, code := range []string{"KeyA", "KeyZ", "Digit0", "Enter", "ControlLeft", "MetaLeft", "AltLeft", "ShiftLeft", "ArrowUp", "F12", "Space", "Backspace", "Tab", "Escape"} {
		if _, ok := keys[code]; !ok {
			t.Errorf("missing %s", code)
		}
	}
	if keys["KeyA"].mac != 0x00 || keys["MetaLeft"].mac != 0x37 || keys["ControlLeft"].mac != 0x3b || keys["Enter"].mac != 0x24 {
		t.Error("macOS keycodes do not match kVK_ANSI_A, kVK_Command, kVK_Control, kVK_Return")
	}
}
