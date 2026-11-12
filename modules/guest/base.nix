# What both the installed guest and the rescue system need for the host to
# reach them: the ABI, the agent user, network, host share and SSH.
{
  config,
  lib,
  modulesPath,
  ...
}:
let
  inherit (config.t3jail) abi;
in
{
  options.t3jail.abi = lib.mkOption {
    description = "Values the host-side t3jail CLI relies on (addresses, ports, user, share tags).";
    type = lib.types.attrs;
    default = lib.importJSON ../../internal/guestabi/guestabi.json;
    readOnly = true;
  };

  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    ./hardware.nix
    ./network.nix
    ./host-share.nix
    ./ssh.nix
  ];

  config = {
    networking.hostName = lib.mkDefault "t3jail";
    system.stateVersion = lib.mkDefault "26.05";

    users.users.${abi.guestUser} = {
      isNormalUser = true;
      uid = abi.guestUid;
      group = "users";
      extraGroups = [ "wheel" ];
    };
    security.sudo.wheelNeedsPassword = false;
    assertions = [
      {
        assertion = config.users.groups.users.gid == abi.guestGid;
        message = "t3jail maps gid ${toString abi.guestGid} in shared directories to the host user's group; the users group must have that gid.";
      }
    ];
    # Only the host user can reach the serial console.
    services.getty.autologinUser = abi.guestUser;
  };
}
