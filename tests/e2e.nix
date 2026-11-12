# End-to-end test: an unprivileged user on `host` runs t3jail, whose VM may
# only reach what the allowlist permits. `internet` stands in for the
# internet (on public-looking addresses routed through it) and for the home
# network (its address on the test LAN).
{
  pkgs,
  t3jail,
  guest-image,
  rescue,
}:
let
  # Addresses on `internet`, each answering for one purpose.
  ip = {
    allowed = "1.2.3.4";
    other = "1.2.3.5";
    wild = "1.2.3.6";
    cidrOnly = "1.2.3.7";
    allowed6 = "2a00:1::4";
    other6 = "2a00:1::5";
  };
  lanIP = "192.168.1.2"; # internet's address on the shared test LAN
in
pkgs.testers.runNixOSTest {
  name = "t3jail-e2e";

  nodes = {
    internet = {
      virtualisation.vlans = [ 1 ];
      networking.firewall.enable = false;
      networking.interfaces.lo = {
        ipv4.addresses =
          map
            (address: {
              inherit address;
              prefixLength = 32;
            })
            (
              with ip;
              [
                allowed
                other
                wild
                cidrOnly
              ]
            );
        ipv6.addresses =
          map
            (address: {
              inherit address;
              prefixLength = 128;
            })
            (
              with ip;
              [
                allowed6
                other6
              ]
            );
      };

      services.dnsmasq = {
        enable = true;
        resolveLocalQueries = false;
        settings = {
          no-resolv = true;
          listen-address = [ ip.allowed ];
          bind-interfaces = true;
          address = [
            "/allowed.test/${ip.allowed}"
            "/allowed.test/${ip.allowed6}"
            "/other.test/${ip.other}"
            "/other.test/${ip.other6}"
            # Also matches every name below wild.test.
            "/wild.test/${ip.wild}"
            # An allowed name pointing into the home network.
            "/lan.test/${lanIP}"
          ];
        };
      };

      services.nginx = {
        enable = true;
        virtualHosts.default = {
          default = true;
          listen = [
            {
              addr = "0.0.0.0";
              port = 80;
            }
            {
              addr = "0.0.0.0";
              port = 8080;
            }
            {
              addr = "[::]";
              port = 80;
            }
          ];
          locations."/".return = ''200 "reached $server_addr:$server_port\n"'';
        };
      };
    };

    host = {
      virtualisation = {
        vlans = [ 1 ];
        memorySize = 6144;
        cores = 4;
        diskSize = 16384;
        # cloud-hypervisor needs KVM inside this VM.
        qemu.options = [ "-cpu host" ];
      };
      networking = {
        firewall.enable = false;
        defaultGateway = {
          address = lanIP;
          interface = "eth1";
        };
        defaultGateway6 = {
          address = "2001:db8:1::2";
          interface = "eth1";
          # QEMU's user-mode NIC advertises a default route (metric 1002)
          # that leads nowhere; this one must win.
          metric = 1;
        };
        nameservers = [ ip.allowed ];
      };
      nix.settings.experimental-features = [ "nix-command" ];

      # A service on the host itself, which the VM must not reach.
      services.nginx.enable = true;

      users.users.alice = {
        isNormalUser = true;
        extraGroups = [ "kvm" ];
        linger = true;
      };
      environment.systemPackages = [
        # Without derivation references, which would pull their whole build
        # graph into this VM's store registration.
        (t3jail.override {
          guest-image = null;
          rescue = null;
        })
        pkgs.git
      ];
      system.extraDependencies = [ rescue ];
    };
  };

  testScript = ''
    import shlex

    start_all()
    internet.wait_for_unit("dnsmasq.service")
    internet.wait_for_unit("nginx.service")
    host.wait_for_unit("multi-user.target")
    host.wait_for_unit("user@1000.service")

    def as_alice(args):
        env = "XDG_RUNTIME_DIR=/run/user/1000 T3JAIL_RESCUE_DRV=${rescue}"
        return f"su - alice -c {shlex.quote(f'{env} t3jail {args}')}"

    def t3jail(args):
        return host.succeed(as_alice(args))

    def guest(cmd):
        return t3jail(f"ssh -- {shlex.quote(cmd)}")

    def reaches(url, extra=""):
        out = guest(f"curl -sS --max-time 5 {extra} {url} || echo FAILED")
        return "reached" in out

    with subtest("boot"):
        t3jail("init --image ${guest-image}/nixos.img --disk-size 12G")
        t3jail("net allow -p allowed.test '*.wild.test' lan.test")
        t3jail("start --cpu 2 --max-cpu 3 --mem 2G --max-mem 3G")
        guest("true")
        # The guest is up once SSH answers; T3 Code may still be starting.
        try:
            listening = shlex.quote("ss -Hltn 'sport = :3773' | grep -q .")
            host.wait_until_succeeds(as_alice(f"ssh -- {listening}"), timeout=300)
        except Exception:
            print(guest("systemctl status t3.service; journalctl -b -u t3.service -n 40 --no-pager || true"))
            raise
        code = host.succeed("curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:37730/")
        assert code != "000", "T3 Code port does not answer"

    with subtest("names on the allowlist, on their ports only"):
        assert reaches("http://allowed.test")
        assert not reaches("http://allowed.test:8080")
        t3jail("net allow allowed.test:8080")
        assert reaches("http://allowed.test:8080")
        t3jail("net remove allowed.test:8080")
        assert not reaches("http://allowed.test:8080")

    with subtest("wildcard entries match the apex and names at any depth"):
        assert reaches("http://wild.test")
        assert reaches("http://a.b.wild.test")

    with subtest("other names are refused, also with a different resolver"):
        guest("! getent hosts other.test")
        assert "other.test" in t3jail("net log")
        assert not reaches("http://${ip.other}")
        guest("! timeout 5 bash -c 'echo > /dev/tcp/${ip.allowed}/53'")
        guest("sudo cp /etc/resolv.conf /tmp/resolv.conf.orig")
        guest("echo 'nameserver ${ip.allowed}' | sudo tee /etc/resolv.conf")
        guest("! getent hosts other.test")
        guest("sudo cp /tmp/resolv.conf.orig /etc/resolv.conf")
        guest("getent hosts allowed.test")

    with subtest("the host and the home network are refused"):
        assert not reaches("http://${lanIP}")
        # Allowed and resolved, but pointing into the home network.
        assert guest("getent hosts lan.test").split()[0] == "${lanIP}"
        assert not reaches("http://lan.test")
        assert not reaches("http://192.168.1.1")

    with subtest("an explicit network entry overrides the block"):
        assert not reaches("http://${ip.cidrOnly}")
        t3jail("net allow ${ip.cidrOnly}/32 ${lanIP}/32")
        assert reaches("http://${ip.cidrOnly}")
        assert reaches("http://${lanIP}")
        t3jail("net remove ${ip.cidrOnly}/32 ${lanIP}/32")
        assert not reaches("http://${lanIP}")

    with subtest("IPv6"):
        # Positive control: the host itself reaches it.
        host.succeed("curl -sS -g --max-time 5 http://[${ip.allowed6}]/")
        assert reaches("http://allowed.test", "-6")
        assert not reaches("http://[${ip.other6}]")
        assert not reaches("http://[2001:db8:1::2]")

    with subtest("switching the allowlist off and on"):
        listed = t3jail("net list")
        t3jail("net disable")
        assert "allowlist off" in t3jail("status")
        guest("getent hosts other.test")
        assert reaches("http://other.test")
        assert reaches("http://${ip.other}:8080")
        assert not reaches("http://${lanIP}")
        assert not reaches("http://192.168.1.1")
        t3jail("net allow ${lanIP}/32")
        assert reaches("http://${lanIP}")
        t3jail("net remove ${lanIP}/32")

        t3jail("net enable")
        guest("! getent hosts other.test")
        assert not reaches("http://${ip.other}:8080")
        assert reaches("http://allowed.test")
        assert t3jail("net list") == listed

        t3jail("net disable -p")
        t3jail("stop")
        t3jail("start -c 2 -m 2G")
        assert reaches("http://other.test")
        t3jail("net enable -p")
        assert not reaches("http://other.test")
        assert t3jail("net list") == listed

    with subtest("mounts"):
        host.succeed("su - alice -c 'mkdir -p ~/proj ~/data && echo from-host > ~/data/f'")
        t3jail("mount add /home/alice/proj:/work")
        guest("echo from-vm > /work/new")
        assert host.succeed("stat -c %U /home/alice/proj/new").strip() == "alice"
        t3jail("mount add --ro /home/alice/data:/data")
        assert guest("cat /data/f").strip() == "from-host"
        guest("! sudo touch /data/x")
        t3jail("mount remove /data")
        guest("! test -e /data/f")
        t3jail("mount add --persist /home/alice/data:/data")

    with subtest("live resize"):
        t3jail("resize --cpu 3 --mem 3G")
        assert guest("nproc").strip() == "3"
        mem_kib = int(guest("awk '/MemTotal/ {print $2}' /proc/meminfo"))
        assert mem_kib > 2.5 * 1024 * 1024, f"MemTotal {mem_kib} KiB after growing to 3G"
        t3jail("resize -c 1")
        assert guest("nproc").strip() == "1"

    with subtest("growing the disk while running"):
        root_size = "df -B1 --output=size / | tail -1"
        before = int(guest(root_size))
        t3jail("disk resize 14G")
        after = int(guest(root_size))
        assert after > before + 1.5 * 1024**3, f"root filesystem {before} -> {after} bytes"

    with subtest("restart keeps the disk and persisted mounts"):
        guest("echo kept > ~/state")
        t3jail("stop")
        t3jail("start -c 2 -m 2G")
        assert guest("cat ~/state").strip() == "kept"
        assert guest("cat /data/f").strip() == "from-host"
        guest("! test -e /work/new")

    with subtest("config pull and push"):
        t3jail("config pull")
        repo = "/home/alice/.local/state/t3jail/default/nixos-config"
        host.succeed(f"su - alice -c {shlex.quote(f'cd {repo} && echo \"# from host\" >> configuration.nix && git -c user.name=a -c user.email=a@b commit -qam host')}")
        t3jail("config push")
        guest("grep -q 'from host' /etc/nixos/configuration.nix")
        guest("cd /etc/nixos && echo '# from vm' >> configuration.nix && git commit -qam vm")
        t3jail("config pull")
        host.succeed(f"grep -q 'from vm' {repo}/configuration.nix")

    with subtest("the VM's configuration rebuilds its system offline"):
        built = guest("nix eval --raw --offline /etc/nixos#nixosConfigurations.t3jail.config.system.build.toplevel").strip()
        assert built == guest("readlink /run/current-system").strip(), f"/etc/nixos evaluates to {built}"
        guest("cd /etc/nixos && git diff --exit-code flake.lock")
        out = guest("nix-instantiate --option flake-registry ''' --eval -E '<nixpkgs>' 2>&1")
        assert "does not exist" not in out, out
        guest("! systemctl is-active systemd-timesyncd")

    with subtest("rescue"):
        t3jail("stop")
        t3jail("start --rescue")
        assert guest("hostname").strip() == "t3jail-rescue"
        guest("sudo mount /dev/disk/by-label/nixos /mnt && sudo test -e /mnt/home/agent/state")
        assert not reaches("http://other.test")
        t3jail("stop")
        t3jail("start")
        assert guest("hostname").strip() == "t3jail"
  '';
}
