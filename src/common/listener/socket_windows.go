//go:build windows

package listener

import (
	"errors"

	"golang.org/x/sys/windows"
)

func setDualStack(fd uintptr) error {
	return windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, windows.IPV6_V6ONLY, 0)
}

func isAddressFamilyUnsupported(err error) bool { return errors.Is(err, windows.WSAEAFNOSUPPORT) }
