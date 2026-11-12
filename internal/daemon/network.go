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
	hostnets.Watch(ctx, func(prefixes []netip.Prefix) error {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err := d.helper.Call(callCtx, http.MethodPut, "/v1/hostnets", jailnet.HostNetsRequest{Prefixes: prefixes}, nil)
		if err == nil {
			d.log.Info("updated host networks", "prefixes", prefixes)
		}
		return err
	}, func(err error) {
		d.log.Error("watching host networks", "err", err)
	})
}
