package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/vsock"
)

// guestConn is an SSH connection to the guest: Dial and Listen work on the
// guest's side, like ssh -L and -R. *ssh.Client implements it.
type guestConn interface {
	Dial(network, addr string) (net.Conn, error)
	Listen(network, addr string) (net.Listener, error)
	Wait() error
	Close() error
}

type portKey struct {
	dir    control.PortDirection
	listen uint16
}

type openPort struct {
	control.PortForward
	stop context.CancelFunc
	done chan struct{} // closed once nothing listens anymore
}

// portForwards are the open forwards. Each has an SSH connection of its own;
// when it drops (the guest reboots, the VM stops) the forward is gone.
type portForwards struct {
	ctx  context.Context // ends all forwards
	dial func(context.Context) (guestConn, error)
	log  *slog.Logger
	// lost is signaled when a forward ends because its connection dropped.
	lost chan struct{}
	// retryDelay is how long keepOpen waits after the guest did not answer.
	retryDelay time.Duration

	mu   sync.Mutex
	open map[portKey]*openPort
}

var (
	errAlreadyOpen = errors.New("already open")
	errNoGuest     = errors.New("connecting to the VM over SSH")
)

func newPortForwards(ctx context.Context, dial func(context.Context) (guestConn, error), log *slog.Logger) *portForwards {
	return &portForwards{
		ctx: ctx, dial: dial, log: log,
		lost:       make(chan struct{}, 1),
		retryDelay: 2 * time.Second,
		open:       map[portKey]*openPort{},
	}
}

func (p *portForwards) Open(ctx context.Context, f control.PortForward) (control.PortForward, error) {
	f, err := f.Normalized()
	if err != nil {
		return f, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	key := portKey{f.Direction, f.Listen}
	if old, ok := p.open[key]; ok {
		return f, fmt.Errorf("%s is %w", old.PortForward, errAlreadyOpen)
	}

	conn, err := p.dial(ctx)
	if err != nil {
		return f, fmt.Errorf("%w: %w", errNoGuest, err)
	}
	l, dial, err := listenForward(ctx, conn, f)
	if err != nil {
		conn.Close()
		return f, err
	}
	fctx, stop := context.WithCancel(p.ctx)
	entry := &openPort{PortForward: f, stop: stop, done: make(chan struct{})}
	p.open[key] = entry
	go vsock.Forward(fctx, l, dial, p.log)
	go func() {
		conn.Wait()
		stop()
	}()
	go func() {
		<-fctx.Done()
		l.Close()
		conn.Close()
		p.mu.Lock()
		// Close removes the entry itself, so one still here was not closed
		// on request.
		lost := p.open[key] == entry
		if lost {
			delete(p.open, key)
		}
		p.mu.Unlock()
		close(entry.done)
		p.log.Info("closed port forward", "forward", f.String())
		if lost {
			select {
			case p.lost <- struct{}{}:
			default:
			}
		}
	}()
	p.log.Info("opened port forward", "forward", f.String())
	return f, nil
}

// listenForward listens on f's listening side and returns how to reach its
// target on the other side.
func listenForward(ctx context.Context, conn guestConn, f control.PortForward) (net.Listener, func(context.Context) (net.Conn, error), error) {
	target := net.JoinHostPort("localhost", strconv.Itoa(int(f.Target)))
	if f.Direction == control.PortToVM {
		addr := netip.AddrPortFrom(f.Bind, f.Listen)
		var l net.Listener
		var err error
		if addr.Addr().IsUnspecified() {
			// Go listens on both IPv4 and IPv6 for a wildcard "tcp" address.
			l, err = net.Listen("tcp", addr.String())
		} else {
			// A persisted forward may bind an address the host gets only
			// later, e.g. a Tailscale IP.
			l, err = listenFreebind(ctx, addr)
		}
		return l, func(context.Context) (net.Conn, error) { return conn.Dial("tcp", target) }, err
	}
	// sshd binds "localhost" to both loopback addresses.
	l, err := conn.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(int(f.Listen))))
	if err != nil {
		return nil, nil, fmt.Errorf("listening on port %d in the VM: %w", f.Listen, err)
	}
	return l, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}, nil
}

func (p *portForwards) Close(req control.ClosePortRequest) error {
	p.mu.Lock()
	entry, ok := p.open[portKey{req.Direction, req.Listen}]
	if ok {
		delete(p.open, portKey{req.Direction, req.Listen})
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("no forward %s port %d is open", req.Direction, req.Listen)
	}
	entry.stop()
	<-entry.done
	return nil
}

// keepOpen opens the forwards load returns once waitGuest says the guest's
// SSH server answers, and again whenever a forward dropped with its
// connection, e.g. because the guest rebooted. It returns when p's context
// ends or waitGuest gives up.
func (p *portForwards) keepOpen(waitGuest func(context.Context) bool, load func() ([]control.PortForward, error)) {
	for waitGuest(p.ctx) {
		var retry <-chan time.Time
		if !p.openAll(load) {
			retry = time.After(p.retryDelay)
		}
		select {
		case <-p.ctx.Done():
			return
		case <-p.lost:
		case <-retry:
		}
	}
}

// openAll opens the forwards load returns that are not open yet. It reports
// false if the guest did not answer, so trying again later may succeed.
func (p *portForwards) openAll(load func() ([]control.PortForward, error)) bool {
	fwds, err := load()
	if err != nil {
		p.log.Warn("not opening persisted port forwards", "err", err)
		return true
	}
	for _, f := range fwds {
		_, err := p.Open(p.ctx, f)
		switch {
		case err == nil, errors.Is(err, errAlreadyOpen):
		case errors.Is(err, errNoGuest):
			p.log.Info("guest not answering, retrying persisted port forwards", "err", err)
			return false
		default:
			p.log.Warn("could not open persisted port forward", "forward", f.String(), "err", err)
		}
	}
	return true
}

func (p *portForwards) List() []control.PortForward {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]control.PortForward, 0, len(p.open))
	for _, e := range p.open {
		out = append(out, e.PortForward)
	}
	slices.SortFunc(out, func(a, b control.PortForward) int {
		return cmp.Or(cmp.Compare(a.Direction, b.Direction), cmp.Compare(a.Listen, b.Listen))
	})
	return out
}

// dialGuestSSH logs in to the guest's sshd over vsock with t3jail's key.
func (d *Daemon) dialGuestSSH(ctx context.Context) (guestConn, error) {
	key, err := os.ReadFile(d.inst.SSHKey())
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := vsock.Dial(ctx, d.inst.VsockSocket(), d.abi.VsockSSHPort)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	c, chans, reqs, err := ssh.NewClientConn(conn, "t3jail", &ssh.ClientConfig{
		User: d.abi.GuestUser,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		// The vsock socket reaches only this VM, which is untrusted anyway, so
		// its host key would authenticate nothing.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

func (d *Daemon) listPorts(context.Context, control.Empty) ([]control.PortForward, error) {
	return d.ports.List(), nil
}

func (d *Daemon) openPort(ctx context.Context, f control.PortForward) (control.PortForward, error) {
	return d.ports.Open(ctx, f)
}

func (d *Daemon) closePort(_ context.Context, req control.ClosePortRequest) (control.Empty, error) {
	return control.Empty{}, d.ports.Close(req)
}
