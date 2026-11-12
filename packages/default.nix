{
  pkgs,
  inputs,
}:
let
  guest-image = import ./guest-image.nix { inherit pkgs inputs; };
  rescue = import ./rescue.nix { inherit pkgs inputs; };
  t3jail = pkgs.callPackage ./t3jail.nix { inherit guest-image rescue; };
in
{
  inherit guest-image rescue t3jail;
  default = t3jail;
}
