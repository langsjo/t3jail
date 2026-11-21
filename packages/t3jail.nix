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
  libnotify,
  OVMF-cloud-hypervisor,
  guest-image,
  rescue,
}:
let
  setDrv =
    name: package:
    lib.optionalString (
      package != null
    ) "--set-default ${name} ${builtins.unsafeDiscardOutputDependency package.drvPath}";
in
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
  vendorHash = "sha256-akMwOj/3t6W7WUWhgFivWQXrNINpd1TfNjLeGrYs3Nk=";

  env.CGO_ENABLED = 0;
  subPackages = [ "cmd/t3jail" ];

  nativeBuildInputs = [ makeBinaryWrapper ];

  # The image and the rescue system are referenced by their derivations, not
  # their outputs, so they are only built when `t3jail init` creates a disk or
  # `start --rescue` needs it. Without them (null), the closure holds no
  # derivations; `init --image` and T3JAIL_RESCUE_DRV supply them instead, as
  # the e2e test does.
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
          libnotify # notify-send, for request reminders
        ]
      } \
      --set-default T3JAIL_FIRMWARE ${OVMF-cloud-hypervisor.fd}/FV/CLOUDHV.fd \
      ${setDrv "T3JAIL_IMAGE_DRV" guest-image} \
      ${setDrv "T3JAIL_RESCUE_DRV" rescue}

    # git runs this for t3jail::<repo> remote URLs.
    makeWrapper $out/bin/t3jail $out/bin/git-remote-t3jail --add-flags __git-remote
  '';

  meta = {
    description = "Run the T3 Code server in a network-filtered, persistent NixOS VM";
    mainProgram = "t3jail";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
  };
}
