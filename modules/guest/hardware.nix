# Kernel and device setup for cloud-hypervisor.
{
  boot = {
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

  # The clock follows the host through kvm-clock, so NTP is not needed (and
  # the default allowlist allows no UDP).
  services.timesyncd.enable = false;

  # Hotplugged vCPUs are not onlined by default.
  services.udev.extraRules = ''
    SUBSYSTEM=="cpu", ACTION=="add", DEVPATH=="/devices/system/cpu/cpu[0-9]*", TEST=="online", ATTR{online}="1"
  '';
}
