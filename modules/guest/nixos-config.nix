# /etc/nixos is a git-tracked flake owned by the agent user, so agents can
# change and rebuild this VM, and the host can sync it with `t3jail config`.
{
  config,
  lib,
  pkgs,
  ...
}:
let
  inherit (config.t3jail) abi;
in
{
  nix.settings = {
    experimental-features = [
      "nix-command"
      "flakes"
    ];
    trusted-users = [
      "root"
      abi.guestUser
    ];
  };

  programs.git = {
    enable = true;
    # Root must still read the agent-owned repo to rebuild.
    config.safe.directory = "/etc/nixos";
  };

  systemd.services.t3jail-nixos-config-init = {
    description = "Turn /etc/nixos into a git repository on first boot";
    wantedBy = [ "multi-user.target" ];
    unitConfig.ConditionPathExists = "!/etc/nixos/.git";
    serviceConfig.Type = "oneshot";
    script = ''
      chown -R ${abi.guestUser}:users /etc/nixos
      cd /etc/nixos
      git() {
        ${lib.getExe' pkgs.util-linux "runuser"} -u ${abi.guestUser} -- ${lib.getExe pkgs.git} "$@"
      }
      git init -q -b main
      # Lets `t3jail config push` update the checked-out branch.
      git config receive.denyCurrentBranch updateInstead
      # AGENTS.md tells agents to commit here; without an identity they can't.
      git config user.name t3jail
      git config user.email t3jail@localhost
      git add -A
      git commit -q -m "Initial configuration"
    '';
  };
}
