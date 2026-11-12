# The rescue system as files cloud-hypervisor boots directly: kernel, initrd
# (holding the whole store) and the kernel command line.
{
  pkgs,
  inputs,
}:
let
  rescue = inputs.nixpkgs.lib.nixosSystem {
    modules = [
      ../modules/rescue
      { nixpkgs.hostPlatform = pkgs.stdenv.hostPlatform.system; }
    ];
  };
  inherit (rescue.config.system) build;
in
pkgs.runCommand "t3jail-rescue"
  {
    passthru.nixos = rescue;
  }
  ''
    mkdir $out
    ln -s ${build.kernel}/${rescue.config.system.boot.loader.kernelFile} $out/kernel
    ln -s ${build.netbootRamdisk}/initrd $out/initrd
    echo -n ${pkgs.lib.escapeShellArg "init=${build.toplevel}/init ${toString rescue.config.boot.kernelParams}"} > $out/cmdline
  ''
