# The t3jail VM. The host-side CLI depends on this configuration, so removing
# parts of it from inside the VM can lock the host out.
{
  imports = [
    ./base.nix
    ./disk.nix
    ./memory.nix
    ./t3.nix
    ./nixos-config.nix
  ];
}
