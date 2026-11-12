# Base configuration of the t3jail VM. The host-side CLI depends on it, so
# removing parts of it from inside the VM can lock the host out.
{
  lib,
  modulesPath,
  ...
}:
let
  abi = lib.importJSON ../../internal/guestabi/guestabi.json;
in
{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    ./boot.nix
    ./network.nix
    ./host-share.nix
    ./ssh.nix
    ./t3.nix
    ./nixos-config.nix
  ];

  networking.hostName = lib.mkDefault abi.hostname;
  system.stateVersion = lib.mkDefault "26.05";

  users.users.${abi.guestUser} = {
    isNormalUser = true;
    uid = abi.guestUid;
    extraGroups = [ "wheel" ];
  };
  security.sudo.wheelNeedsPassword = false;
  # Only the host user can reach the serial console.
  services.getty.autologinUser = abi.guestUser;
}
