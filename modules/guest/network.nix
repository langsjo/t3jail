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

  # Asks the user, through the host daemon, to allow blocked names. The
  # protocol is in internal/daemon/requests.go; each name is its own request,
  # sent in parallel so the user can answer them all at once.
  request = pkgs.writeShellApplication {
    name = "t3jail-request";
    runtimeInputs = [ pkgs.socat ];
    text = ''
      usage() {
        echo "usage: t3jail-request NAME[:PORTS]... [--] [REASON...]" >&2
        echo "Asks the user to allow each NAME (e.g. registry.npmjs.org or '*.pypi.org')" >&2
        echo "and waits for the answers: exit 0 if all are allowed, 1 if not." >&2
        echo "The names are the leading arguments that look like domain names;" >&2
        echo "the rest, or everything after --, is the reason." >&2
        exit 2
      }
      entries=()
      while [ $# -gt 0 ]; do
        if [[ $1 == -- ]]; then
          shift
          break
        fi
        # A name has a dot and no spaces; anything else starts the reason.
        [[ $1 =~ ^[^[:space:]-][^[:space:]]*\.[^[:space:]]+$ ]] || break
        entries+=("$1")
        shift
      done
      [ ''${#entries[@]} -gt 0 ] || usage
      reason="$*"
      reason=''${reason//[$'\t\n']/ }
      reason=''${reason:0:200} # the daemon keeps 200 characters and refuses oversized requests

      ask() {
        local entry=$1 answer="" line
        while IFS= read -r line; do
          case $line in
            pending) echo "Asked the user to allow $entry; waiting for the answer. Interrupting is fine: the request stays, and asking again waits for it." ;;
            *) answer=$line ;;
          esac
        done < <(printf '%s\t%s\n' "$entry" "$reason" | socat -t 86400 - VSOCK-CONNECT:2:${toString abi.vsockRequestPort})
        case $answer in
          allowed*) echo "$entry: $answer" ;;
          "") echo "$entry: no answer from the host (the VM may be stopping, or the host's t3jail predates requests)" >&2; return 1 ;;
          *) echo "$entry: $answer" >&2; return 1 ;;
        esac
      }
      pids=()
      for entry in "''${entries[@]}"; do
        ask "$entry" &
        pids+=($!)
      done
      status=0
      for pid in "''${pids[@]}"; do
        wait "$pid" || status=1
      done
      exit $status
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
