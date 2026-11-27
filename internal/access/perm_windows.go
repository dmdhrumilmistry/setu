package access

// checkPrivate is a no-op on Windows: the service and session directories
// are under the user's profile, whose ACL keeps other users out.
func checkPrivate(string) error { return nil }
