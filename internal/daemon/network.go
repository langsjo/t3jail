package daemon

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/hostnets"
	"github.com/langsjo/t3jail/internal/jailnet"
)

func (d *Daemon) reloadNetwork(ctx context.Context, _ control.Empty) (control.Empty, error) {
	err := d.helper.Call(ctx, http.MethodPost, "/v1/reload", nil, nil)
	if err == nil {
		d.log.Info("reloaded network rules")
	}
	return control.Empty{}, err
}

// watchHostNets keeps the helper's view of the host networks current, so a
// network joined while the VM runs (a VPN, another Wi-Fi) is blocked too.
func (d *Daemon) watchHostNets(ctx context.Context) {
	hostnets.Watch(ctx, func(prefixes []netip.Prefix) {
		for attempt := 0; ; attempt++ {
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := d.helper.Call(callCtx, http.MethodPut, "/v1/hostnets", jailnet.HostNetsRequest{Prefixes: prefixes}, nil)
			cancel()
			if err == nil {
				d.log.Info("updated host networks", "prefixes", prefixes)
				return
			}
			// The helper's API may not be up yet right after boot.
			if attempt == 10 || ctx.Err() != nil {
				d.log.Error("updating host networks", "err", err)
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}, func(err error) {
		d.log.Error("watching host networks", "err", err)
	})
}
