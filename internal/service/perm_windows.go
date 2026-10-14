package service

// checkPrivate is a no-op on Windows: files under %LOCALAPPDATA% inherit an
// ACL that grants access only to the user, SYSTEM and administrators.
func checkPrivate(string) error { return nil }
