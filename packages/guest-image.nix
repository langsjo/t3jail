# The VM's initial disk: a self-contained NixOS install whose /etc/nixos flake
# rebuilds exactly this system. `t3jail init` copies it once; after that the VM
# manages itself.
{
  pkgs,
  inputs,
}:
let
  inherit (inputs) self nixpkgs;
  lib = pkgs.lib;
  system = pkgs.stdenv.hostPlatform.system;

  # Same modules, in the same order, as template/flake.nix.
  guest = nixpkgs.lib.nixosSystem {
    modules = [
      self.nixosModules.guest
      { nixpkgs.hostPlatform = system; }
      ../modules/guest/template/configuration.nix
    ];
  };

  repo = {
    type = "github";
    owner = "langsjo";
    repo = "t3jail";
  };
  t3jailLocked =
    if self ? rev then
      repo // { inherit (self) rev narHash lastModified; }
    else
      lib.warn "t3jail: guest image built from a dirty tree; its /etc/nixos pins t3jail to a local path" {
        type = "path";
        path = self.outPath;
        inherit (self) narHash lastModified;
      };

  # Pins the VM's flake to the sources this image was built from. Both are in
  # the image's store, so the first rebuild needs no network.
  guestLock = {
    version = 7;
    root = "root";
    nodes = {
      root.inputs = {
        t3jail = "t3jail";
        nixpkgs = [
          "t3jail"
          "nixpkgs"
        ];
      };
      t3jail = {
        inputs.nixpkgs = "nixpkgs";
        locked = t3jailLocked;
        original = repo;
      };
      nixpkgs = {
        inherit ((lib.importJSON ../flake.lock).nodes.nixpkgs) locked original;
      };
    };
  };

  etcNixos = pkgs.runCommand "t3jail-etc-nixos" { } ''
    cp -r ${../modules/guest/template} $out
    chmod -R u+w $out
    echo '{ nixpkgs.hostPlatform = "${system}"; }' > $out/platform.nix
    cp ${pkgs.writeText "flake.lock" (builtins.toJSON guestLock)} $out/flake.lock
  '';

  image = import "${nixpkgs}/nixos/lib/make-disk-image.nix" {
    inherit (guest) config pkgs;
    inherit lib;
    name = "t3jail-guest-image";
    format = "raw";
    partitionTableType = "efi";
    installBootLoader = true;
    touchEFIVars = false;
    copyChannel = false;
    diskSize = "auto";
    additionalSpace = "1G";
    contents = [
      {
        source = etcNixos;
        target = "/etc/nixos";
        mode = "u+w";
      }
    ];
    additionalPaths = [
      nixpkgs.outPath
      self.outPath
    ];
  };
in
image // { inherit guest etcNixos; }
