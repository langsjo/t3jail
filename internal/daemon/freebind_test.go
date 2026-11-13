package daemon

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
)

func TestListenFreebindAcceptsLocalConnections(t *testing.T) {
	l, err := listenFreebind(context.Background(), netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Write([]byte("x"))
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil || buf[0] != 'x' {
		t.Fatalf("read %q, %v", buf, err)
	}
}

// Documentation addresses (RFC 5737, RFC 3849) are never on the host, so a
// plain listen fails there; with IP_FREEBIND it must succeed.
func TestListenFreebindAddressNotOnHost(t *testing.T) {
	for _, addr := range []string{"192.0.2.123:0", "[2001:db8::123]:0"} {
		ap := netip.MustParseAddrPort(addr)
		l, err := listenFreebind(context.Background(), ap)
		if ap.Addr().Is6() && (errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL) && !ipv6Available()) {
			t.Logf("%s: IPv6 unavailable here: %v", addr, err)
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", addr, err)
			continue
		}
		l.Close()
	}
}

func ipv6Available() bool {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}
