package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/langsjo/t3jail/internal/chv"
	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/diskimg"
)

// resize asks cloud-hypervisor for new sizes. Plugging happens in the
// background; shrinking memory can take a while as the guest moves pages.
func (d *Daemon) resize(ctx context.Context, req control.ResizeRequest) (control.Empty, error) {
	if err := d.spec.Resources.CheckResize(req.CPUs, config.ByteSize(req.Memory)); err != nil {
		return control.Empty{}, err
	}
	if err := d.ch.Resize(ctx, req.CPUs, req.Memory); err != nil {
		return control.Empty{}, fmt.Errorf("cloud-hypervisor: %w", err)
	}
	d.log.Info("resizing", "cpus", req.CPUs, "memory", req.Memory)
	return control.Empty{}, nil
}

// resizeDisk grows the disk image through cloud-hypervisor, which also tells
// the guest kernel. Growing the partition and filesystem is up to the guest.
func (d *Daemon) resizeDisk(ctx context.Context, req control.DiskResizeRequest) (control.Empty, error) {
	if d.spec.Rescue != nil {
		return control.Empty{}, errors.New("not while the rescue system runs; stop it first")
	}
	fi, err := os.Stat(d.inst.Disk())
	if err != nil {
		return control.Empty{}, err
	}
	if err := diskimg.CheckGrow(fi.Size(), int64(req.Size)); err != nil {
		return control.Empty{}, err
	}
	if err := d.ch.ResizeDisk(ctx, chv.DiskID, req.Size); err != nil {
		return control.Empty{}, fmt.Errorf("cloud-hypervisor: %w", err)
	}
	d.log.Info("resized disk", "bytes", req.Size)
	return control.Empty{}, nil
}
