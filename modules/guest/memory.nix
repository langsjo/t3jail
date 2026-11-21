{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.t3jail.memoryReclaim;
  reclaim = pkgs.callPackage ../../packages/reclaim.nix { };
in
{
  imports = [
    (lib.mkRemovedOptionModule [ "t3jail" "memoryReclaim" "minAge" ] ''
      The guest now uses memory pressure and file refaults to pace reclaim.
      Use t3jail.memoryReclaim.interval and t3jail.memoryReclaim.keepCacheMiB
      to set the sampling interval and file cache floor.
    '')
  ];

  options.t3jail.memoryReclaim = {
    enable = lib.mkOption {
      description = "Whether to reclaim guest file cache when free page reporting is active.";
      type = lib.types.bool;
      default = true;
    };

    interval = lib.mkOption {
      description = ''
        Seconds between samples of memory pressure, file refaults and file
        cache size. Each sample may reclaim 2% of the file cache, so a longer
        interval drains more slowly. The backoff limits scale with it.
      '';
      type = lib.types.ints.positive;
      default = 10;
    };

    keepCacheMiB = lib.mkOption {
      description = "Floor in MiB of total file cache when sizing a reclaim request.";
      type = lib.types.ints.unsigned;
      default = 256;
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.memory-reclaim = {
      description = "Reclaim guest file cache between bursts of memory pressure";
      wantedBy = [ "multi-user.target" ];
      unitConfig.ConditionPathExists = "/sys/fs/cgroup/memory.reclaim";
      serviceConfig = {
        ExecStart = "${reclaim}/bin/t3jail-reclaim --interval=${toString cfg.interval}s --keep-cache-mib=${toString cfg.keepCacheMiB}";
        Nice = 19;
        IOSchedulingClass = "best-effort";
        IOSchedulingPriority = 7;
        Restart = "on-failure";
        RestartSec = "30s";

        # It only reads /proc and /sys and writes memory.reclaim, which needs
        # no capabilities.
        CapabilityBoundingSet = "";
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        PrivateNetwork = true;
        RestrictAddressFamilies = "none";
        RestrictNamespaces = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
      };
    };
  };
}
