# This VM's NixOS configuration

You are inside a t3jail VM. This directory is its NixOS configuration, and you
may change it.

- `configuration.nix`: add packages, services, users, etc. here.
- `flake.nix`: imports the t3jail guest module (`t3jail.nixosModules.guest`),
  which provides boot, the static network, SSH and T3 Code over vsock, and host
  mounts. Do not remove it: the host loses access to the VM without it.
- `platform.nix`: the VM's CPU architecture. Leave it alone.

Apply changes:

    sudo nixos-rebuild switch --flake /etc/nixos

Then commit them (`git add -A && git commit`). The host user syncs this repo
with `t3jail config pull` / `t3jail config push`.

Update the base module and nixpkgs:

    nix flake update --flake /etc/nixos

Network access is limited by an allowlist on the host. A blocked download fails
immediately (connection refused / DNS REFUSED). Request the domain with
`t3jail-request NAME "REASON"`; it waits for the user's answer.
