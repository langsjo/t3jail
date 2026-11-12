// Package guestabi holds the constants shared by the host CLI and the guest
// NixOS module (nix/guest/module.nix reads the same JSON file), so addresses,
// ports and paths cannot drift apart.
package guestabi

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/netip"
)

//go:embed guestabi.json
var raw []byte

// ABI is the host/guest contract.
type ABI struct {
	GuestUser          string     `json:"guestUser"`
	GuestUID           int        `json:"guestUid"`
	GuestGID           int        `json:"guestGid"`
	Gateway4           netip.Addr `json:"gateway4"`
	Guest4             netip.Addr `json:"guest4"`
	PrefixLen4         int        `json:"prefixLen4"`
	Gateway6           netip.Addr `json:"gateway6"`
	Guest6             netip.Addr `json:"guest6"`
	PrefixLen6         int        `json:"prefixLen6"`
	VsockCID           uint32     `json:"vsockCID"`
	VsockSSHPort       uint32     `json:"vsockSSHPort"`
	VsockT3Port        uint32     `json:"vsockT3Port"`
	T3Port             uint16     `json:"t3Port"`
	MetaTag            string     `json:"metaTag"`
	MetaMountPoint     string     `json:"metaMountPoint"`
	AuthorizedKeysFile string     `json:"authorizedKeysFile"`
	MountsManifestFile string     `json:"mountsManifestFile"`
}

// Get returns the parsed ABI. The file is embedded, so a parse error is a
// build-time bug and panics.
func Get() ABI {
	var abi ABI
	if err := json.Unmarshal(raw, &abi); err != nil {
		panic(fmt.Sprintf("guestabi.json: %v", err))
	}
	return abi
}

// GatewayPrefix4 is the IPv4 network shared by the gateway and the guest.
func (a ABI) GatewayPrefix4() netip.Prefix {
	return netip.PrefixFrom(a.Gateway4, a.PrefixLen4)
}

// GatewayPrefix6 is the IPv6 network shared by the gateway and the guest.
func (a ABI) GatewayPrefix6() netip.Prefix {
	return netip.PrefixFrom(a.Gateway6, a.PrefixLen6)
}
