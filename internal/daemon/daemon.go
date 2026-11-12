// Package daemon supervises one running VM: virtiofsd, pasta with the netns
// helper (which runs cloud-hypervisor), the serial console, the T3 forwarder
// and the control API used by all other t3jail commands.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/langsjo/t3jail/internal/chv"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/hostnets"
	"github.com/langsjo/t3jail/internal/jailnet"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/vmspec"
	"github.com/langsjo/t3jail/internal/vsock"
)

// RunSpec is written by `t3jail start` and read by the daemon.
type RunSpec struct {
	Resources  vmspec.Resources
	Firmware   string
	T3HostPort uint16
}

func (s RunSpec) Write(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ReadRunSpec(path string) (RunSpec, error) {
	var s RunSpec
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

const metaShareID = "fs-meta"

type Daemon struct {
	inst paths.Instance
	spec RunSpec
	self string
	abi  guestabi.ABI
	log  *slog.Logger
	ch   *chv.Client
	// helper is the API of the netns helper that owns the network filter.
	helper *control.Client

	mu         sync.Mutex
	state      control.State
	guestReady bool
	started    time.Time

	stopReq chan control.StopRequest
	vmDone  chan struct{}
}

func New(inst paths.Instance, spec RunSpec, self string, log *slog.Logger) *Daemon {
	return &Daemon{
		inst:    inst,
		spec:    spec,
		self:    self,
		abi:     guestabi.Get(),
		log:     log,
		ch:      chv.NewClient(inst.CHAPISocket()),
		helper:  control.NewClient(inst.NetnsSocket()),
		state:   control.StateBooting,
		started: time.Now(),
		stopReq: make(chan control.StopRequest, 1),
		vmDone:  make(chan struct{}),
	}
}

// Run boots the VM and supervises it until it stops.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lock, err := lockFile(d.inst.DaemonLock())
	if err != nil {
		return err
	}
	defer lock.Close()

	// Entries allowed only for the previous run end with it.
	os.Remove(d.inst.EphemeralNetworkList())
	defer os.Remove(d.inst.EphemeralNetworkList())

	if err := d.writeMeta(); err != nil {
		return err
	}
	if err := startVirtiofsd(ctx, d.inst.FSSocket(d.abi.MetaTag), d.inst.MetaDir(), true, d.log); err != nil {
		return err
	}

	pasta, err := d.startVM()
	if err != nil {
		return err
	}
	var vmErr error
	go func() {
		vmErr = pasta.Wait()
		close(d.vmDone)
	}()

	if err := d.waitForCH(ctx); err != nil {
		pasta.Process.Kill()
		<-d.vmDone
		return err
	}
	go d.watchHostNets(ctx)
	if err := d.startConsole(); err != nil {
		d.log.Error("serial console unavailable", "err", err)
	}
	if err := d.startT3Forwarder(ctx); err != nil {
		d.log.Error("T3 forwarder unavailable", "err", err)
	}
	srv, err := d.serveControl()
	if err != nil {
		pasta.Process.Kill()
		<-d.vmDone
		return err
	}
	go d.pollGuestReady(ctx)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	select {
	case <-d.vmDone:
		d.log.Info("VM exited", "err", vmErr)
	case req := <-d.stopReq:
		d.shutdown(req, pasta)
	case sig := <-sigs:
		d.log.Info("received signal, stopping VM", "signal", sig)
		d.shutdown(control.StopRequest{Timeout: 30 * time.Second}, pasta)
	}

	// Let a pending stop request receive its response.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	srv.Shutdown(shutdownCtx)
	d.removeSockets()
	return nil
}

func (d *Daemon) writeMeta() error {
	pub, err := os.ReadFile(d.inst.SSHKey() + ".pub")
	if err != nil {
		return fmt.Errorf("reading SSH public key (run `t3jail init`): %w", err)
	}
	if err := writeFileAtomic(filepath.Join(d.inst.MetaDir(), d.abi.AuthorizedKeysFile), pub, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(d.inst.MetaDir(), d.abi.MountsManifestFile), []byte("[]\n"), 0o644)
}

func (d *Daemon) startVM() (*exec.Cmd, error) {
	res := d.spec.Resources
	launch := chv.LaunchSpec{
		APISocket:    d.inst.CHAPISocket(),
		SerialSocket: d.inst.SerialSocket(),
		VsockSocket:  d.inst.VsockSocket(),
		VsockCID:     d.abi.VsockCID,
		Disk:         d.inst.Disk(),
		Firmware:     d.spec.Firmware,
		CPUs:         res.CPUs,
		MaxCPUs:      res.MaxCPUs,
		Memory:       res.SplitMemory(),
		Shares:       []chv.Share{{ID: metaShareID, Tag: d.abi.MetaTag, Socket: d.inst.FSSocket(d.abi.MetaTag)}},
	}
	for _, sock := range []string{d.inst.CHAPISocket(), d.inst.SerialSocket(), d.inst.VsockSocket()} {
		os.Remove(sock)
	}
	hostNets, err := hostnets.Current()
	if err != nil {
		return nil, fmt.Errorf("reading host networks: %w", err)
	}
	specPath := filepath.Join(d.inst.Runtime, "helper.json")
	data, err := json.Marshal(jailnet.HelperSpec{
		Launch:               launch,
		NetworkList:          d.inst.NetworkList(),
		EphemeralNetworkList: d.inst.EphemeralNetworkList(),
		DNSLog:               d.inst.DNSLog(),
		APISocket:            d.inst.NetnsSocket(),
		HostNets:             hostNets,
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		return nil, err
	}

	cmd := exec.Command("pasta", jailnet.PastaArgs(d.self, "__netns-helper", specPath)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting pasta: %w", err)
	}
	d.log.Info("started VM", "pasta_pid", cmd.Process.Pid, "cpus", res.CPUs, "memory", res.Memory)
	return cmd, nil
}

func (d *Daemon) waitForCH(ctx context.Context) error {
	deadline := time.After(30 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := d.ch.Ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-d.vmDone:
			return errors.New("cloud-hypervisor exited during startup; see the daemon log")
		case <-deadline:
			return fmt.Errorf("cloud-hypervisor API did not come up: %w", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *Daemon) startConsole() error {
	// Keep the previous run's log for debugging.
	os.Rename(d.inst.SerialLog(), d.inst.SerialLog()+".1")
	logFile, err := os.OpenFile(d.inst.SerialLog(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	serial, err := dialRetry(d.inst.SerialSocket(), 5*time.Second)
	if err != nil {
		logFile.Close()
		return err
	}
	l, err := listenUnix(d.inst.ConsoleSocket())
	if err != nil {
		serial.Close()
		logFile.Close()
		return err
	}
	mux := newSerialMux(serial, logFile, d.log)
	go func() {
		mux.pump()
		l.Close()
		logFile.Close()
	}()
	go mux.serve(l)
	return nil
}

func (d *Daemon) startT3Forwarder(ctx context.Context) error {
	dial := func(ctx context.Context) (net.Conn, error) {
		return vsock.Dial(ctx, d.inst.VsockSocket(), d.abi.VsockT3Port)
	}
	tcp, err := net.Listen("tcp", d.t3Addr())
	if err != nil {
		return fmt.Errorf("listening on %s: %w", d.t3Addr(), err)
	}
	unixL, err := listenUnix(d.inst.T3Socket())
	if err != nil {
		tcp.Close()
		return err
	}
	go vsock.Forward(ctx, tcp, dial, d.log)
	go vsock.Forward(ctx, unixL, dial, d.log)
	return nil
}

func (d *Daemon) t3Addr() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(d.spec.T3HostPort)))
}

func (d *Daemon) serveControl() (*http.Server, error) {
	l, err := listenUnix(d.inst.ControlSocket())
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /v1/status", control.Handler(d.status))
	mux.Handle("POST /v1/stop", control.Handler(d.stop))
	mux.Handle("POST /v1/net/reload", control.Handler(d.reloadNetwork))
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	return srv, nil
}

func (d *Daemon) status(ctx context.Context, _ control.Empty) (control.Status, error) {
	d.mu.Lock()
	st := control.Status{
		State:      d.state,
		PID:        os.Getpid(),
		GuestReady: d.guestReady,
		MaxCPUs:    d.spec.Resources.MaxCPUs,
		MaxMemory:  uint64(d.spec.Resources.MaxMemory),
		BootMemory: uint64(d.spec.Resources.SplitMemory().Boot),
		T3URL:      "http://" + d.t3Addr(),
		Started:    d.started,
	}
	d.mu.Unlock()
	if info, err := d.ch.Info(ctx); err == nil {
		st.CPUs = info.Config.CPUs.BootVCPUs
		st.Memory = info.MemoryActualSize
	}
	return st, nil
}

// stop asks the main loop to shut down and waits until the VM is gone.
func (d *Daemon) stop(ctx context.Context, req control.StopRequest) (control.Empty, error) {
	select {
	case d.stopReq <- req:
	default: // a stop is already in progress
	}
	select {
	case <-d.vmDone:
		return control.Empty{}, nil
	case <-ctx.Done():
		return control.Empty{}, ctx.Err()
	}
}

func (d *Daemon) shutdown(req control.StopRequest, pasta *exec.Cmd) {
	d.setState(control.StateStopping)
	if !req.Force {
		d.log.Info("asking the guest to power off", "timeout", req.Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.ch.PowerButton(ctx)
		cancel()
		if err == nil && d.waitVM(req.Timeout) {
			return
		}
		d.log.Warn("guest did not power off in time", "err", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	d.ch.ShutdownVMM(ctx)
	cancel()
	if d.waitVM(5 * time.Second) {
		return
	}
	d.log.Warn("killing the VM")
	pasta.Process.Kill()
	<-d.vmDone
}

func (d *Daemon) waitVM(timeout time.Duration) bool {
	select {
	case <-d.vmDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// pollGuestReady marks the guest ready once its SSH server sends a banner.
func (d *Daemon) pollGuestReady(ctx context.Context) {
	for {
		if guestSSHAnswers(ctx, d.inst.VsockSocket(), d.abi.VsockSSHPort) {
			d.mu.Lock()
			d.guestReady = true
			if d.state == control.StateBooting {
				d.state = control.StateRunning
			}
			d.mu.Unlock()
			d.log.Info("guest is ready")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-d.vmDone:
			return
		case <-time.After(time.Second):
		}
	}
}

func guestSSHAnswers(ctx context.Context, socket string, port uint32) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := vsock.Dial(ctx, socket, port)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	banner := make([]byte, 4)
	_, err = io.ReadFull(conn, banner)
	return err == nil && string(banner) == "SSH-"
}

func (d *Daemon) setState(s control.State) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
}

func (d *Daemon) removeSockets() {
	for _, s := range []string{d.inst.ControlSocket(), d.inst.NetnsSocket(), d.inst.ConsoleSocket(), d.inst.T3Socket(), d.inst.CHAPISocket(), d.inst.SerialSocket(), d.inst.VsockSocket()} {
		os.Remove(s)
	}
}

// startVirtiofsd shares dir under the socket; it exits when the VM disconnects.
func startVirtiofsd(ctx context.Context, socket, dir string, readOnly bool, log *slog.Logger) error {
	os.Remove(socket)
	args := []string{"--socket-path", socket, "--shared-dir", dir, "--sandbox", "namespace", "--cache", "never"}
	if readOnly {
		args = append(args, "--readonly")
	}
	cmd := exec.CommandContext(ctx, "virtiofsd", args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting virtiofsd: %w", err)
	}
	go cmd.Wait()
	// The socket must exist before cloud-hypervisor connects to it.
	for range 50 {
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("virtiofsd did not create %s", socket)
}

func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("the VM is already running")
	}
	return f, nil
}

func listenUnix(path string) (net.Listener, error) {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func dialRetry(path string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil || time.Now().After(deadline) {
			return conn, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
