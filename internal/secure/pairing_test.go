package secure

import (
	"regexp"
	"testing"
)

func TestPairingCode(t *testing.T) {
	code, v, err := NewPairingVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`).MatchString(code) {
		t.Errorf("code %q", code)
	}
	if err := v.Validate(); err != nil {
		t.Fatal(err)
	}
	// Same cases as web/test/crypto.test.mjs.
	for in, want := range map[string]string{"7k2m-qx9d 4hrt": "7K2MQX9D4HRT", "oil-OIL": "011011"} {
		if got := NormalizePairingCode(in); got != want {
			t.Errorf("NormalizePairingCode(%q) = %q, want %q", in, got, want)
		}
	}
}
