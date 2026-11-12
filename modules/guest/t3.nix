{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.t3j.t3;
  abi = lib.importJSON ../../internal/guestabi/guestabi.json;
  home = config.users.users.${abi.guestUser}.home;
in
{
  options.t3j.t3 = {
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
    systemd.services.t3 = {
      description = "T3 Code server";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [
        "network-online.target"
        "t3jail-host-setup.service"
      ];
      environment = {
        T3CODE_NO_BROWSER = "1";
        # Agent CLIs installed later through any profile must be found.
        PATH = lib.mkForce (
          lib.concatStringsSep ":" [
            "/run/wrappers/bin"
            "${home}/.nix-profile/bin"
            "/etc/profiles/per-user/${abi.guestUser}/bin"
            "/nix/var/nix/profiles/default/bin"
            "/run/current-system/sw/bin"
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
