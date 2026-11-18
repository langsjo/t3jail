// Package jailnet runs inside the user+network namespace created by pasta. It
// owns the VM's only network path: a tap device whose traffic is routed and filtered through this namespace before pasta turns
// it into ordinary host sockets.
package jailnet

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/langsjo/t3jail/internal/chv"
	"github.com/langsjo/t3jail/internal/guestabi"
	"github.com/langsjo/t3jail/internal/netrules"
)

const (
	TapName  = "tap0"
	GuestMAC = "52:54:00:7a:30:02"
	// DNSForward is the address pasta maps to the host's resolver. It is only
	// reachable from inside this namespace, never from the guest.
	DNSForward = "169.254.53.53"
	// Namespace side of pasta. Pinned so that it cannot collide with the
	// guest link, whatever addresses the host uses. TEST-NET-2 is blocked as a
	// destination anyway.
	pastaAddr4    = "198.51.100.2"
	pastaGateway4 = "198.51.100.1"
)

// HelperSpec is handed from the daemon to the helper as a JSON file.
type HelperSpec struct {
	Launch      chv.LaunchSpec
	NetworkList string
	// EphemeralNetworkList holds entries allowed until the VM stops. It may
	// not exist.
	EphemeralNetworkList string
	NetworkMode          netrules.ModeFiles
	DNSLog               string
	// APISocket is where the helper serves the API the daemon uses to
	// change the filter while the VM runs.
	APISocket string
	// HostNets is the initial set of host networks; the daemon sends
	// updates through the API.
	HostNets []netip.Prefix
}

// PastaArgs returns the pasta command line that creates the namespaces and
// runs helper inside them. No ports are forwarded in either direction, and the
// host's loopback is not reachable through the gateway address.
func PastaArgs(helper ...string) []string {
	args := []string{
		"--config-net", "--foreground", "--quiet",
		"--tcp-ports", "none", "--udp-ports", "none",
		"--tcp-ns", "none", "--udp-ns", "none",
		"--map-host-loopback", "none",
		"--address", pastaAddr4, "--netmask", "24", "--gateway", pastaGateway4,
		"--dns-forward", DNSForward,
		"--",
	}
	return append(args, helper...)
}

// RunHelper sets up the namespace and runs cloud-hypervisor until it exits.
func RunHelper(specPath string) error {
	// Pdeathsig is tied to the thread that started the child.
	runtime.LockOSThread()

	data, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	var spec HelperSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}

	tap, err := openTap(TapName)
	if err != nil {
		return fmt.Errorf("creating %s: %w", TapName, err)
	}
	if err := configureLinks(guestabi.Get()); err != nil {
		return err
	}
	if _, err := startJail(guestabi.Get(), spec); err != nil {
		return fmt.Errorf("network filter: %w", err)
	}

	launch := spec.Launch
	launch.TapFD = 3 // first entry of ExtraFiles
	launch.GuestMAC = GuestMAC
	args, err := chv.Args(launch)
	if err != nil {
		return err
	}
	// We are root in this user namespace; drop every capability so a VMM
	// compromise gains nothing beyond the host user's own rights.
	cmd := exec.Command("setpriv", append([]string{
		"--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs",
		"--", "cloud-hypervisor",
	}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.ExtraFiles = []*os.File{tap}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return err
	}
	tap.Close()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		cmd.Process.Kill()
	}()
	return cmd.Wait()
}

func openTap(name string) (*os.File, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		f.Close()
		return nil, err
	}
	// cloud-hypervisor expects a vnet header on fds it is given.
	ifr.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_VNET_HDR)
	if err := unix.IoctlIfreq(int(f.Fd()), unix.TUNSETIFF, ifr); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func configureLinks(abi guestabi.ABI) error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return err
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		return err
	}
	tap, err := netlink.LinkByName(TapName)
	if err != nil {
		return err
	}
	addrs := []*netlink.Addr{
		{IPNet: &net.IPNet{IP: abi.Gateway4.AsSlice(), Mask: net.CIDRMask(abi.PrefixLen4, 32)}},
		// No duplicate address detection: nothing else is on this link.
		{IPNet: &net.IPNet{IP: abi.Gateway6.AsSlice(), Mask: net.CIDRMask(abi.PrefixLen6, 128)}, Flags: unix.IFA_F_NODAD},
	}
	for _, a := range addrs {
		if err := netlink.AddrAdd(tap, a); err != nil {
			return fmt.Errorf("adding %v to %s: %w", a.IPNet, TapName, err)
		}
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return err
	}
	for _, sysctl := range []string{
		"/proc/sys/net/ipv4/ip_forward",
		"/proc/sys/net/ipv6/conf/all/forwarding",
	} {
		if err := os.WriteFile(sysctl, []byte("1"), 0); err != nil {
			return fmt.Errorf("enabling forwarding: %w", err)
		}
	}
	return nil
}
