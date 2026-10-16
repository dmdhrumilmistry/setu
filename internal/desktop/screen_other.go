//go:build !windows && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package desktop

import (
	"fmt"
	"runtime"
)

func open() (Screen, error) {
	return nil, fmt.Errorf("%w (%s): use Windows, or Linux/BSD with an X11 session", ErrUnsupported, runtime.GOOS)
}
