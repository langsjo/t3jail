# t3jail

> [!WARNING]
> This is basically completely slop coded in its current state. I have read and reviewed the
> Nix/NixOS side but not the Go CLI, though I plan to do it at some point. If you see this warning,
> I have not done that yet.


Run the [T3 Code](https://github.com/pingdotgg/t3code) server, and with it
your coding agents, in a persistent NixOS VM on your machine. The agents get
a full NixOS system with sudo, but cannot read your files or reach your home
network: the VM only sees the directories you share, and all of its traffic
passes a domain allowlist on the host. The T3 desktop app runs on the host
and connects to the server in the VM.

Everything runs as your user; nothing needs root on the host.

## Requirements

- x86_64 Linux with KVM (`/dev/kvm` accessible to you), `/dev/net/tun` and
  unprivileged user namespaces. Tested on NixOS. aarch64 Linux should work but
  is untested; macOS is not supported.
- Nix with flakes enabled.

## Quick start

    nix run github:langsjo/t3jail -- start

The first start builds the VM's disk image (a few minutes) and creates a 64G
sparse disk, an SSH key and the default configuration. Then:

    t3jail ssh               # a shell in the VM, as user `agent` with sudo
    t3jail gui               # the T3 desktop app, connected to the VM
    t3jail stop

To install it, add `github:langsjo/t3jail` as a flake input and put
`inputs.t3jail.packages.${system}.default` in your packages.

## Using it

| Command | What it does |
|---|---|
| `start [-c/--cpu N] [-m/--mem 8G] [--max-cpu N] [--max-mem 16G]` | Boot the VM. |
| `stop [--force]`, `status [--json]` | Shut it down; show its state and size. |
| `ssh [-- cmd]`, `console` | Shell over SSH; serial console (Ctrl-] detaches). |
| `logs [-f] [--log serial\|dns\|daemon]` | Logs. |
| `net allow\|remove\|list\|log\|reload` | Network allowlist, see below. |
| `mount add\|remove\|list` | Share host directories, see below. |
| `resize [-c/--cpu N] [-m/--mem SIZE]` | Change vCPUs and RAM while running. |
| `config pull\|push [--switch]` | Sync the VM's `/etc/nixos` with a host clone. |
| `pair`, `gui` | Sign T3 clients in to the server in the VM. |
| `disk resize SIZE` | Grow the disk and root filesystem, also while running. |
| `start --rescue` | Boot the rescue system, see Recovery. |

Every command has `--help`.

### Files

- `~/.config/t3jail/default/config.toml`: default CPUs, memory, disk size, T3
  port, desktop app command.
- `~/.config/t3jail/default/network.list`: the allowlist.
- `~/.local/state/t3jail/default/`: the VM disk, SSH key, persisted mounts,
  logs, the desktop app's profile.
- `$XDG_RUNTIME_DIR/t3jail/default/`: sockets, readable only by you.

### Network allowlist

The VM reaches only destinations on the allowlist; everything else fails
immediately (DNS answers REFUSED, connections are reset). Initially allowed:
`cache.nixos.org`, `channels.nixos.org`, `releases.nixos.org`, `*.github.com`,
`*.githubusercontent.com`.

    t3jail net allow registry.npmjs.org      # until the VM stops
    t3jail net allow -p '*.pypi.org'         # permanently
    t3jail net allow -p github.com:22        # only port 22
    t3jail net remove registry.npmjs.org
    t3jail net log -f                        # DNS queries and decisions

Entries are `name` (exactly that name), `*.name` (the name and everything
below it), addresses and networks (`192.0.2.7`, `198.51.100.0/24`,
`[2001:db8::/32]`). Ports default to TCP 80 and 443; `:22`, `:80,443` or
`:8000-8100` replace them. Only TCP is forwarded.

Names are enforced by address: when the VM resolves an allowed name, the
answer's addresses are opened for that entry's ports. Changes apply
immediately; removing an entry also cuts its open connections.

Private, loopback, link-local, CGNAT and other special-purpose ranges, and the
host's own addresses and local networks (followed live, so a VPN coming up is
covered), are blocked even if an allowed name resolves to them. An explicit
address or network entry overrides that block.

### Shared directories

    t3jail mount add ~/src/project                 # same path in the VM
    t3jail mount add --ro ~/datasets:/data --persist
    t3jail mount remove /data                      # --force if it is busy

Files the VM creates are owned by you on the host. `--ro` is enforced on the
host, so even root in the VM cannot write. `--persist` mounts it at every
start.

### Resources

`resize` changes vCPUs and RAM within the maximums set at start (by default
the host's CPU count and RAM). Memory can shrink back to the part fixed at
start, a quarter of the start size and at least 1G. Shrinking is best effort
and can take minutes.

### The VM's own configuration

The VM is self-contained: its own `/nix/store`, bootloader and
`/etc/nixos` flake, which imports this repository's guest module. Agents can
change it and run `sudo nixos-rebuild switch --flake /etc/nixos`; the result
persists. `/etc/nixos` is a git repository; `t3jail config pull` clones or
updates a copy on the host (default in the state directory, or `--dir`), and
`t3jail config push [--switch]` sends your commits back and optionally
rebuilds.

### T3 Code

The server listens in the VM and is forwarded to `127.0.0.1:37730` on the host
(`[t3] host_port` in config.toml). T3's own pairing tokens control access:

- `t3jail gui` starts the desktop app with a profile of its own and its
  local environment (which would run agents directly on the host) turned
  off. It uses `[gui] command` from config.toml, else `t3code-desktop` on
  `PATH`, else offers to fetch `t3code` from nixpkgs. Until the app has an
  environment saved, it prints a one-time pairing link: add it under
  Settings > Connections.
- `t3jail pair` prints a one-time link for any other client.

## Recovery

If the VM no longer boots after a configuration change:

1. One-off: `t3jail start`, then `t3jail console` and pick an earlier
   generation in the boot menu (3 seconds).
2. Otherwise `t3jail stop --force && t3jail start --rescue`. This boots a
   NixOS from RAM, built from this repository and not from the VM's
   configuration, with the VM's disk attached but not mounted. `t3jail
   console` logs in as root and shows recipes: make an earlier generation the
   default, or fix `/etc/nixos` and rebuild with `nixos-enter`. Then
   `t3jail stop && t3jail start`.

The rescue system's store is in its initrd, so it boots with fixed memory
(no live memory resize) of at least 2G, and without persisted mounts.

## Security model

The VM is the boundary. Agents have root inside it; assume they control
everything there, including the T3 server. What they can still affect on the
host:

1. **Shared directories.** Writable unless `--ro`. Files agents write there,
   such as `.envrc`, git hooks or a `flake.nix` with a `shellHook`, run on the
   host when you use those tools in that directory. Review before you do.
2. **`config pull`.** The clone's files are written by agents. Git transfers
   neither hooks nor `.git/config`, but review the files before evaluating
   them with Nix or allowing them with direnv on the host.
3. **The desktop app** talks to a server the agents control. Keep the app's
   local environment off (`t3jail gui` enforces this at every launch), and
   keep T3's "linked environments" and T3 Connect off: they let one
   environment's agents run work in another.
4. **Allowed destinations can receive data.** Agents can, for example, push to
   GitHub if it is allowed and they have credentials.
5. **Networks the host reaches through a router or VPN** are not detected as
   host networks; only the host's addresses and directly attached networks
   are. Private ranges are blocked regardless, but if a VPN routes public
   address space (an employer's network, say) and an allowed name resolves
   into it, the VM can reach it there.
6. **Escape bugs.** A bug in cloud-hypervisor or virtiofsd that lets the guest
   out gives code execution as your user. cloud-hypervisor runs with no
   capabilities in its own user and network namespace; virtiofsd runs in its
   namespace sandbox.

The T3 port on `127.0.0.1` is reachable by other local users; T3's pairing
tokens protect it. All other control sockets are readable only by you.

## Verifying it

Automated: `go test ./...`, and `nix flake check`, which runs an end-to-end
NixOS test (it needs KVM inside a VM, i.e. nested virtualization): boot,
the network filter with IPv4 and IPv6, mounts, resize, restarts,
config pull/push and the rescue system.

By hand, after `t3jail start`:

1. `t3jail ssh -- curl -sI https://github.com` succeeds;
   `t3jail ssh -- curl -sI https://example.com` fails at once, and
   `t3jail net log` shows example.com refused.
2. `t3jail net allow example.com`: the same curl succeeds.
   `t3jail net remove example.com`: it fails again.
3. `t3jail ssh -- curl -m5 http://<your router>` and
   `http://<this machine's LAN address>` fail.
4. `mkdir -p ~/tmp/x && t3jail mount add ~/tmp/x:/work`, then
   `t3jail ssh -- touch /work/f`: `~/tmp/x/f` is owned by you.
5. `t3jail resize --cpu 2 --mem 4G`; `t3jail ssh -- nproc` prints 2.
6. `t3jail gui`: pair with the printed link; the app shows no local
   environment, and `ls ~` in a new thread lists the VM's home directory.

## Development

See [AGENTS.md](AGENTS.md).
