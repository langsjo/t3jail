{
  description = "Run the T3 Code server in a network-filtered, persistent NixOS VM";

  outputs =
    { nixpkgs, ... }@inputs:
    let
      lib = nixpkgs.lib;
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = func: lib.genAttrs systems (system: func nixpkgs.legacyPackages.${system});
    in
    {
      nixosModules.guest = ./modules/guest;

      packages = forAllSystems (pkgs: import ./packages { inherit pkgs inputs; });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShellNoCC {
          packages = with pkgs; [
            go
            gopls
            cloud-hypervisor
            virtiofsd
            passt
            nftables
            openssh
          ];
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-tree);
    };

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };
}
