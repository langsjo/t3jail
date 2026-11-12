package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/langsjo/t3jail/internal/chv"
	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/mounts"
)

// share is a running virtiofsd for one mount.
type share struct {
	mounts.Mount
	stop context.CancelFunc
}

func (d *Daemon) startShare(m mounts.Mount) error {
	ctx, cancel := context.WithCancel(d.runCtx)
	if err := startVirtiofsd(ctx, d.inst.FSSocket(m.Tag()), m.HostPath, m.ReadOnly, idMapArgs(d.abi.GuestUID, d.abi.GuestGID, os.Getuid(), os.Getgid())); err != nil {
		cancel()
		return err
	}
	d.shares[m.GuestPath] = &share{Mount: m, stop: cancel}
	return nil
}

func (d *Daemon) stopShare(s *share) {
	s.stop()
	os.Remove(d.inst.FSSocket(s.Tag()))
	delete(d.shares, s.GuestPath)
}

// startPersistedShares starts virtiofsd for every persisted mount whose host
// directory exists, so they can be attached at boot. The guest mounts them
// from the manifest written by writeMeta.
func (d *Daemon) startPersistedShares() error {
	persisted, err := mounts.Load(d.inst.MountsFile())
	if err != nil {
		return err
	}
	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	for _, m := range persisted.Mounts {
		real, err := mounts.CheckHostDir(m.HostPath)
		if err != nil {
			d.log.Warn("skipping persisted mount", "guest_path", m.GuestPath, "err", err)
			continue
		}
		m.HostPath = real
		if err := d.startShare(m); err != nil {
			return err
		}
	}
	return nil
}

// bootShares returns the shares to attach at boot, the meta share first.
func (d *Daemon) bootShares() []chv.Share {
	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	out := []chv.Share{{ID: metaShareID, Tag: d.abi.MetaTag, Socket: d.inst.FSSocket(d.abi.MetaTag)}}
	for _, s := range d.sortedShares() {
		out = append(out, chv.Share{ID: s.DeviceID(), Tag: s.Tag(), Socket: d.inst.FSSocket(s.Tag())})
	}
	return out
}

// manifest is what the guest's t3jail-host-setup mounts at boot.
func (d *Daemon) manifest() ([]byte, error) {
	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	type entry struct {
		Tag       string `json:"tag"`
		GuestPath string `json:"guestPath"`
		ReadOnly  bool   `json:"readOnly"`
	}
	entries := []entry{}
	for _, s := range d.sortedShares() {
		entries = append(entries, entry{s.Tag(), s.GuestPath, s.ReadOnly})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	return append(data, '\n'), err
}

func (d *Daemon) sortedShares() []*share {
	var out []*share
	for _, s := range d.shares {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b *share) int { return strings.Compare(a.GuestPath, b.GuestPath) })
	return out
}

// addMount attaches a share to the running VM. Mounting it inside the guest
// is left to the caller, which has SSH access.
func (d *Daemon) addMount(ctx context.Context, m mounts.Mount) (mounts.Mount, error) {
	if err := mounts.CheckGuestPath(m.GuestPath); err != nil {
		return m, err
	}
	real, err := mounts.CheckHostDir(m.HostPath)
	if err != nil {
		return m, err
	}
	m.HostPath = real

	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	for _, s := range d.shares {
		if s.GuestPath == m.GuestPath {
			return m, fmt.Errorf("%s is already shared from %s", m.GuestPath, s.HostPath)
		}
		if strings.HasPrefix(m.GuestPath, s.GuestPath+"/") || strings.HasPrefix(s.GuestPath, m.GuestPath+"/") {
			return m, fmt.Errorf("%s overlaps the mount at %s", m.GuestPath, s.GuestPath)
		}
	}
	if err := d.startShare(m); err != nil {
		return m, err
	}
	err = d.ch.AddFs(ctx, chv.Share{ID: m.DeviceID(), Tag: m.Tag(), Socket: d.inst.FSSocket(m.Tag())})
	if err != nil {
		d.stopShare(d.shares[m.GuestPath])
		return m, fmt.Errorf("attaching the share: %w", err)
	}
	d.log.Info("added mount", "host_path", m.HostPath, "guest_path", m.GuestPath, "read_only", m.ReadOnly)
	return m, nil
}

// removeMount detaches a share. The guest must have unmounted it first.
func (d *Daemon) removeMount(ctx context.Context, req control.RemoveMountRequest) (control.Empty, error) {
	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	s, ok := d.shares[req.GuestPath]
	if !ok {
		return control.Empty{}, fmt.Errorf("nothing is mounted at %s", req.GuestPath)
	}
	if err := d.ch.RemoveDevice(ctx, s.DeviceID()); err != nil {
		return control.Empty{}, fmt.Errorf("detaching the share: %w", err)
	}
	d.stopShare(s)
	d.log.Info("removed mount", "guest_path", req.GuestPath)
	return control.Empty{}, nil
}

func (d *Daemon) listMounts(context.Context, control.Empty) ([]mounts.Mount, error) {
	d.sharesMu.Lock()
	defer d.sharesMu.Unlock()
	out := []mounts.Mount{}
	for _, s := range d.sortedShares() {
		out = append(out, s.Mount)
	}
	return out, nil
}

// idMapArgs makes the guest user and the host user the same owner across a
// share: the guest sees the host user's files as its own, and files the guest
// creates (as any user; virtiofsd runs unprivileged) belong to the host user.
func idMapArgs(guestUID, guestGID, hostUID, hostGID int) []string {
	return []string{
		"--translate-uid", fmt.Sprintf("map:%d:%d:1", guestUID, hostUID),
		"--translate-gid", fmt.Sprintf("map:%d:%d:1", guestGID, hostGID),
	}
}
