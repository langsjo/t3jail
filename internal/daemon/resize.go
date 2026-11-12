package daemon

import (
	"context"
	"fmt"

	"github.com/langsjo/t3jail/internal/config"
	"github.com/langsjo/t3jail/internal/control"
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
