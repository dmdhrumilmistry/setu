// Package link builds and parses invite links and copy/paste codes.
//
// Everything sensitive lives in the URL fragment (after '#'), which browsers
// never send to the web server, so the static page host (e.g. GitHub Pages)
// never sees secrets.
//
//	https://<pages>/#k=<secret>[&r=wss://relay1,wss://relay2]   nostr signaling
//	https://<pages>/#o=<offer code>                              manual signaling
package link

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// DefaultWebURL is the GitHub Pages hosted client.
const DefaultWebURL = "https://dmdhrumilmistry.github.io/setu/"

// Invite is the decoded content of an invite link.
type Invite struct {
	Secret string   // k=, nostr mode
	Relays []string // r=, optional relay override
	Offer  string   // o=, manual mode offer code
}

// Build renders an invite as a link rooted at base (or as a bare fragment if
// base is empty).
func Build(base string, inv Invite) string {
	v := url.Values{}
	if inv.Secret != "" {
		v.Set("k", inv.Secret)
	}
	if len(inv.Relays) > 0 {
		v.Set("r", strings.Join(inv.Relays, ","))
	}
	if inv.Offer != "" {
		v.Set("o", inv.Offer)
	}
	frag := encodeOrdered(v, "k", "r", "o")
	if base == "" {
		return frag
	}
	return strings.TrimSuffix(base, "#") + "#" + frag
}

func encodeOrdered(v url.Values, keys ...string) string {
	var parts []string
	for _, k := range keys {
		if val := v.Get(k); val != "" {
			parts = append(parts, k+"="+url.QueryEscape(val))
		}
	}
	return strings.Join(parts, "&")
}

// Parse accepts a full link, a bare fragment ("k=..."), or a bare offer code.
func Parse(s string) (Invite, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Invite{}, errors.New("empty invite")
	}
	frag := s
	if i := strings.IndexByte(s, '#'); i >= 0 {
		frag = s[i+1:]
	} else if !strings.Contains(s, "=") {
		return Invite{Offer: s}, nil
	}
	v, err := url.ParseQuery(frag)
	if err != nil {
		return Invite{}, fmt.Errorf("invalid invite fragment: %w", err)
	}
	inv := Invite{Secret: v.Get("k"), Offer: v.Get("o")}
	if r := v.Get("r"); r != "" {
		for _, u := range strings.Split(r, ",") {
			if u = strings.TrimSpace(u); u != "" {
				inv.Relays = append(inv.Relays, u)
			}
		}
	}
	if inv.Secret == "" && inv.Offer == "" {
		return Invite{}, errors.New("invite has neither k= (secret) nor o= (offer code)")
	}
	return inv, nil
}

// maxCodeSize bounds decompressed codes so a pasted blob cannot exhaust memory.
const maxCodeSize = 256 << 10

// EncodeCode serialises v as JSON, raw-deflates and base64url encodes it.
// The browser decodes it with DecompressionStream("deflate-raw").
func EncodeCode(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// DecodeCode reverses EncodeCode.
func DecodeCode(code string, v any) error {
	code = strings.Join(strings.Fields(code), "") // tolerate wrapped pastes
	code = strings.TrimRight(code, "=")
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		return fmt.Errorf("invalid code encoding: %w", err)
	}
	r := flate.NewReader(bytes.NewReader(raw))
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxCodeSize+1))
	if err != nil {
		return fmt.Errorf("invalid code compression: %w", err)
	}
	if len(data) > maxCodeSize {
		return errors.New("code too large")
	}
	return json.Unmarshal(data, v)
}
