// Package listener creates TCP listeners with explicit dual-stack behavior.
package listener

import (
	"context"
	"log"
	"net"
	"strings"
	"sync"
	"syscall"
)

var fallbackWarning sync.Once

// ListenTCP binds address. IPv6 wildcard binds explicitly allow IPv4; only an
// unsupported IPv6 address family falls back to the IPv4 wildcard.
func ListenTCP(address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if host == "" || (ip != nil && ip.To4() == nil && ip.IsUnspecified()) {
		lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
			var sockErr error
			if err := c.Control(func(fd uintptr) { sockErr = setDualStack(fd) }); err != nil {
				return err
			}
			return sockErr
		}}
		ln, err := lc.Listen(context.Background(), "tcp6", net.JoinHostPort("::", port))
		if err == nil {
			return ln, nil
		}
		if !isAddressFamilyUnsupported(err) {
			return nil, err
		}
		fallbackWarning.Do(func() { log.Printf("warning: IPv6 is unavailable; falling back to IPv4 wildcard listener") })
		return net.Listen("tcp4", net.JoinHostPort("0.0.0.0", port))
	}
	if ip != nil && ip.To4() != nil {
		return net.Listen("tcp4", address)
	}
	if host == "::" || strings.Contains(host, ":") {
		return net.Listen("tcp6", address)
	}
	if host == "0.0.0.0" {
		return net.Listen("tcp4", address)
	}
	return net.Listen("tcp", address)
}
