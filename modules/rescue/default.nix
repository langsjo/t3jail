# A NixOS system that runs from RAM, for repairing a VM that no longer boots.
# It is built from this repository, not from the VM's /etc/nixos, so it works
# however the VM's configuration was broken. The VM's disk is attached but
# not mounted.
{
  lib,
  modulesPath,
  ...
}:
{
  imports = [
    (modulesPath + "/installer/netboot/netboot.nix")
    (modulesPath + "/profiles/minimal.nix")
    ../guest/base.nix
  ];

  networking.hostName = "t3jail-rescue";
  # Faster to build; the store is only read once per boot.
  netboot.squashfsCompression = "zstd -Xcompression-level 6";
  # Rebuilding the VM uses the nixpkgs locked in its /etc/nixos, so the
  # rescue system's own nixpkgs source (~200 MiB of RAM) is not needed.
  nixpkgs.flake.source = lib.mkForce null;

  services.getty.autologinUser = lib.mkForce "root";
  # The motd's recipes mount the VM's disk here.
  systemd.tmpfiles.rules = [ "d /mnt 0755 root root -" ];

  users.motd = ''

    t3jail rescue system. The VM's disk is attached but not mounted.

    Mount it:
      mount /dev/disk/by-label/nixos /mnt
      mount /dev/disk/by-label/ESP /mnt/boot

    Boot an earlier generation by default:
      ls -l /mnt/nix/var/nix/profiles/          # system-<N>-link
      nixos-enter --root /mnt -c 'nix-env -p /nix/var/nix/profiles/system --switch-generation <N> &&
        /nix/var/nix/profiles/system/bin/switch-to-configuration boot'

    Fix the configuration and rebuild it:
      nixos-enter --root /mnt                   # a shell in the VM's system
      cd /etc/nixos && git log && git diff
      nixos-rebuild boot --flake /etc/nixos#t3jail   # name from flake.nix

    Then: umount -R /mnt; poweroff; and on the host `t3jail start`.
    One-off: pick an earlier generation in the boot menu with `t3jail console`.
  '';
}
