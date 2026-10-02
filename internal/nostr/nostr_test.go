package nostr_test

import (
	"context"
	"testing"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/nostr"
	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
)

func TestSignVerify(t *testing.T) {
	kp, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: nostr.KindSignal, Tags: [][]string{{"p", "ab"}}, Content: "hello <&> \"world\""}
	if err := kp.Sign(&ev); err != nil {
		t.Fatal(err)
	}
	if err := ev.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	bad := ev
	bad.Content = "tampered"
	if bad.Verify() == nil {
		t.Fatal("tampered event verified")
	}
	bad = ev
	other, _ := nostr.GenerateKey()
	bad.PubKey = other.Pub
	if bad.Verify() == nil {
		t.Fatal("event with swapped pubkey verified")
	}
}

func TestPoolRoundTrip(t *testing.T) {
	r := relaytest.New()
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := nostr.NewPool(ctx, []string{r.URL()}, t.Logf)
	b := nostr.NewPool(ctx, []string{r.URL()}, t.Logf)
	defer a.Close()
	defer b.Close()
	if err := a.WaitConnected(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := b.WaitConnected(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got := make(chan nostr.Event, 4)
	b.Subscribe("s", nostr.Filter{Kinds: []int{nostr.KindSignal}, P: []string{"target"}}, func(ev nostr.Event) { got <- ev })
	time.Sleep(100 * time.Millisecond)

	kp, _ := nostr.GenerateKey()
	ev := nostr.Event{Kind: nostr.KindSignal, Tags: [][]string{{"p", "target"}}, Content: "x"}
	_ = kp.Sign(&ev)
	if n := a.Publish(ev); n != 1 {
		t.Fatalf("published to %d relays", n)
	}
	a.Publish(ev) // duplicate must be suppressed
	other := nostr.Event{Kind: nostr.KindSignal, Tags: [][]string{{"p", "someone-else"}}, Content: "y"}
	_ = kp.Sign(&other)
	a.Publish(other)

	select {
	case e := <-got:
		if e.ID != ev.ID {
			t.Fatalf("wrong event")
		}
	case <-ctx.Done():
		t.Fatal("no event received")
	}
	select {
	case e := <-got:
		t.Fatalf("unexpected extra event %s", e.Content)
	case <-time.After(300 * time.Millisecond):
	}
}
