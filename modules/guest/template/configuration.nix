# Your changes go here. The base (boot, network, SSH and T3 over vsock) comes
# from the t3jail guest module imported in flake.nix.
{ pkgs, ... }:
{
  environment.systemPackages = with pkgs; [
    curl
    vim
  ];
}
