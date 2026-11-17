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
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/langsjo/t3jail/internal/portfwd"
	"github.com/langsjo/t3jail/internal/vmspec"
	"github.com/langsjo/t3jail/internal/vsock"
)

// RunSpec is written by `t3jail start` and read by the daemon.
type RunSpec struct {
	Resources vmspec.Resources
	// NestedVirt lets the guest use KVM itself.
	NestedVirt bool
	// Firmware boots the VM's disk; Rescue, if set, boots the rescue system
	// instead.
	Firmware string
	Rescue   *RescueBoot
	// Host addresses forwarded to the VM's T3 server; the first is the one
	// shown in status and used for pairing links.
	T3Listen []netip.AddrPort
	// RequestReminder is the interval between notifications while network
	// requests are pending; 0 notifies only for new requests.
	RequestReminder time.Duration
}

// RescueBoot is a direct kernel boot of the rescue system.
type RescueBoot struct {
	Kernel    string
	Initramfs string
	Cmdline   string
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

	// runCtx ends when the daemon stops; per-share virtiofsd processes live
	// under it.
	runCtx   context.Context
	sharesMu sync.Mutex
	shares   map[string]*share // by guest path

	requests requestQueue
	ports    *portForwards

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
		shares:  map[string]*share{},
		vmDone:  make(chan struct{}),
	}
}

// Run boots the VM and supervises it until it stops.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.runCtx = ctx
	d.ports = newPortForwards(ctx, d.dialGuestSSH, d.log)

	lock, err := lockFile(d.inst.DaemonLock())
	if err != nil {
		return err
	}
	defer lock.Close()

	// Entries allowed, and filter switches made, only for the previous run end
	// with it.
	os.Remove(d.inst.EphemeralNetworkList())
	os.Remove(d.inst.NetworkFilterOverride())
	defer os.Remove(d.inst.EphemeralNetworkList())
	defer os.Remove(d.inst.NetworkFilterOverride())

	// A persisted mount may be what keeps the VM from booting, so the rescue
	// system starts without them.
	if d.spec.Rescue == nil {
		if err := d.startPersistedShares(); err != nil {
			return err
		}
	}
	if err := d.writeMeta(); err != nil {
		return err
	}
	if err := startVirtiofsd(ctx, d.inst.FSSocket(d.abi.MetaTag), d.inst.MetaDir(), true, nil); err != nil {
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
	if err := d.serveRequests(ctx); err != nil {
		d.log.Error("network requests unavailable", "err", err)
	}
	srv, err := d.serveControl()
	if err != nil {
		pasta.Process.Kill()
		<-d.vmDone
		return err
	}
	go d.pollGuestReady(ctx)
	if d.spec.Rescue == nil {
		go d.ports.keepOpen(d.waitGuestSSH, func() ([]control.PortForward, error) {
			p, err := portfwd.Load(d.inst.PortsFile())
			return p.Forwards, err
		})
	}

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
	manifest, err := d.manifest()
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(d.inst.MetaDir(), d.abi.MountsManifestFile), manifest, 0o644)
}

func (d *Daemon) startVM() (*exec.Cmd, error) {
	res := d.spec.Resources
	launch := chv.LaunchSpec{
		APISocket:    d.inst.CHAPISocket(),
		SerialSocket: d.inst.SerialSocket(),
		VsockSocket:  d.inst.VsockSocket(),
		VsockCID:     d.abi.VsockCID,
		Disk:         d.inst.Disk(),
		CPUs:         res.CPUs,
		MaxCPUs:      res.MaxCPUs,
		Nested:       d.spec.NestedVirt,
		Memory:       res.SplitMemory(),
		Shares:       d.bootShares(),
	}
	if r := d.spec.Rescue; r != nil {
		launch.Kernel, launch.Initramfs, launch.Cmdline = r.Kernel, r.Initramfs, r.Cmdline
	} else {
		launch.Firmware = d.spec.Firmware
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
		Launch:                launch,
		NetworkList:           d.inst.NetworkList(),
		EphemeralNetworkList:  d.inst.EphemeralNetworkList(),
		NetworkOffMarker:      d.inst.NetworkOffMarker(),
		NetworkFilterOverride: d.inst.NetworkFilterOverride(),
		DNSLog:                d.inst.DNSLog(),
		APISocket:             d.inst.NetnsSocket(),
		HostNets:              hostNets,
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
	if len(d.spec.T3Listen) == 0 {
		return errors.New("no T3 listen address in the run spec")
	}
	var listeners []net.Listener
	closeAll := func() {
		for _, l := range listeners {
			l.Close()
		}
	}
	for _, addr := range d.spec.T3Listen {
		l, err := listenFreebind(ctx, addr)
		if err != nil {
			closeAll()
			return fmt.Errorf("listening on %s for T3: %w", addr, err)
		}
		listeners = append(listeners, l)
	}
	unixL, err := listenUnix(d.inst.T3Socket())
	if err != nil {
		closeAll()
		return err
	}
	for _, l := range append(listeners, unixL) {
		go vsock.Forward(ctx, l, dial, d.log)
	}
	return nil
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
	mux.Handle("GET /v1/net/requests", control.Handler(d.listRequests))
	mux.Handle("POST /v1/net/requests/answer", control.Handler(d.answerRequests))
	mux.Handle("POST /v1/resize", control.Handler(d.resize))
	mux.Handle("POST /v1/disk/resize", control.Handler(d.resizeDisk))
	mux.Handle("GET /v1/mounts", control.Handler(d.listMounts))
	mux.Handle("POST /v1/mounts", control.Handler(d.addMount))
	mux.Handle("POST /v1/mounts/remove", control.Handler(d.removeMount))
	mux.Handle("GET /v1/ports", control.Handler(d.listPorts))
	mux.Handle("POST /v1/ports", control.Handler(d.openPort))
	mux.Handle("POST /v1/ports/close", control.Handler(d.closePort))
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
		Rescue:     d.spec.Rescue != nil,
		MaxCPUs:    d.spec.Resources.MaxCPUs,
		MaxMemory:  uint64(d.spec.Resources.MaxMemory),
		BootMemory: uint64(d.spec.Resources.SplitMemory().Boot),
		T3URL:      "http://" + d.spec.T3Listen[0].String(),
		Started:    d.started,
	}
	for _, addr := range d.spec.T3Listen {
		st.T3Listen = append(st.T3Listen, addr.String())
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
	if !d.waitGuestSSH(ctx) {
		return
	}
	d.mu.Lock()
	d.guestReady = true
	if d.state == control.StateBooting {
		d.state = control.StateRunning
	}
	d.mu.Unlock()
	d.log.Info("guest is ready")
}

// waitGuestSSH waits until the guest's SSH server sends a banner. It returns
// false if the VM or the daemon stopped first.
func (d *Daemon) waitGuestSSH(ctx context.Context) bool {
	for !guestSSHAnswers(ctx, d.inst.VsockSocket(), d.abi.VsockSSHPort) {
		select {
		case <-ctx.Done():
			return false
		case <-d.vmDone:
			return false
		case <-time.After(time.Second):
		}
	}
	return true
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
	for _, s := range []string{d.inst.ControlSocket(), d.inst.NetnsSocket(), d.inst.ConsoleSocket(), d.inst.T3Socket(), d.inst.CHAPISocket(), d.inst.SerialSocket(), d.inst.VsockSocket(), d.inst.VsockHostSocket(d.abi.VsockRequestPort)} {
		os.Remove(s)
	}
}

// startVirtiofsd shares dir under the socket; it exits when the VM disconnects.
func startVirtiofsd(ctx context.Context, socket, dir string, readOnly bool, extraArgs []string) error {
	os.Remove(socket)
	args := []string{"--socket-path", socket, "--shared-dir", dir, "--sandbox", "namespace", "--cache", "never"}
	args = append(args, extraArgs...)
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
