{
  pkgs,
  inputs,
}:
let
  guest-image = import ./guest-image.nix { inherit pkgs inputs; };
  t3jail = pkgs.callPackage ./t3jail.nix { inherit guest-image; };
in
{
  inherit guest-image t3jail;
  default = t3jail;
}
