// Command releasesign makes and uses the ed25519 key that signs setu
// releases (SHA256SUMS.sig), which `setu upgrade` and the service verify.
//
//	go run ./tools/releasesign gen            write release-signing.key (0600), print the public key
//	go run ./tools/releasesign sign SHA256SUMS sign with the key in $SETU_RELEASE_SIGNING_KEY
//	go run ./tools/releasesign verify SHA256SUMS check SHA256SUMS.sig against update.ReleaseKeys
//
// Store the private key only as the SETU_RELEASE_SIGNING_KEY secret of the
// GitHub repository (gh secret set SETU_RELEASE_SIGNING_KEY < release-signing.key),
// delete the file, and add the public key to update.ReleaseKeys. Once a
// build carries the public key it refuses unsigned releases.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dmdhrumilmistry/setu/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "releasesign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "gen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile("release-signing.key", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Printf("private key: release-signing.key (store it as a repository secret, then delete it)\npublic key:  %s\n", base64.StdEncoding.EncodeToString(pub))
		return nil
	case len(args) == 2 && args[0] == "sign":
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("SETU_RELEASE_SIGNING_KEY")))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("SETU_RELEASE_SIGNING_KEY must hold the base64 key from `releasesign gen`")
		}
		msg, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), msg)
		return os.WriteFile(args[1]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644)
	case len(args) == 2 && args[0] == "verify":
		// What installed setu binaries will check: a release signed with a
		// key they do not know would be refused by every one of them.
		msg, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(args[1] + ".sig")
		if err != nil {
			return err
		}
		if err := update.VerifySums(msg, sig); err != nil {
			return err
		}
		fmt.Println("signature verifies against the release key built into setu")
		return nil
	}
	return errors.New("usage: releasesign gen | releasesign sign FILE | releasesign verify FILE")
}
