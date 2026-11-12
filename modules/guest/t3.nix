{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.t3jail.t3;
  inherit (config.t3jail) abi;
  home = config.users.users.${abi.guestUser}.home;
in
{
  options.t3jail.t3 = {
    package = lib.mkOption {
      description = "T3 Code package providing `bin/t3`";
      type = lib.types.package;
      # Agent CLIs are configured in the VM, not bundled.
      default = pkgs.t3code.override {
        enableCodex = false;
        enableGitHub = false;
      };
      defaultText = lib.literalExpression "pkgs.t3code.override { enableCodex = false; enableGitHub = false; }";
    };
  };

  config = {
    # `t3 auth`/`t3 pair` operate on the server's state, and `t3jail pair` runs them.
    environment.systemPackages = [ cfg.package ];

    systemd.services.t3 = {
      description = "T3 Code server";
      wantedBy = [ "multi-user.target" ];
      # Every agent session runs inside this service, including the one that
      # runs `nixos-rebuild switch`, so a switch that changes the unit (e.g. a
      # new package after a nixpkgs update) must not restart it. A new version
      # takes effect on the next `systemctl restart t3` or reboot.
      restartIfChanged = false;
      wants = [ "network-online.target" ];
      after = [
        "network-online.target"
        "t3jail-host-setup.service"
      ];
      environment = {
        # Agent CLIs installed later through any profile must be found.
        PATH = lib.mkForce (
          lib.makeBinPath [
            "/run/wrappers"
            "${home}/.nix-profile"
            "/etc/profiles/per-user/${abi.guestUser}"
            "/nix/var/nix/profiles/default"
            "/run/current-system/sw"
          ]
        );
      };
      serviceConfig = {
        User = abi.guestUser;
        WorkingDirectory = home;
        ExecStart = lib.escapeShellArgs [
          "${cfg.package}/bin/t3"
          "serve"
          "--host"
          "127.0.0.1"
          "--port"
          (toString abi.t3Port)
          "--base-dir"
          "${home}/.t3"
        ];
        Restart = "always";
        RestartSec = "2s";
      };
    };

    systemd.sockets.t3-vsock = {
      description = "T3 Code over vsock for the t3jail host";
      wantedBy = [ "sockets.target" ];
      listenStreams = [ "vsock::${toString abi.vsockT3Port}" ];
    };
    systemd.services.t3-vsock = {
      description = "Proxy vsock connections to the T3 Code server";
      requires = [ "t3.service" ];
      after = [ "t3.service" ];
      serviceConfig.ExecStart = "${config.systemd.package}/lib/systemd/systemd-socket-proxyd 127.0.0.1:${toString abi.t3Port}";
    };
  };
}
