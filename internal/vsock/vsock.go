// Package vsock connects to guest vsock ports through cloud-hypervisor's
// "hybrid vsock" Unix socket and forwards host connections to them. Only the
// owner of the socket's directory can connect, so file permissions are the
// access control.
package vsock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// handshakeTimeout bounds the CONNECT/OK exchange.
const handshakeTimeout = 10 * time.Second

// Dial opens a stream to the guest's vsock port. Protocol: send
// "CONNECT <port>\n", receive "OK <host port>\n", then the stream is raw.
func Dial(ctx context.Context, socket string, port uint32) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	if err := handshake(ctx, conn, port); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func handshake(ctx context.Context, conn net.Conn, port uint32) error {
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	defer conn.SetDeadline(time.Time{})

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		return err
	}
	// Read byte by byte: anything after the newline belongs to the stream.
	line, err := readLine(conn, 64)
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("vsock port %d: nothing is listening in the guest", port)
	}
	if err != nil {
		return fmt.Errorf("vsock port %d: %w", port, err)
	}
	if !strings.HasPrefix(line, "OK ") {
		return fmt.Errorf("vsock port %d: unexpected reply %q", port, line)
	}
	return nil
}

func readLine(r io.Reader, limit int) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < limit {
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		if buf[0] == '\n' {
			return b.String(), nil
		}
		b.WriteByte(buf[0])
	}
	return "", errors.New("reply line too long")
}

// Splice copies between a and b in both directions until both sides are done,
// propagating half-closes, then closes both.
func Splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyHalf(a, b) }()
	go func() { defer wg.Done(); copyHalf(b, a) }()
	wg.Wait()
	a.Close()
	b.Close()
}

func copyHalf(dst, src net.Conn) {
	io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		dst.Close()
	}
}

// Forward accepts connections on l and splices each to a stream from dial,
// until ctx is cancelled.
func Forward(ctx context.Context, l net.Listener, dial func(context.Context) (net.Conn, error), log *slog.Logger) error {
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			upstream, err := dial(ctx)
			if err != nil {
				log.Warn("forward: cannot connect", "listener", l.Addr(), "err", err)
				conn.Close()
				return
			}
			Splice(conn, upstream)
		}()
	}
}

// ProxyStdio connects stdin/stdout to conn, for use as an ssh ProxyCommand.
func ProxyStdio(conn net.Conn, stdin io.Reader, stdout io.Writer) error {
	done := make(chan error, 1)
	go func() {
		io.Copy(conn, stdin)
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
	}()
	go func() {
		_, err := io.Copy(stdout, conn)
		done <- err
	}()
	return <-done
}
