package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/daemon"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/termsafe"
	"github.com/langsjo/t3jail/internal/vmspec"
)

const bootTimeout = 3 * time.Minute

func newStartCmd() *cobra.Command {
	var res struct {
		cpus, maxCPUs     int
		memory, maxMemory config.ByteSize
	}
	var rescue bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Boot the VM",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			if _, err := os.Stat(inst.Disk()); errors.Is(err, fs.ErrNotExist) {
				if err := initInstance(inst, 0, ""); err != nil {
					return err
				}
			}
			// The runtime directory is on tmpfs, so it is gone after a host
			// reboot even though the disk exists.
			if err := inst.Ensure(); err != nil {
				return err
			}
			if err := ensureNetworkList(inst); err != nil {
				return err
			}
			cfg, err := config.Load(inst.ConfigFile())
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			resources := resolveResources(cfg, hostCPUs(), hostMemory())
			if flags.Changed("cpu") {
				resources.CPUs = res.cpus
			}
			if flags.Changed("mem") {
				resources.Memory = res.memory
			}
			if flags.Changed("max-cpu") {
				resources.MaxCPUs = res.maxCPUs
			}
			if flags.Changed("max-mem") {
				resources.MaxMemory = res.maxMemory
			}
			if err := resources.Validate(); err != nil {
				return err
			}
			listen, err := cfg.T3.ListenAddrs()
			if err != nil {
				return err
			}
			spec := daemon.RunSpec{
				Resources:  resources,
				NestedVirt: cfg.NestedVirt,
				Firmware:   os.Getenv(envFirmware),
				T3Listen:   listen,

				RequestReminder: time.Duration(cfg.Net.RequestReminderSecs) * time.Second,
			}
			if rescue {
				spec.Resources = resources.ForRescue()
				spec.NestedVirt = false
				if spec.Rescue, err = buildRescue(inst); err != nil {
					return err
				}
				if err := os.Remove(inst.RescueKnownHosts()); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			return start(cmd.Context(), inst, spec)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&rescue, "rescue", false, "boot the rescue system, with the VM's disk attached but not mounted")
	f.IntVarP(&res.cpus, "cpu", "c", 0, "vCPUs at boot (default from config.toml)")
	f.VarP(&res.memory, "mem", "m", "RAM at boot, e.g. 8G (default from config.toml)")
	f.IntVar(&res.maxCPUs, "max-cpu", 0, "upper bound for live vCPU resizing")
	f.Var(&res.maxMemory, "max-mem", "upper bound for live RAM resizing")
	return cmd
}

// resolveResources fills unset maxima from the host.
func resolveResources(cfg config.Config, hostCPUs int, hostMem config.ByteSize) vmspec.Resources {
	r := vmspec.Resources{CPUs: cfg.CPUs, MaxCPUs: cfg.MaxCPUs, Memory: cfg.Memory, MaxMemory: cfg.MaxMemory}
	if r.MaxCPUs == 0 {
		r.MaxCPUs = max(hostCPUs, r.CPUs)
	}
	if r.MaxMemory == 0 {
		r.MaxMemory = max(hostMem/vmspec.MemoryAlign*vmspec.MemoryAlign, r.Memory)
	}
	return r
}

func hostCPUs() int { return runtime.NumCPU() }

func hostMemory() config.ByteSize {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	return config.ByteSize(uint64(info.Totalram) * uint64(info.Unit))
}

func start(ctx context.Context, inst paths.Instance, spec daemon.RunSpec) error {
	client := control.NewClient(inst.ControlSocket())
	if _, err := client.Status(ctx); err == nil {
		return errors.New("the VM is already running")
	}
	if err := preflight(spec); err != nil {
		return err
	}
	if err := spec.Write(runSpecPath(inst)); err != nil {
		return err
	}
	exited, err := spawnDaemon(inst)
	if err != nil {
		return err
	}

	what := "Booting"
	if spec.Rescue != nil {
		what = "Booting the rescue system"
	}
	fmt.Printf("%s (%d vCPUs, %v RAM)...\n", what, spec.Resources.CPUs, spec.Resources.Memory)
	deadline := time.Now().Add(bootTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			printLogTail("daemon log", inst.DaemonLog(), 30)
			printLogTail("serial console", inst.SerialLog(), 30)
			return errors.New("the VM failed to start")
		case <-time.After(500 * time.Millisecond):
		}
		st, err := client.Status(ctx)
		if err == nil && st.GuestReady && st.Rescue {
			fmt.Println("The rescue system is running. Its welcome message explains how to repair the VM.\n  Shell: t3jail console (as root) or t3jail ssh (sudo works)\n  Afterwards: t3jail stop && t3jail start")
			return nil
		}
		if err == nil && st.GuestReady {
			fmt.Printf("VM is running.\n  T3 Code: %s\n  Shell:   t3jail ssh\n", st.T3URL)
			for _, addr := range absentListenAddrs(st.T3Listen) {
				fmt.Printf("Note: this host has no address %s yet; T3 is reachable there once it does.\n", addr)
			}
			return nil
		}
	}
	printLogTail("serial console", inst.SerialLog(), 30)
	return fmt.Errorf("the guest did not become reachable within %v; it is still running (see `t3jail console`)", bootTimeout)
}

func runSpecPath(inst paths.Instance) string { return filepath.Join(inst.Runtime, "run.json") }

// spawnDaemon starts `t3jail __daemon` in its own session so it outlives this
// command. The returned channel is closed if it exits.
func spawnDaemon(inst paths.Instance) (<-chan struct{}, error) {
	self, err := selfExe()
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(inst.DaemonLog(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "\n=== t3jail daemon starting at %s\n", time.Now().Format(time.RFC3339))

	cmd := exec.Command(self, "__daemon")
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the daemon: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	return exited, nil
}

func preflight(spec daemon.RunSpec) error {
	for _, dev := range []string{"/dev/kvm", "/dev/net/tun"} {
		if err := unix.Access(dev, unix.R_OK|unix.W_OK); err != nil {
			return fmt.Errorf("%s is not accessible: %w", dev, err)
		}
	}
	for _, bin := range []string{"cloud-hypervisor", "virtiofsd", "pasta", "setpriv"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found in PATH", bin)
		}
	}
	if spec.NestedVirt {
		if err := checkHostNested("/sys/module"); err != nil {
			return err
		}
	}
	if spec.Rescue != nil {
		return nil
	}
	if spec.Firmware == "" {
		return fmt.Errorf("%s is not set; run t3jail through its Nix package", envFirmware)
	}
	if _, err := os.Stat(spec.Firmware); err != nil {
		return fmt.Errorf("firmware: %w", err)
	}
	return nil
}

// checkHostNested fails unless the loaded kvm_intel or kvm_amd module allows
// nested guests; without it the VM would boot with no usable /dev/kvm.
func checkHostNested(sysModule string) error {
	for _, mod := range []string{"kvm_intel", "kvm_amd"} {
		data, err := os.ReadFile(filepath.Join(sysModule, mod, "parameters", "nested"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		// kvm_intel reports Y/N, kvm_amd 1/0.
		if v := strings.TrimSpace(string(data)); v == "Y" || v == "1" {
			return nil
		}
		return fmt.Errorf("nested_virt is set but %s has nested virtualization off; enable it with the module option %s.nested=1 (NixOS: boot.extraModprobeConfig)", mod, mod)
	}
	return errors.New("nested_virt is set but neither kvm_intel nor kvm_amd is loaded")
}

func printLogTail(name, path string, lines int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	start := len(data)
	for n := 0; start > 0 && n <= lines; start-- {
		if data[start-1] == '\n' {
			n++
		}
	}
	fmt.Fprintf(os.Stderr, "--- last lines of the %s (%s):\n", name, path)
	os.Stderr.Write(termsafe.Escape(data[start:]))
	fmt.Fprintln(os.Stderr, "---")
}

func newStopCmd() *cobra.Command {
	var force bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Shut the VM down",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout+30*time.Second)
			defer cancel()
			err = control.NewClient(inst.ControlSocket()).Stop(ctx, control.StopRequest{Force: force, Timeout: timeout})
			if errors.Is(err, control.ErrNotRunning) {
				fmt.Println("The VM is not running.")
				return nil
			}
			if err != nil {
				return err
			}
			fmt.Println("The VM is stopped.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "kill the VM without a clean guest shutdown")
	cmd.Flags().DurationVar(&timeout, "timeout", time.Minute, "how long to wait for a clean guest shutdown")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the VM runs and with which resources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			st, err := control.NewClient(inst.ControlSocket()).Status(cmd.Context())
			if errors.Is(err, control.ErrNotRunning) {
				if asJSON {
					fmt.Println(`{"state":"stopped"}`)
				} else {
					fmt.Println("stopped")
				}
				return nil
			}
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(st)
			}
			state := string(st.State)
			if st.Rescue {
				state += ", rescue system"
			}
			fmt.Printf("state:   %s (guest ready: %v)\n", state, st.GuestReady)
			fmt.Printf("vCPUs:   %d (max %d)\n", st.CPUs, st.MaxCPUs)
			fmt.Printf("memory:  %v (boot %v, max %v)\n", config.ByteSize(st.Memory), config.ByteSize(st.BootMemory), config.ByteSize(st.MaxMemory))
			fmt.Printf("T3 Code: %s\n", st.T3URL)
			if len(st.T3Listen) > 1 || len(absentListenAddrs(st.T3Listen)) > 0 {
				fmt.Printf("listen:  %s\n", describeListen(st.T3Listen))
			}
			if state, err := filterState(inst, true); err == nil && describeFilterState(state) != "" {
				fmt.Printf("network: %s\n", describeFilterState(state))
			}
			fmt.Printf("uptime:  %s\n", time.Since(st.Started).Round(time.Second))
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
