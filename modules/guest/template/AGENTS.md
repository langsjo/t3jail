# This VM's NixOS configuration

You are inside a t3jail VM. This directory is its NixOS configuration, and you
may change it.

- `configuration.nix`: add packages, services, users, etc. here.
- `flake.nix`: imports the t3jail guest module (`t3jail.nixosModules.guest`),
  which provides boot, the static network, SSH and T3 Code over vsock, and host
  mounts. Do not remove it: the host loses access to the VM without it.
- `platform.nix`: the VM's CPU architecture. Leave it alone.

With virtio free page reporting active, the guest reclaims file cache in
small steps and pauses when memory stalls or file refaults spike. In
`configuration.nix`, set `t3jail.memoryReclaim.enable = false;` to disable it
or adjust `interval` (default 10 seconds) and `keepCacheMiB` (default 256 MiB)
under the same option. The guest reports free blocks of 64 KiB or larger.
Changes to the `page_reporting.page_reporting_order` kernel parameter take
effect on reboot.

Apply changes:

    sudo nixos-rebuild switch --flake /etc/nixos

Then commit them (`git add -A && git commit`). The host user syncs this repo
with `t3jail config pull` / `t3jail config push`.

Update the base module and nixpkgs:

    nix flake update --flake /etc/nixos

Network access is limited by an allowlist on the host. A blocked download fails
immediately (connection refused / DNS REFUSED). Request the domains with
`t3jail-request NAME... "REASON"`; it waits for the user's answer. Ask for all
the names you need in one call, and use a wildcard (`'*.example.org'`) when a
service spans many subdomains.
