package control

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isRefused(err error) bool { return errors.Is(err, windows.WSAECONNREFUSED) }
