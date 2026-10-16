//go:build !windows && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package desktop

import (
	"fmt"
	"runtime"
)

func open(OpenOptions) (Screen, error) {
	return nil, fmt.Errorf("%w (%s): use Windows, macOS, or Linux/BSD with an X11 session", ErrUnsupported, runtime.GOOS)
}
