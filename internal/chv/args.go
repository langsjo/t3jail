// Package chv launches and controls cloud-hypervisor.
package chv

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/langsjo/t3jail/internal/vmspec"
)

// LaunchSpec describes one VM run. Exactly one of Firmware or Kernel is set.
type LaunchSpec struct {
	APISocket    string
	SerialSocket string
	VsockSocket  string
	VsockCID     uint32
	Disk         string

	// Normal boot: UEFI firmware boots the disk's own bootloader.
	Firmware string
	// Rescue boot: direct kernel boot.
	Kernel    string
	Initramfs string
	Cmdline   string

	CPUs     int
	MaxCPUs  int
	Memory   vmspec.Memory
	Shares   []Share
	TapFD    int // 0 means no network device
	GuestMAC string
}

type Share struct {
	ID     string
	Tag    string
	Socket string
}

// FsQueueSize is the virtio-fs queue size used for every share.
const FsQueueSize = 1024

// Args returns the cloud-hypervisor command-line arguments.
func Args(s LaunchSpec) ([]string, error) {
	if (s.Firmware == "") == (s.Kernel == "") {
		return nil, fmt.Errorf("exactly one of firmware or kernel must be set")
	}
	args := []string{
		"--api-socket", "path=" + s.APISocket,
		"--cpus", fmt.Sprintf("boot=%d,max=%d", s.CPUs, s.MaxCPUs),
		// shared=on is required for virtio-fs and can only be set at boot.
		"--memory", kv(
			"size", byteCount(uint64(s.Memory.Boot)),
			"hotplug_method", "virtio-mem",
			"hotplug_size", byteCount(uint64(s.Memory.Hotplug)),
			"hotplugged_size", byteCount(uint64(s.Memory.Plugged)),
			"shared", "on",
		),
		"--disk", kv("path", s.Disk, "image_type", "raw"),
		"--serial", "socket=" + s.SerialSocket,
		"--console", "off",
		"--vsock", kv("cid", strconv.FormatUint(uint64(s.VsockCID), 10), "socket", s.VsockSocket),
		"--rng",
	}
	if s.Firmware != "" {
		args = append(args, "--firmware", s.Firmware)
	} else {
		args = append(args, "--kernel", s.Kernel, "--initramfs", s.Initramfs, "--cmdline", s.Cmdline)
	}
	if s.TapFD != 0 {
		args = append(args, "--net", kv("fd", strconv.Itoa(s.TapFD), "mac", s.GuestMAC))
	}
	for _, sh := range s.Shares {
		args = append(args, "--fs", fsArg(sh))
	}
	return args, nil
}

func fsArg(sh Share) string {
	return kv("tag", sh.Tag, "socket", sh.Socket, "id", sh.ID, "num_queues", "1", "queue_size", strconv.Itoa(FsQueueSize))
}

// kv joins key/value pairs into cloud-hypervisor's "k=v,k=v" syntax.
func kv(pairs ...string) string {
	var b strings.Builder
	for i := 0; i < len(pairs); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(pairs[i] + "=" + pairs[i+1])
	}
	return b.String()
}

func byteCount(n uint64) string { return strconv.FormatUint(n, 10) }
