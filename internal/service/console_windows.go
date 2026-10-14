package service

import "golang.org/x/sys/windows"

// DetachConsole releases the console window Task Scheduler opened for the
// supervisor, so it runs without a visible window. ConPTY sessions do not
// need a console of their own.
func DetachConsole() {
	_, _, _ = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call()
}
