// Command testrelay runs the in-memory test relay over TLS for browser tests.
// It prints "URL <wss url>" and "CERT <pem path>" then serves until killed.
package main

import (
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dmdhrumilmistry/setu/internal/nostr/relaytest"
)

func main() {
	r := relaytest.NewTLS()
	dir, err := os.MkdirTemp("", "setu-testrelay")
	if err != nil {
		panic(err)
	}
	cert := filepath.Join(dir, "cert.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.Certificate().Raw})
	if err := os.WriteFile(cert, pemBytes, 0o600); err != nil {
		panic(err)
	}
	fmt.Printf("URL %s\nCERT %s\n", r.URL(), cert)
	select {}
}
