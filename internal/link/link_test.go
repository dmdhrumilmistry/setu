package link

import (
	"strings"
	"testing"

	"github.com/dmdhrumilmistry/setu/internal/proto"
)

func TestBuildParse(t *testing.T) {
	l := Build("https://x.github.io/setu/", Invite{Secret: "abc_-123", Relays: []string{"wss://a", "wss://b"}})
	if !strings.HasPrefix(l, "https://x.github.io/setu/#k=abc_-123&r=") {
		t.Fatalf("unexpected link %s", l)
	}
	inv, err := Parse(l)
	if err != nil || inv.Secret != "abc_-123" || len(inv.Relays) != 2 || inv.Relays[1] != "wss://b" {
		t.Fatalf("parse: %+v %v", inv, err)
	}
	inv, err = Parse("k=zzz")
	if err != nil || inv.Secret != "zzz" {
		t.Fatalf("bare fragment: %+v %v", inv, err)
	}
	inv, err = Parse("  SOMECODE  ")
	if err != nil || inv.Offer != "SOMECODE" {
		t.Fatalf("bare code: %+v %v", inv, err)
	}
	if _, err := Parse("https://x/#foo=bar"); err == nil {
		t.Fatal("expected error for link without k/o")
	}
}

func TestCodeRoundTrip(t *testing.T) {
	in := proto.Signal{Type: proto.SigOffer, SID: "s1", SDP: strings.Repeat("a=candidate:1 1 udp 1 1.2.3.4 5 typ host\r\n", 20)}
	code, err := EncodeCode(in)
	if err != nil {
		t.Fatal(err)
	}
	var out proto.Signal
	// tolerate line wrapping in pastes
	wrapped := code[:10] + "\n " + code[10:]
	if err := DecodeCode(wrapped, &out); err != nil {
		t.Fatal(err)
	}
	if out.SDP != in.SDP || out.SID != "s1" {
		t.Fatal("mismatch")
	}
	if err := DecodeCode("!!!", &out); err == nil {
		t.Fatal("garbage accepted")
	}
}
