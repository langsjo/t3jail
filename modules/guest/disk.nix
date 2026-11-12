# The installed system on the VM's disk.
{
  boot = {
    # The guest owns its bootloader so `nixos-rebuild` inside the VM persists.
    # There is no NVRAM; systemd-boot is found through the fallback path.
    loader = {
      systemd-boot.enable = true;
      efi.canTouchEfiVariables = false;
      timeout = 3;
    };
    growPartition = true;
  };

  fileSystems = {
    "/" = {
      device = "/dev/disk/by-label/nixos";
      fsType = "ext4";
      autoResize = true;
    };
    "/boot" = {
      device = "/dev/disk/by-label/ESP";
      fsType = "vfat";
      options = [ "umask=0077" ];
    };
  };
}
