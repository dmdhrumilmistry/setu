// Command nextversion decides whether a commit should be released, and as
// which version. The auto-release workflow runs it on every commit that
// passed CI on main.
//
//	go run ./tools/nextversion [-bump auto|patch|minor|major] [-rev SHA]
//
// It prints "tag=vX.Y.Z" (for $GITHUB_OUTPUT), or "tag=" when there is
// nothing to release, and the reason on stderr.
//
// With -bump auto (the default), a release happens only when something that
// goes into the setu binary changed since the last tag: Go code under cmd/
// or internal/ (tests excluded), go.mod or go.sum. Docs, CI, the web client
// (published by the pages workflow) and tests do not cut a release. The bump
// follows the commit messages since the last tag: a breaking change ("type!:"
// or a "BREAKING CHANGE:" footer) is major (minor before v1), a "feat"
// commit is minor, anything else a patch. "[skip release]" in the newest
// commit message skips it.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

func main() {
	bump := flag.String("bump", "auto", "auto, patch, minor or major")
	rev := flag.String("rev", "HEAD", "the commit to release")
	flag.Parse()
	tag, reason, err := run(*rev, *bump)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nextversion:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, reason)
	fmt.Printf("tag=%s\n", tag)
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func run(rev, bump string) (tag, reason string, err error) {
	if here, _ := git("tag", "--points-at", rev, "--list", "v*"); here != "" {
		return "", "already released as " + strings.Fields(here)[0], nil
	}
	last, err := git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", rev)
	rng := last + ".." + rev
	if err != nil {
		last, rng = "", rev // no release yet: everything counts
	}
	log, err := git("log", "--format=%B%x1e", rng)
	if err != nil {
		return "", "", err
	}
	var msgs []string
	for _, m := range strings.Split(log, "\x1e") {
		if m = strings.TrimSpace(m); m != "" {
			msgs = append(msgs, m)
		}
	}
	var files []string
	if last != "" {
		out, err := git("diff", "--name-only", last, rev)
		if err != nil {
			return "", "", err
		}
		files = strings.Fields(out)
	} else {
		out, err := git("ls-tree", "-r", "--name-only", rev)
		if err != nil {
			return "", "", err
		}
		files = strings.Fields(out)
	}
	return next(last, msgs, files, bump)
}

var (
	versionRE  = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)
	breakingRE = regexp.MustCompile(`(?m)^[a-zA-Z0-9_/-]+(\([^)]*\))?!:|^BREAKING[ -]CHANGE:`)
	featRE     = regexp.MustCompile(`(?m)^feat(\([^)]*\))?:|^Feature:`)
)

// shipped reports whether a changed file goes into the setu binary.
func shipped(f string) bool {
	if f == "go.mod" || f == "go.sum" {
		return true
	}
	return (strings.HasPrefix(f, "cmd/") || strings.HasPrefix(f, "internal/")) &&
		strings.HasSuffix(f, ".go") && !strings.HasSuffix(f, "_test.go") &&
		!strings.Contains(f, "/e2e/") && !strings.Contains(f, "test/")
}

// next computes the release after last (empty: none yet) for these commit
// messages (newest first) and changed files.
func next(last string, msgs, files []string, bump string) (tag, reason string, err error) {
	major, minor, patch := 0, 0, 0
	if last != "" {
		m := versionRE.FindStringSubmatch(last)
		if m == nil {
			return "", "", fmt.Errorf("last tag %q is not vX.Y.Z", last)
		}
		major, _ = strconv.Atoi(m[1])
		minor, _ = strconv.Atoi(m[2])
		patch, _ = strconv.Atoi(m[3])
	}
	if bump == "auto" {
		if len(msgs) == 0 {
			return "", "no commits since " + last, nil
		}
		if strings.Contains(msgs[0], "[skip release]") {
			return "", "the newest commit asks to skip the release", nil
		}
		changed := 0
		for _, f := range files {
			if shipped(f) {
				changed++
			}
		}
		if changed == 0 {
			return "", "nothing that goes into the binary changed (docs, CI, web client or tests only)", nil
		}
		bump = "patch"
		for _, m := range msgs {
			switch {
			case breakingRE.MatchString(m):
				bump = "major"
			case featRE.MatchString(m) && bump == "patch":
				bump = "minor"
			}
		}
		if bump == "major" && major == 0 {
			bump = "minor" // before v1, breaking changes bump the minor version
		}
		reason = fmt.Sprintf("%d binary file(s) changed in %d commit(s): %s release", changed, len(msgs), bump)
	} else {
		reason = bump + " release requested"
	}
	switch bump {
	case "major":
		major, minor, patch = major+1, 0, 0
	case "minor":
		minor, patch = minor+1, 0
	case "patch":
		patch++
	default:
		return "", "", fmt.Errorf("-bump %q: want auto, patch, minor or major", bump)
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), reason, nil
}
