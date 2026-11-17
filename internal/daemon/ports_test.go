package daemon

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/langsjo/t3jail/internal/control"
)

// fakeGuest stands in for the SSH connection. The guest's localhost is this
// process: Dial maps guest addresses to real ones, and Listen opens a real
// listener on a free port and records where.
type fakeGuest struct {
	dialMap map[string]string

	mu       sync.Mutex
	listened map[string]string
	done     chan struct{}
	once     sync.Once
}

func newFakeGuest(dialMap map[string]string) *fakeGuest {
	return &fakeGuest{dialMap: dialMap, listened: map[string]string{}, done: make(chan struct{})}
}

func (g *fakeGuest) Dial(_, addr string) (net.Conn, error) {
	real, ok := g.dialMap[addr]
	if !ok {
		return nil, fmt.Errorf("nothing listens on %s in the guest", addr)
	}
	return net.Dial("tcp", real)
}

func (g *fakeGuest) Listen(_, addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.listened[addr] = l.Addr().String()
	g.mu.Unlock()
	return l, nil
}

func (g *fakeGuest) Wait() error  { <-g.done; return nil }
func (g *fakeGuest) Close() error { g.once.Do(func() { close(g.done) }); return nil }

// echoServer answers each line with "<name>: <line>".
func echoServer(t *testing.T, name string) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
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
				s := bufio.NewScanner(conn)
				for s.Scan() {
					fmt.Fprintf(conn, "%s: %s\n", name, s.Text())
				}
			}()
		}
	}()
	return l.Addr().String()
}

func freePort(t *testing.T) uint16 {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func port(t *testing.T, addr string) uint16 {
	ap, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(ap.Port)
}

func roundTrip(t *testing.T, addr string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "dial: " + err.Error()
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintln(conn, "hi")
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && err != io.EOF {
		return "read: " + err.Error()
	}
	return line
}

func testPorts(t *testing.T, guests chan *fakeGuest) *portForwards {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dial := func(context.Context) (guestConn, error) { return <-guests, nil }
	return newPortForwards(ctx, dial, slog.New(slog.DiscardHandler))
}

func TestPortToVM(t *testing.T) {
	guest := newFakeGuest(map[string]string{"localhost:3000": echoServer(t, "guest")})
	guests := make(chan *fakeGuest, 2)
	guests <- guest
	p := testPorts(t, guests)

	listen := freePort(t)
	f, err := p.Open(context.Background(), control.PortForward{Direction: control.PortToVM, Listen: listen, Target: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if f.Bind != control.DefaultPortBind {
		t.Errorf("bind %v, want the default", f.Bind)
	}
	hostAddr := fmt.Sprintf("127.0.0.1:%d", listen)
	if got := roundTrip(t, hostAddr); got != "guest: hi\n" {
		t.Errorf("through the forward: %q", got)
	}

	if _, err := p.Open(context.Background(), control.PortForward{Direction: control.PortToVM, Listen: listen, Target: 4000}); err == nil {
		t.Error("opened the same port twice")
	}
	if err := p.Close(control.ClosePortRequest{Direction: control.PortFromVM, Listen: listen}); err == nil {
		t.Error("closed a forward in the other direction")
	}
	if err := p.Close(control.ClosePortRequest{Direction: control.PortToVM, Listen: listen}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-guest.done:
	default:
		t.Error("the guest connection is still open")
	}
	if _, err := net.Dial("tcp", hostAddr); err == nil {
		t.Error("the host port still accepts connections")
	}
	if len(p.List()) != 0 {
		t.Errorf("still listed: %v", p.List())
	}
}

func TestPortFromVM(t *testing.T) {
	host := echoServer(t, "host")
	guest := newFakeGuest(nil)
	guests := make(chan *fakeGuest, 1)
	guests <- guest
	p := testPorts(t, guests)

	if _, err := p.Open(context.Background(), control.PortForward{Direction: control.PortFromVM, Listen: 5432, Target: port(t, host)}); err != nil {
		t.Fatal(err)
	}
	guest.mu.Lock()
	inGuest, ok := guest.listened["localhost:5432"]
	guest.mu.Unlock()
	if !ok {
		t.Fatalf("listened on %v in the guest", guest.listened)
	}
	if got := roundTrip(t, inGuest); got != "host: hi\n" {
		t.Errorf("through the forward: %q", got)
	}
}

func TestPortForwardEndsWithItsConnection(t *testing.T) {
	guest := newFakeGuest(map[string]string{"localhost:3000": echoServer(t, "guest")})
	guests := make(chan *fakeGuest, 1)
	guests <- guest
	p := testPorts(t, guests)

	listen := freePort(t)
	if _, err := p.Open(context.Background(), control.PortForward{Direction: control.PortToVM, Listen: listen, Target: 3000}); err != nil {
		t.Fatal(err)
	}
	guest.Close() // e.g. the guest rebooted
	waitFor(t, "the forward to go away", func() bool { return len(p.List()) == 0 })
	waitFor(t, "the host port to close", func() bool {
		_, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", listen))
		return err != nil
	})
}

func TestPortOpenRejects(t *testing.T) {
	p := testPorts(t, make(chan *fakeGuest))
	for _, f := range []control.PortForward{
		{Direction: control.PortToVM, Listen: 0, Target: 1},
		{Direction: "sideways", Listen: 1, Target: 1},
		{Direction: control.PortFromVM, Bind: control.DefaultPortBind, Listen: 1, Target: 1},
	} {
		if _, err := p.Open(context.Background(), f); err == nil {
			t.Errorf("opened %+v", f)
		}
	}
}

func TestKeepOpenReopensAfterTheGuestReboots(t *testing.T) {
	guests := make(chan *fakeGuest, 1)
	p := testPorts(t, guests)
	listen := freePort(t)
	persisted := []control.PortForward{{Direction: control.PortToVM, Listen: listen, Target: 3000}}
	go p.keepOpen(func(ctx context.Context) bool { return ctx.Err() == nil },
		func() ([]control.PortForward, error) { return persisted, nil })

	hostAddr := fmt.Sprintf("127.0.0.1:%d", listen)
	before := newFakeGuest(map[string]string{"localhost:3000": echoServer(t, "before")})
	guests <- before
	waitFor(t, "the persisted forward", func() bool { return roundTrip(t, hostAddr) == "before: hi\n" })

	before.Close()
	guests <- newFakeGuest(map[string]string{"localhost:3000": echoServer(t, "after")})
	waitFor(t, "the forward to reopen", func() bool { return roundTrip(t, hostAddr) == "after: hi\n" })
}

func TestKeepOpenRetriesWhileTheGuestDoesNotAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	guest := newFakeGuest(map[string]string{"localhost:3000": echoServer(t, "guest")})
	var mu sync.Mutex
	attempts := 0
	dial := func(context.Context) (guestConn, error) {
		mu.Lock()
		defer mu.Unlock()
		if attempts++; attempts < 3 {
			return nil, fmt.Errorf("connection refused")
		}
		return guest, nil
	}
	p := newPortForwards(ctx, dial, slog.New(slog.DiscardHandler))
	p.retryDelay = time.Millisecond
	listen := freePort(t)
	go p.keepOpen(func(ctx context.Context) bool { return ctx.Err() == nil },
		func() ([]control.PortForward, error) {
			return []control.PortForward{{Direction: control.PortToVM, Listen: listen, Target: 3000}}, nil
		})
	hostAddr := fmt.Sprintf("127.0.0.1:%d", listen)
	waitFor(t, "the persisted forward", func() bool { return roundTrip(t, hostAddr) == "guest: hi\n" })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
