package secure

// checkKeyFileMode is a no-op on Windows: the key lives in the user's
// profile, whose ACL already restricts it to the user and administrators.
func checkKeyFileMode(string) error { return nil }
