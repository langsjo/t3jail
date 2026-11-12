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

    kernelParams = [
      "console=ttyS0"
      # virtio-mem memory must land in ZONE_MOVABLE to be unpluggable again.
      "memhp_default_state=online"
      "memory_hotplug.online_policy=auto-movable"
      "memory_hotplug.auto_movable_ratio=301"
      # ssh.nix defines its own vsock socket.
      "systemd.ssh_auto=no"
    ];
    initrd = {
      availableKernelModules = [
        "virtio_pci"
        "virtio_blk"
        "virtio_net"
        "virtiofs"
      ];
      kernelModules = [ "vmw_vsock_virtio_transport" ];
    };
    kernelModules = [ "virtio_mem" ];
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

  # Hotplugged vCPUs are not onlined by default.
  services.udev.extraRules = ''
    SUBSYSTEM=="cpu", ACTION=="add", DEVPATH=="/devices/system/cpu/cpu[0-9]*", TEST=="online", ATTR{online}="1"
  '';
}
