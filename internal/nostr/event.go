// Package nostr is a deliberately small Nostr (NIP-01) client used only as a
// decentralised, serverless rendezvous for WebRTC signaling.
//
// setu publishes *ephemeral* events (kind 20000-29999, never stored by
// relays) whose content is AES-GCM ciphertext. Relays learn nothing but a
// random room id, throwaway public keys and timing.
package nostr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// KindSignal is the ephemeral event kind setu uses for signaling.
const KindSignal = 21210

// DefaultRelays are public relays that forward ephemeral events.
var DefaultRelays = []string{
	"wss://relay.damus.io",
	"wss://nos.lol",
	"wss://relay.primal.net",
	"wss://nostr.mom",
}

// Event is a NIP-01 event.
type Event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// TagValue returns the first value of the first tag named name.
func (e *Event) TagValue(name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

func (e *Event) hash() ([32]byte, error) {
	tags := e.Tags
	if tags == nil {
		tags = [][]string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // NIP-01 serialisation must not escape <>&
	if err := enc.Encode([]any{0, e.PubKey, e.CreatedAt, e.Kind, tags, e.Content}); err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// Verify checks the event id and BIP-340 signature.
func (e *Event) Verify() error {
	h, err := e.hash()
	if err != nil {
		return err
	}
	if hex.EncodeToString(h[:]) != e.ID {
		return errors.New("event id mismatch")
	}
	pkb, err := hex.DecodeString(e.PubKey)
	if err != nil {
		return err
	}
	pk, err := schnorr.ParsePubKey(pkb)
	if err != nil {
		return err
	}
	sb, err := hex.DecodeString(e.Sig)
	if err != nil {
		return err
	}
	sig, err := schnorr.ParseSignature(sb)
	if err != nil {
		return err
	}
	if !sig.Verify(h[:], pk) {
		return errors.New("bad signature")
	}
	return nil
}

// Keypair is a throwaway secp256k1 identity. setu creates a new one per
// process (host) or per join attempt (client); it carries no reputation.
type Keypair struct {
	priv *btcec.PrivateKey
	Pub  string
}

// GenerateKey returns a fresh random keypair.
func GenerateKey() (*Keypair, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, err
	}
	return &Keypair{priv: priv, Pub: hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))}, nil
}

// Sign fills PubKey, CreatedAt (if zero), ID and Sig.
func (k *Keypair) Sign(e *Event) error {
	e.PubKey = k.Pub
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	if e.Tags == nil {
		e.Tags = [][]string{}
	}
	h, err := e.hash()
	if err != nil {
		return err
	}
	sig, err := schnorr.Sign(k.priv, h[:])
	if err != nil {
		return err
	}
	e.ID = hex.EncodeToString(h[:])
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}
