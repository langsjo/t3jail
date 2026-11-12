# Base configuration of the t3jail VM. The host-side CLI depends on it, so
# removing parts of it from inside the VM can lock the host out.
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
    ./boot.nix
    ./network.nix
    ./host-share.nix
    ./ssh.nix
    ./t3.nix
    ./nixos-config.nix
  ];

  config = {
    networking.hostName = lib.mkDefault "t3jail";
    system.stateVersion = lib.mkDefault "26.05";

    users.users.${abi.guestUser} = {
      isNormalUser = true;
      uid = abi.guestUid;
      extraGroups = [ "wheel" ];
    };
    security.sudo.wheelNeedsPassword = false;
    # Only the host user can reach the serial console.
    services.getty.autologinUser = abi.guestUser;
  };
}
