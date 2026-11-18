{
  config,
  lib,
  ...
}:
let
  cfg = config.t3jail.memoryReclaim;
  params = "/sys/module/damon_reclaim/parameters";
  skipAnon = config.swapDevices == [ ] && !config.zramSwap.enable;
in
{
  options.t3jail.memoryReclaim = {
    enable = lib.mkOption {
      description = "Whether to reclaim cold guest memory with DAMON_RECLAIM.";
      type = lib.types.bool;
      default = true;
    };

    minAge = lib.mkOption {
      description = "Seconds a page must go untouched before DAMON_RECLAIM can reclaim it.";
      type = lib.types.ints.positive;
      default = 120;
    };
  };

  config = lib.mkIf cfg.enable {
    # The guest disk is a raw file, so reads of reclaimed guest page cache
    # can usually be served from the host's page cache.
    systemd.services.damon-reclaim = {
      description = "Reclaim cold guest memory with DAMON";
      wantedBy = [ "multi-user.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      script = ''
        cd ${params}
        echo ${toString (cfg.minAge * 1000000)} > min_age
        # Keep reclaim active even when page cache fills most of RAM.
        echo 1000 > wmarks_high
        echo 1000 > wmarks_mid
        echo 0 > wmarks_low
        echo ${if skipAnon then "Y" else "N"} > skip_anon
        if [ "$(cat enabled)" = Y ]; then
          echo Y > commit_inputs
        else
          echo Y > enabled
        fi
      '';
      preStop = ''
        echo N > ${params}/enabled
      '';
    };
  };
}
