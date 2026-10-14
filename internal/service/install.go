package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Unit describes what autostart should run.
type Unit struct {
	Exe     string // absolute path of the setu binary
	Dir     string // service state directory
	LogFile string // where the supervisor logs (unused under systemd)
	Path    string // PATH for the shared commands (Unix; Windows uses the user's)
}

// Args is the command line the autostart entry runs. The state directory
// is passed explicitly so the supervisor uses the one that was configured,
// even if SETU_SERVICE_DIR was only set in the installing shell.
func (u Unit) Args() []string {
	args := []string{"service", "run", "--dir", u.Dir}
	if u.LogFile != "" {
		args = append(args, "--log-file", u.LogFile)
	}
	return args
}

// env is the environment for the supervisor (and so the shared commands):
// service managers start with a minimal PATH that often lacks tools such as
// claude, so the installing shell's PATH is carried over.
func (u Unit) env() map[string]string {
	env := map[string]string{}
	if u.Path != "" {
		env["PATH"] = u.Path
	}
	return env
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
