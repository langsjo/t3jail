package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/paths"
	"github.com/langsjo/t3jail/internal/sshx"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh [-- command...]",
		Short: "Open a shell (or run a command) in the VM",
		RunE: func(cmd *cobra.Command, args []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			argv, err := sshArgv(inst, args...)
			if err != nil {
				return err
			}
			sshPath, err := exec.LookPath("ssh")
			if err != nil {
				return err
			}
			return syscall.Exec(sshPath, argv, os.Environ())
		},
	}
}

// sshArgv rewrites the ssh_config (the executable path may have changed since
// the last run) and returns the ssh command line.
func sshArgv(inst paths.Instance, args ...string) ([]string, error) {
	self, err := selfExe()
	if err != nil {
		return nil, err
	}
	cfg := sshx.RenderConfig(sshx.ConfigParams{
		User:         guestabi.Get().GuestUser,
		KeyPath:      inst.SSHKey(),
		KnownHosts:   inst.KnownHosts(),
		ControlPath:  inst.SSHControlPath(),
		ProxyCommand: []string{self, "__vsock-proxy", fmt.Sprint(guestabi.Get().VsockSSHPort)},
	})
	if err := os.WriteFile(inst.SSHConfig(), []byte(cfg), 0o600); err != nil {
		return nil, err
	}
	return append([]string{"ssh", "-F", inst.SSHConfig(), sshx.HostAlias}, args...), nil
}

const detachKey = 0x1d // Ctrl-]

func newConsoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Attach to the VM's serial console (Ctrl-] detaches)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			conn, err := net.Dial("unix", inst.ConsoleSocket())
			if err != nil {
				return errors.New("the VM is not running")
			}
			defer conn.Close()
			fmt.Fprintln(os.Stderr, "Connected to the serial console. Press Ctrl-] to detach; press Enter for a prompt.")
			if term.IsTerminal(int(os.Stdin.Fd())) {
				old, err := term.MakeRaw(int(os.Stdin.Fd()))
				if err != nil {
					return err
				}
				defer term.Restore(int(os.Stdin.Fd()), old)
			}
			done := make(chan struct{}, 2)
			go func() {
				io.Copy(os.Stdout, conn)
				done <- struct{}{}
			}()
			go func() {
				copyUntilDetach(conn, os.Stdin)
				done <- struct{}{}
			}()
			<-done
			fmt.Fprint(os.Stderr, "\r\nDetached.\r\n")
			return nil
		},
	}
}

// copyUntilDetach forwards input until the detach key is pressed.
func copyUntilDetach(dst io.Writer, src io.Reader) {
	buf := make([]byte, 1024)
	for {
		n, err := src.Read(buf)
		if i := bytes.IndexByte(buf[:n], detachKey); i >= 0 {
			dst.Write(buf[:i])
			return
		}
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func newLogsCmd() *cobra.Command {
	var which string
	var follow, raw bool
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show the serial console, DNS or daemon log",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			files := map[string]string{"serial": inst.SerialLog(), "dns": inst.DNSLog(), "daemon": inst.DaemonLog()}
			path, ok := files[which]
			if !ok {
				return fmt.Errorf("unknown log %q (serial, dns or daemon)", which)
			}
			var out io.Writer = termsafe.Writer{W: os.Stdout}
			if raw {
				out = os.Stdout
			}
			return printFile(path, out, follow)
		},
	}
	cmd.Flags().StringVar(&which, "log", "serial", "which log: serial, dns or daemon")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().BoolVar(&raw, "raw", false, "print guest output without escaping terminal control sequences")
	return cmd
}

// printFile prints path, and with follow keeps printing what is appended. When
// the path is replaced (the daemon rotates logs on each start), it switches to
// the new file.
func printFile(path string, out io.Writer, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()
	for {
		if _, err := io.Copy(out, f); err != nil {
			return err
		}
		if !follow {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
		if replaced(f, path) {
			// Read what was written to the old file in the meantime first.
			if _, err := io.Copy(out, f); err != nil {
				return err
			}
			if next, err := os.Open(path); err == nil {
				f.Close()
				f = next
			}
		}
	}
}

func replaced(f *os.File, path string) bool {
	open, err := f.Stat()
	if err != nil {
		return false
	}
	current, err := os.Stat(path)
	return err == nil && !os.SameFile(open, current)
}
