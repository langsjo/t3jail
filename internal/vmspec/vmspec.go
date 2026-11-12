// Package vmspec decides how a requested CPU/RAM size maps onto
// cloud-hypervisor's boot and hotplug resources.
package vmspec

import (
	"fmt"

	"github.com/langsjo/t3jail/internal/config"
)

// MemoryAlign is the granularity of virtio-mem hotplug.
const MemoryAlign = 128 * config.MiB

// MinBootMemory keeps enough non-movable memory for the kernel itself.
const MinBootMemory = 1 * config.GiB

// Resources is what the user asked for at start.
type Resources struct {
	CPUs      int
	MaxCPUs   int
	Memory    config.ByteSize
	MaxMemory config.ByteSize
}

// Memory is the boot/hotplug split given to cloud-hypervisor.
type Memory struct {
	// Boot memory is fixed for the lifetime of the VM; it cannot be unplugged.
	Boot config.ByteSize
	// Hotplug is the virtio-mem region: Max - Boot.
	Hotplug config.ByteSize
	// Plugged is how much of Hotplug is plugged at start: Memory - Boot.
	Plugged config.ByteSize
}

func (r Resources) Validate() error {
	switch {
	case r.CPUs < 1:
		return fmt.Errorf("cpus must be at least 1")
	case r.CPUs > r.MaxCPUs:
		return fmt.Errorf("cpus (%d) exceeds max cpus (%d)", r.CPUs, r.MaxCPUs)
	case r.Memory%MemoryAlign != 0 || r.MaxMemory%MemoryAlign != 0:
		return fmt.Errorf("memory sizes must be multiples of %v", MemoryAlign)
	case r.Memory < MinBootMemory:
		return fmt.Errorf("memory must be at least %v", MinBootMemory)
	case r.Memory > r.MaxMemory:
		return fmt.Errorf("memory (%v) exceeds max memory (%v)", r.Memory, r.MaxMemory)
	}
	return nil
}

// SplitMemory picks the boot portion as a quarter of the start size (at least
// MinBootMemory). The guest onlines hotplugged memory as movable up to ~3x the
// boot memory (auto_movable_ratio=301), so this keeps everything plugged at
// start unpluggable; memory can later shrink back down to Boot.
func (r Resources) SplitMemory() Memory {
	boot := alignUp(r.Memory/4, MemoryAlign)
	boot = max(boot, MinBootMemory)
	boot = min(boot, r.Memory)
	return Memory{Boot: boot, Hotplug: r.MaxMemory - boot, Plugged: r.Memory - boot}
}

func alignUp(v, align config.ByteSize) config.ByteSize {
	return (v + align - 1) / align * align
}

// CheckResize validates a live resize against the limits fixed at start. Zero
// leaves that resource as it is.
func (r Resources) CheckResize(cpus int, memory config.ByteSize) error {
	if cpus != 0 && (cpus < 1 || cpus > r.MaxCPUs) {
		return fmt.Errorf("cpus must be between 1 and %d (max cpus, set at start)", r.MaxCPUs)
	}
	if memory == 0 {
		return nil
	}
	boot := r.SplitMemory().Boot
	switch {
	case memory%MemoryAlign != 0:
		return fmt.Errorf("memory must be a multiple of %v", MemoryAlign)
	case memory < boot:
		return fmt.Errorf("memory cannot go below %v, the part fixed at start", boot)
	case memory > r.MaxMemory:
		return fmt.Errorf("memory cannot exceed %v (max memory, set at start)", r.MaxMemory)
	}
	return nil
}
