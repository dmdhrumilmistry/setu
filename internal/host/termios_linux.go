package host

import "golang.org/x/sys/unix"

// termiosPasswordMode reads the terminal mode through the PTY master, which
// shares the line discipline settings of the command's side.
func termiosPasswordMode(fd uintptr) bool {
	t, err := unix.IoctlGetTermios(int(fd), unix.TCGETS)
	return err == nil && t.Lflag&unix.ICANON != 0 && t.Lflag&unix.ECHO == 0
}
