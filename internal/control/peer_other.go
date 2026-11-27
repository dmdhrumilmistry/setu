//go:build !linux && !darwin

package control

import "net"

// checkPeer relies on the directory alone where the OS does not report a
// socket peer's user: on Windows the state directory's ACL admits only the
// user, SYSTEM and administrators.
func checkPeer(net.Conn) error { return nil }
