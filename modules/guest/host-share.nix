# The read-only "meta" share from the host carries the SSH key and the list of
# persisted mounts. virtiofsd enforces read-only on the host side.
{
  lib,
  pkgs,
  ...
}:
let
  abi = lib.importJSON ../../internal/guestabi/guestabi.json;

  guestHelper = pkgs.writeShellApplication {
    name = "t3jail-guest";
    runtimeInputs = with pkgs; [
      coreutils
      util-linux
      jq
      psmisc
    ];
    text = ''
      META_DIR=${lib.escapeShellArg abi.metaMountPoint}
      KEYS_SRC="$META_DIR"/${lib.escapeShellArg abi.authorizedKeysFile}
      MOUNTS_MANIFEST="$META_DIR"/${lib.escapeShellArg abi.mountsManifestFile}
    ''
    + builtins.readFile ./t3jail-guest.sh;
  };
in
{
  fileSystems.${abi.metaMountPoint} = {
    device = abi.metaTag;
    fsType = "virtiofs";
    options = [
      "ro"
      "nofail"
    ];
  };

  systemd.services.t3jail-host-setup = {
    description = "Apply the SSH key and persisted mounts from the t3jail host";
    wantedBy = [ "multi-user.target" ];
    requires = [ "run-t3jail-host.mount" ];
    after = [ "run-t3jail-host.mount" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${lib.getExe guestHelper} host-setup";
    };
  };

  environment.systemPackages = [ guestHelper ];
}
