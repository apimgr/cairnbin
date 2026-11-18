//go:build !windows

package listener

import (
	"errors"

	"golang.org/x/sys/unix"
)

func setDualStack(fd uintptr) error {
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0)
}

func isAddressFamilyUnsupported(err error) bool { return errors.Is(err, unix.EAFNOSUPPORT) }
