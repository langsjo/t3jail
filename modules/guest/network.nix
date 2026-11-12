# Static addressing matching the host-side jail. Filtering is enforced on the
# host, so nothing here is a security boundary.
{ config, lib, ... }:
let
  inherit (config.t3jail) abi;
in
{
  networking = {
    useDHCP = false;
    useNetworkd = true;
    firewall.enable = false;
    nameservers = [
      abi.gateway4
      abi.gateway6
    ];
  };
  services.resolved.enable = false;

  systemd.network.networks."10-uplink" = {
    matchConfig.Driver = "virtio_net";
    address = [
      "${abi.guest4}/${toString abi.prefixLen4}"
      "${abi.guest6}/${toString abi.prefixLen6}"
    ];
    routes = [
      { Gateway = abi.gateway4; }
      { Gateway = abi.gateway6; }
    ];
    networkConfig = {
      IPv6AcceptRA = false;
      LinkLocalAddressing = "ipv6";
    };
    linkConfig.RequiredForOnline = "routable";
  };
}
