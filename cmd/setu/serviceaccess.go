package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/dmdhrumilmistry/setu/internal/access"
	"github.com/dmdhrumilmistry/setu/internal/service"
)

const accessUsage = `usage:
  setu service access list [name|all]
  setu service access block [--for 24h] [--note TEXT] <name|all> device|person|network <value>
  setu service access allow [--for 24h] [--note TEXT] <name|all> device|person|network <value>
  setu service access remove <name|all> <rule-id>`

// serviceAccess manages the service's block and allow lists.
func serviceAccess(args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New(accessUsage)
	}
	fs := flag.NewFlagSet("service access "+args[0], flag.ExitOnError)
	dur := fs.Duration("for", 0, "the rule expires after this long, for example 24h (default: never)")
	note := fs.String("note", "", "why the rule exists, for listings")
	_ = fs.Parse(args[1:])
	rest := fs.Args()
	st, err := loadService()
	if err != nil {
		return 1, err
	}
	scope := func(s string) (string, error) {
		if s == "all" || s == access.AllScope {
			return access.AllScope, nil
		}
		if _, err := st.cfg.Get(s); err != nil {
			return "", err
		}
		return s, nil
	}
	path := access.Path(st.dir)
	switch args[0] {
	case "list", "ls":
		if len(rest) > 1 {
			return 2, errors.New(accessUsage)
		}
		f, err := access.Load(path)
		if err != nil {
			return 1, err
		}
		want := ""
		if len(rest) == 1 {
			if want, err = scope(rest[0]); err != nil {
				return 1, err
			}
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tFOR\tACTION\tKIND\tVALUE\tEXPIRES\tNOTE")
		n := 0
		now := time.Now()
		for _, sc := range append([]string{access.AllScope}, st.cfg.Names()...) {
			if want != "" && sc != want {
				continue
			}
			for _, r := range f.Scopes[sc] {
				exp := "never"
				if !r.Expires.IsZero() {
					exp = r.Expires.Local().Format("2006-01-02 15:04")
					if !r.Active(now) {
						exp += " (expired)"
					}
				}
				label := sc
				if sc == access.AllScope {
					label = "all profiles"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, label, r.Action, r.Kind, r.Value, exp, r.Note)
				n++
			}
		}
		tw.Flush()
		if n == 0 {
			fmt.Fprintln(os.Stderr, "no block or allow rules: every client with a valid link and login may join")
		}
		return 0, nil
	case "block", "allow":
		if len(rest) != 3 {
			return 2, errors.New(accessUsage)
		}
		sc, err := scope(rest[0])
		if err != nil {
			return 1, err
		}
		r, err := access.New(rest[1], rest[2], args[0], *note, *dur, time.Now())
		if err != nil {
			return 1, err
		}
		if err := access.Update(path, func(f *access.File) error { return f.Add(sc, r) }); err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stderr, "%s %s (rule %s)\n", map[string]string{access.Block: "blocked", access.Allow: "allowed"}[r.Action], r.Describe(), r.ID)
		if r.Action == access.Allow {
			fmt.Fprintln(os.Stderr, "With an allow rule in place, only clients matching an allow rule can join; connected clients that do not are disconnected.")
		}
		enforced(st.dir, sc)
		return 0, nil
	case "remove", "rm", "unblock":
		if len(rest) != 2 {
			return 2, errors.New(accessUsage)
		}
		sc, err := scope(rest[0])
		if err != nil {
			return 1, err
		}
		if err := access.Update(path, func(f *access.File) error { return f.Remove(sc, rest[1]) }); err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stderr, "removed rule %s\n", rest[1])
		return 0, nil
	}
	return 2, errors.New(accessUsage)
}

// enforced applies a rule to the running service at once and says so.
func enforced(dir, scope string) {
	n, err := service.EnforceAccess(dir, scope)
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "The running service applies it within seconds (%v).\n", err)
	case n > 0:
		fmt.Fprintf(os.Stderr, "Disconnected %d client(s) the rule covers.\n", n)
	}
}
