package policy

import (
	"testing"
	"time"
)

func TestIdleTimeout(t *testing.T) {
	var none *Policy
	if d, err := none.IdleTimeout(0, false); d != 0 || err != nil {
		t.Errorf("no policy: %v %v", d, err)
	}
	p, err := Parse([]byte(`{"MaxIdleTime": "15m"}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		d        time.Duration
		explicit bool
		want     time.Duration
		viol     bool
	}{
		{0, false, 15 * time.Minute, false}, // unset: the maximum
		{10 * time.Minute, true, 10 * time.Minute, false},
		{15 * time.Minute, true, 15 * time.Minute, false},
		{20 * time.Minute, true, 0, true},  // longer: refused
		{20 * time.Minute, false, 0, true}, // a stored profile value too
		{0, true, 0, true},                 // explicitly off: refused
	} {
		got, err := p.IdleTimeout(c.d, c.explicit)
		if got != c.want || isViolation(err, "MaxIdleTime") != c.viol {
			t.Errorf("IdleTimeout(%v, %v) = %v, %v", c.d, c.explicit, got, err)
		}
	}
	if _, err := Parse([]byte(`{"MaxIdleTime": "-1m"}`), "test"); err == nil {
		t.Error("negative MaxIdleTime accepted")
	}
}
