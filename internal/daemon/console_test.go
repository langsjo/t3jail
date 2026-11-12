package daemon

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestSerialMuxFansOutAndForwardsInput(t *testing.T) {
	guest, serial := net.Pipe()
	var logBuf syncBuffer
	mux := newSerialMux(serial, &logBuf, slog.New(slog.DiscardHandler))
	go mux.pump()

	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "console.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go mux.serve(l)

	var clients []net.Conn
	for range 2 {
		c, err := net.Dial("unix", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients = append(clients, c)
	}
	// Wait until both clients are registered.
	for deadline := time.Now().Add(2 * time.Second); ; {
		mux.mu.Lock()
		n := len(mux.clients)
		mux.mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	go io.WriteString(guest, "login: ")
	for i, c := range clients {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 7)
		if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "login: " {
			t.Fatalf("client %d got %q, %v", i, buf, err)
		}
	}
	if logBuf.String() != "login: " {
		t.Errorf("log = %q", logBuf.String())
	}

	io.WriteString(clients[1], "root\n")
	guest.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(guest, buf); err != nil || string(buf) != "root\n" {
		t.Fatalf("guest got %q, %v", buf, err)
	}
}
