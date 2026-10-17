package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/proto"
	"github.com/dmdhrumilmistry/setu/internal/secure"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

// Devices of service profiles: pairing codes, and listing, adding and
// removing the devices a profile admits (see internal/service/devices.go).

func servicePair(args []string) (int, error) {
	fs := flag.NewFlagSet("service pair", flag.ExitOnError)
	pairFor := fs.Duration("for", defaultPairFor, "how long the code works (one device can use it)")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 {
		return 2, errors.New("usage: setu service pair [--for 15m] <name> [person]")
	}
	if err := checkPairFor(*pairFor); err != nil {
		return 2, err
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	name := rest[0]
	p, err := st.cfg.Get(name)
	if err != nil {
		return 1, err
	}
	if !p.RequireDevice {
		return 1, fmt.Errorf("%s does not use paired devices (switch with `setu service auth %s device`)", name, name)
	}
	explicit := false
	fs.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "for" })
	if !explicit && st.pol.MaxPairingTime > 0 {
		*pairFor = min(*pairFor, st.pol.MaxPairingTime.D())
	}
	if err := st.pol.CheckPairing(*pairFor); err != nil {
		return 1, err
	}
	person := ""
	if len(rest) == 2 {
		if person = rest[1]; p.PersonalKeys[person] == nil {
			return 1, fmt.Errorf("%s has no personal key on %s", person, name)
		}
	}
	code, exp, err := newPairing(st.dir, name, *pairFor)
	if err != nil {
		return 1, err
	}
	printProfile(name, p, person)
	printPairing(name, code, exp)
	if _, ok := service.RunningPID(service.LockPath(st.dir)); !ok {
		fmt.Fprintln(os.Stderr, "\nThe service is not running: start it with `setu service install` (or `setu service run`).")
	}
	return 0, nil
}

// forgetDevices removes a profile's devices that became known the way how.
func forgetDevices(dir, name, how string) error {
	return service.UpdateDevices(dir, func(ds *service.DeviceState) error {
		if n := ds.Profile(name).Forget("", how); n > 0 {
			fmt.Fprintf(os.Stderr, "%s: forgot %d device(s) that had proved the old password\n", name, n)
		}
		return nil
	})
}

func serviceDevice(args []string) (int, error) {
	usage := errors.New("usage: setu service device list <name> | device add [--name N] [--person P | --view] <name> <fingerprint> | device remove <name> <fingerprint>|--all")
	if len(args) == 0 {
		return 2, usage
	}
	fs := flag.NewFlagSet("service device "+args[0], flag.ExitOnError)
	label := fs.String("name", "", "a name for the device, for listings")
	person := fs.String("person", "", "the device joins with this person's link (default: the shared control link)")
	view := fs.Bool("view", false, "the device joins with the shared view link")
	all := fs.Bool("all", false, "remove every device of the profile")
	_ = fs.Parse(args[1:])
	rest := fs.Args()
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	switch args[0] {
	case "list", "ls":
		if len(rest) != 1 {
			return 2, usage
		}
		if _, err := st.cfg.Get(rest[0]); err != nil {
			return 1, err
		}
		ds, err := service.LoadDevices(st.dir)
		if err != nil {
			return 1, err
		}
		pd := ds.Profiles[rest[0]]
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "DEVICE\tNAME\tLINK\tHOW\tADDED\tLAST SEEN")
		for _, fp := range pd.Fingerprints() {
			d := pd.Devices[fp]
			seen := "-"
			if !d.LastSeen.IsZero() {
				seen = d.LastSeen.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", fp, orDefault(d.Name, "-"), service.BindingLabel(d.Binding), d.How, d.Added.Local().Format("2006-01-02"), seen)
		}
		if err := w.Flush(); err != nil {
			return 1, err
		}
		if v := pd.ActivePairing(time.Now()); v != nil {
			fmt.Printf("\na pairing code is waiting for a device until %s\n", pd.Pairing.ExpiresAt.Local().Format("15:04"))
		}
		return 0, nil
	case "add":
		if len(rest) != 2 || (*person != "" && *view) {
			return 2, usage
		}
		p, err := st.cfg.Get(rest[0])
		if err != nil {
			return 1, err
		}
		fp, err := secure.NormalizeDeviceFingerprint(rest[1])
		if err != nil {
			return 1, err
		}
		role := proto.RoleControl
		if *view {
			role = proto.RoleView
		}
		switch {
		case *person != "" && p.PersonalKeys[*person] == nil:
			return 1, fmt.Errorf("%s has no personal key on %s", *person, rest[0])
		case *person == "" && role == proto.RoleControl && p.ControlKey == "":
			return 1, fmt.Errorf("%s has no shared control link: name the person whose link the device uses (--person)", rest[0])
		case *person == "" && role == proto.RoleView && p.ViewKey == "":
			return 1, fmt.Errorf("%s has no shared view link", rest[0])
		}
		binding := service.Binding(*person, role)
		now := time.Now().UTC()
		if err := service.UpdateDevices(st.dir, func(ds *service.DeviceState) error {
			if pd := ds.Profile(rest[0]); pd.Devices[fp] == nil {
				if err := st.pol.DeviceRoom(len(pd.Devices)); err != nil {
					return err
				}
			}
			ds.Profile(rest[0]).Devices[fp] = &service.Device{Name: *label, Binding: binding, How: service.HowManual, Added: now}
			return nil
		}); err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stderr, "%s admits device %s through the %s\n", rest[0], fp, service.BindingLabel(binding))
		return 0, nil
	case "remove", "rm", "revoke":
		if !((len(rest) == 2 && !*all) || (len(rest) == 1 && *all)) {
			return 2, usage
		}
		if _, err := st.cfg.Get(rest[0]); err != nil {
			return 1, err
		}
		fp := ""
		if !*all {
			if fp, err = secure.NormalizeDeviceFingerprint(rest[1]); err != nil {
				return 1, err
			}
		}
		n := 0
		if err := service.UpdateDevices(st.dir, func(ds *service.DeviceState) error {
			pd := ds.Profile(rest[0])
			if *all {
				n = pd.Forget("", "")
			} else if pd.Devices[fp] != nil {
				delete(pd.Devices, fp)
				n = 1
			}
			return nil
		}); err != nil {
			return 1, err
		}
		if n == 0 {
			return 1, fmt.Errorf("%s does not know that device", rest[0])
		}
		fmt.Fprintf(os.Stderr, "removed %d device(s) from %s; a connected one is disconnected within seconds\n", n, rest[0])
		return 0, nil
	}
	return 2, usage
}

func serviceAuth(args []string) (int, error) {
	fs := flag.NewFlagSet("service auth", flag.ExitOnError)
	passwordStdin := fs.Bool("password-stdin", false, "with password: read it from the first line of stdin")
	passwordEveryJoin := fs.Bool("password-every-join", false, "with password: ask for it on every join, not once per device")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 2 {
		return 2, errors.New("usage: setu service auth [--password-stdin] [--password-every-join] <name> device|password|none")
	}
	name := rest[0]
	mode, err := authMode(rest[1], false, false, nil)
	if err != nil {
		return 2, err
	}
	if *passwordEveryJoin && mode != service.AuthPassword {
		return 2, errors.New("--password-every-join needs password")
	}
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	p, err := st.cfg.Get(name)
	if err != nil {
		return 1, err
	}
	p.RequireDevice, p.PasswordEveryJoin = false, false
	switch mode {
	case service.AuthDevices:
		p.Password, p.RequireDevice = nil, true
	case service.AuthPassword:
		if p.Password, err = profilePassword(*passwordStdin, st.pol.MinPassword(servicePasswordMin)); err != nil {
			return 1, err
		}
		p.PasswordEveryJoin = *passwordEveryJoin
	case service.AuthNone:
		p.Password = nil
	}
	if err := p.Validate(); err != nil {
		return 1, err
	}
	if err := st.save(name); err != nil {
		return 1, err
	}
	if mode == service.AuthPassword {
		// Devices remembered for an earlier password must prove this one.
		if err := forgetDevices(st.dir, name, service.HowPassword); err != nil {
			return 1, err
		}
	}
	printProfile(name, p, "")
	if mode == service.AuthDevices {
		fmt.Fprintf(os.Stderr, "\nDevices already known keep working. Let a new one in with `setu service pair %s`.\n", name)
	}
	return 0, nil
}
