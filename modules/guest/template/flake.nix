# NixOS configuration of this t3jail VM. It is managed from inside the VM:
# edit configuration.nix, run `sudo nixos-rebuild switch --flake /etc/nixos`,
# and commit. See AGENTS.md.
{
  description = "t3jail VM configuration";

  inputs = {
    t3jail.url = "github:langsjo/t3jail";
    nixpkgs.follows = "t3jail/nixpkgs";
  };

  outputs =
    { nixpkgs, t3jail, ... }:
    {
      nixosConfigurations.t3jail = nixpkgs.lib.nixosSystem {
        modules = [
          t3jail.nixosModules.guest
          ./platform.nix
          ./configuration.nix
        ];
      };
    };
}
