{
  lib,
  buildGoModule,
  makeBinaryWrapper,
  cloud-hypervisor,
  virtiofsd,
  passt,
  nftables,
  openssh,
  gitMinimal,
  nix,
  util-linux,
  OVMF-cloud-hypervisor,
  guest-image,
  rescue,
}:
buildGoModule {
  pname = "t3jail";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
    ];
  };
  vendorHash = "sha256-w7RVEw8daTEQEoVMsNstT1kLLNJF7aUQZ8Ji5h7mmqQ=";

  env.CGO_ENABLED = 0;
  subPackages = [ "cmd/t3jail" ];

  nativeBuildInputs = [ makeBinaryWrapper ];

  # The image and the rescue system are referenced by their derivations, not
  # their outputs, so they are only built when `t3jail init` creates a disk or
  # `start --rescue` needs it.
  postInstall = ''
    wrapProgram $out/bin/t3jail \
      --prefix PATH : ${
        lib.makeBinPath [
          cloud-hypervisor
          virtiofsd
          passt
          nftables
          openssh
          gitMinimal
          nix
          util-linux
        ]
      } \
      --set-default T3JAIL_FIRMWARE ${OVMF-cloud-hypervisor.fd}/FV/CLOUDHV.fd \
      --set-default T3JAIL_IMAGE_DRV ${builtins.unsafeDiscardOutputDependency guest-image.drvPath} \
      --set-default T3JAIL_RESCUE_DRV ${builtins.unsafeDiscardOutputDependency rescue.drvPath}
  '';

  meta = {
    description = "Run the T3 Code server in a network-filtered, persistent NixOS VM";
    mainProgram = "t3jail";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
  };
}
