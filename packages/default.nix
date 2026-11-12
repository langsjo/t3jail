{
  pkgs,
  inputs,
}:
{
  guest-image = import ./guest-image.nix { inherit pkgs inputs; };
}
