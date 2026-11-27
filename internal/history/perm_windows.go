package history

import "os"

// checkPrivate is a no-op on Windows: the service directory is under
// %LOCALAPPDATA%, whose ACL grants access only to the user, SYSTEM and
// administrators.
func checkPrivate(*os.File) error { return nil }
