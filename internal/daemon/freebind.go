package daemon

import (
	"context"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenFreebind listens on addr even if the host does not have that address
// yet (IP_FREEBIND): connections arrive once it appears, e.g. a Tailscale IP
// after Tailscale comes up, so nothing has to bind again. The option is per
// socket and needs no privileges or sysctl.
func listenFreebind(ctx context.Context, addr netip.AddrPort) (net.Listener, error) {
	network, level, opt := "tcp4", unix.IPPROTO_IP, unix.IP_FREEBIND
	if addr.Addr().Is6() {
		network, level, opt = "tcp6", unix.IPPROTO_IPV6, unix.IPV6_FREEBIND
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var sockErr error
		if err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), level, opt, 1)
		}); err != nil {
			return err
		}
		return sockErr
	}}
	return lc.Listen(ctx, network, addr.String())
}
