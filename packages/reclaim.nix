{ lib, buildGoModule }:

buildGoModule {
  pname = "t3jail-reclaim";
  version = "0.1.0";

  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../cmd/t3jail-reclaim
      ../internal/reclaim
    ];
  };
  # The helper uses only the standard library, so it needs none of the CLI's
  # dependencies and rebuilds only when its sources or go.mod change.
  vendorHash = null;
  proxyVendor = true;

  env.CGO_ENABLED = 0;
  subPackages = [ "cmd/t3jail-reclaim" ];

  meta = {
    description = "Reclaim guest file cache for free page reporting";
    mainProgram = "t3jail-reclaim";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
  };
}
