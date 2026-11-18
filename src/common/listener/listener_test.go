package listener

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestIPv6WildcardAcceptsIPv4(t *testing.T) {
	ln, err := ListenTCP("[::]:0")
	if err != nil {
		if isAddressFamilyUnsupported(err) {
			t.Skipf("IPv6 wildcard is unavailable: %v", err)
		}
		t.Fatalf("ListenTCP([::]:0) failed: %v", err)
	}
	defer func() { _ = ln.Close() }()

	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", port), time.Second)
	if err != nil {
		t.Fatalf("IPv4 connection to dual-stack listener failed: %v", err)
	}
	_ = conn.Close()
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept() failed: %v", err)
	}
	_ = accepted.Close()
}

func TestExplicitIPv4WildcardStaysIPv4(t *testing.T) {
	ln, err := ListenTCP("0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if tcpAddr := ln.Addr().(*net.TCPAddr); tcpAddr.IP.To4() == nil {
		t.Fatalf("explicit IPv4 wildcard bound non-IPv4 address %v", tcpAddr.IP)
	}
}
