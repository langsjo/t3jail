# Working on t3jail

t3jail runs the T3 Code server in a persistent NixOS VM (cloud-hypervisor) and
filters all of the VM's network traffic on the host. Everything runs as the
invoking user; nothing needs root on the host. Treat anything coming from the
VM as hostile: the agents in it have root there.

## Layout

- `cmd/t3jail`: entry point.
- `internal/cli`: cobra commands, one file per group (`net.go`, `mount.go`,
  `pair.go`, ...). Hidden internal commands (`__daemon`, `__netns-helper`,
  `__vsock-proxy`) live here too.
- `internal/daemon` + `internal/control`: the per-VM supervisor and the
  HTTP-over-Unix-socket API that every CLI command talks to.
- `internal/jailnet`: the netns helper started by pasta: tap device, nftables
  ruleset (`ruleset.nft.tmpl`), its API on `netns.sock`.
- `internal/dnsfilter`, `internal/netrules`, `internal/hostnets`: the DNS
  allowlist filter, allowlist entry parsing/matching, host network watcher.
- `internal/guestabi/guestabi.json`: values the host and the guest module must
  agree on (vsock ports, uids/gids, tags). Nix reads the same file, so change
  it there, never in only one side.
- `modules/guest`: `nixosModules.guest`, the guest's NixOS module.
  `template/` is the VM's initial `/etc/nixos` (with its own AGENTS.md for
  agents inside the VM).
- `modules/rescue`: a RAM-only NixOS for `start --rescue`, sharing
  `modules/guest/base.nix` (network, SSH, host share) with the guest.
- `packages/`: the CLI (`t3jail.nix`), the guest disk image and the rescue
  system. The CLI wrapper records the image and rescue derivations, which are
  built lazily when first needed.
- `docs/`: local notes, git-ignored.

## Commands

    go test ./...                        # unit tests; some skip without nft/userns
    go vet ./...
    nix build .#t3jail                   # CLI; rebuild image with .#guest-image
    nix fmt                              # nixfmt-tree for .nix files
    gofmt -l .

After changing Go dependencies, update `vendorHash` in `packages/t3jail.nix`
(set it to `lib.fakeHash`, build, copy the hash from the error).

To try changes against a real VM, run the packaged CLI
(`nix build .#t3jail --out-link <dir>`), whose wrapper sets the firmware,
image and rescue paths, with separate `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and
`XDG_RUNTIME_DIR` so experiments do not touch a real instance. Changes to the
guest module or image change the image derivation, but an existing instance
keeps its disk: delete the state directory to test a fresh image. Needs
`/dev/kvm`, `/dev/net/tun` and unprivileged user namespaces.

## Conventions

- Optimize for readability and easy review. Comments explain non-obvious whys
  only.
- Write tests for logic that can actually go wrong; no tautological tests.
  Prefer fakes at interfaces (e.g. `dnsfilter.Grants`) over mocking processes.
- Output that originates in the VM (command output, file names, DNS names)
  must go through `internal/termsafe` before reaching the terminal.
- Never store data in `~/.ssh`; everything lives under the XDG paths in
  `internal/paths`.
- Nix: options under the `t3jail.*` namespace, `lib.mkOption` with
  `description`, `type`, `default`; split modules only where it helps.
- Commits: one logical change each.
