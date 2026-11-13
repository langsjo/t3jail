# Static addressing matching the host-side jail. Filtering is enforced on the
# host, so nothing here is a security boundary.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (config.t3jail) abi;

  # Asks the user, through the host daemon, to allow a blocked name. The
  # protocol is in internal/daemon/requests.go.
  request = pkgs.writeShellApplication {
    name = "t3jail-request";
    runtimeInputs = [ pkgs.socat ];
    text = ''
      if [ $# -lt 1 ] || [[ $1 == -* ]]; then
        echo "usage: t3jail-request NAME[:PORTS] [REASON...]" >&2
        echo "Asks the user to allow NAME (e.g. registry.npmjs.org or '*.pypi.org')" >&2
        echo "and waits for the answer: exit 0 if allowed, 1 if not." >&2
        exit 2
      fi
      entry=$1
      shift
      reason="$*"
      reason=''${reason//[$'\t\n']/ }
      reason=''${reason:0:200} # the daemon keeps 200 characters and refuses oversized requests
      answer=
      while IFS= read -r line; do
        case $line in
          pending) echo "Asked the user to allow $entry; waiting for the answer. Interrupting is fine: the request stays, and asking again waits for it." ;;
          *) answer=$line ;;
        esac
      done < <(printf '%s\t%s\n' "$entry" "$reason" | socat -t 86400 - VSOCK-CONNECT:2:${toString abi.vsockRequestPort})
      case $answer in
        allowed*) echo "$entry: $answer" ;;
        "") echo "t3jail-request: no answer from the host (the VM may be stopping, or the host's t3jail predates requests)" >&2; exit 1 ;;
        *) echo "$entry: $answer" >&2; exit 1 ;;
      esac
    '';
  };
in
{
  environment.systemPackages = [ request ];

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
