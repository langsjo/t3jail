package vsock

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHybridVsock emulates cloud-hypervisor: ports in listening are echo
// servers; any other port closes the connection after the CONNECT line.
func fakeHybridVsock(t *testing.T, listening map[string]bool, reply string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "vsock.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				line, _ := r.ReadString('\n')
				port := strings.TrimSpace(strings.TrimPrefix(line, "CONNECT "))
				if !listening[port] {
					return
				}
				io.WriteString(conn, reply)
				io.Copy(conn, r) // echo
			}()
		}
	}()
	return sock
}

func TestDialHandshakeAndStream(t *testing.T) {
	sock := fakeHybridVsock(t, map[string]bool{"22": true}, "OK 1073741824\n")
	conn, err := Dial(context.Background(), sock, 22)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "hello")
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo = %q, %v; the OK line must not leak into the stream", buf, err)
	}
}

func TestDialPortNotListening(t *testing.T) {
	sock := fakeHybridVsock(t, map[string]bool{}, "")
	_, err := Dial(context.Background(), sock, 3773)
	if err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Fatalf("err = %v", err)
	}
}

func TestDialRejectsUnexpectedReply(t *testing.T) {
	sock := fakeHybridVsock(t, map[string]bool{"22": true}, "ERR nope\n")
	if _, err := Dial(context.Background(), sock, 22); err == nil {
		t.Fatal("unexpected reply accepted")
	}
}

func TestForwardSplicesAndHalfCloses(t *testing.T) {
	sock := fakeHybridVsock(t, map[string]bool{"3773": true}, "OK 1\n")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Forward(ctx, l, func(ctx context.Context) (net.Conn, error) { return Dial(ctx, sock, 3773) }, slog.New(slog.DiscardHandler))

	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\n\r\n")
	conn.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "GET / HTTP/1.1\r\n\r\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestProxyStdio(t *testing.T) {
	sock := fakeHybridVsock(t, map[string]bool{"22": true}, "OK 1\n")
	conn, err := Dial(context.Background(), sock, 22)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ProxyStdio(conn, strings.NewReader("SSH-2.0-test\r\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "SSH-2.0-test\r\n" {
		t.Fatalf("out = %q", out.String())
	}
}
