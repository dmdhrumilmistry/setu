package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/dmdhrumilmistry/setu/internal/client"
)

// runDevice shows or resets this installation's device key, which hosts
// use to approve this device automatically (setu share --trust-device).
func runDevice(args []string) (int, error) {
	path, err := client.DeviceKeyPath()
	if err != nil {
		return 1, err
	}
	switch {
	case len(args) == 0 || args[0] == "show":
		key, err := client.LoadDeviceKey(true)
		if err != nil {
			return 1, err
		}
		fmt.Println(key.Fingerprint())
		fmt.Fprintf(os.Stderr, "\nThis is this device's key fingerprint. A host can approve it automatically with\n  setu share --approve --trust-device %s\nor by answering \"a\" when asked to approve it. The private key stays in\n  %s\n", key.Fingerprint(), path)
		return 0, nil
	case args[0] == "reset":
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 1, err
		}
		key, err := client.LoadDeviceKey(true)
		if err != nil {
			return 1, err
		}
		fmt.Printf("new device key: %s\n", key.Fingerprint())
		fmt.Fprintln(os.Stderr, "Hosts that trusted the old key will ask to approve this device again.")
		return 0, nil
	}
	return 2, errors.New("usage: setu device [show | reset]")
}
