{
  config,
  lib,
  ...
}:
let
  abi = lib.importJSON ../../internal/guestabi/guestabi.json;
in
{
  services.openssh = {
    enable = true;
    openFirewall = false;
    # Written by t3jail-host-setup from the host's meta share.
    authorizedKeysFiles = [ "/run/t3jail/ssh/authorized_keys" ];
    settings = {
      PasswordAuthentication = false;
      KbdInteractiveAuthentication = false;
      PermitRootLogin = "no";
    };
  };

  # The host reaches sshd through cloud-hypervisor's vsock Unix socket, which
  # only the host user can open.
  systemd.sockets.t3jail-sshd-vsock = {
    description = "SSH over vsock for the t3jail host";
    wantedBy = [ "sockets.target" ];
    socketConfig = {
      ListenStream = "vsock::${toString abi.vsockSSHPort}";
      Accept = true;
    };
  };
  systemd.services."t3jail-sshd-vsock@" = {
    description = "SSH session over vsock";
    # sshd.service generates the host keys.
    after = [ "sshd.service" ];
    serviceConfig = {
      ExecStart = "-${config.services.openssh.package}/bin/sshd -i -f /etc/ssh/sshd_config";
      StandardInput = "socket";
      StandardError = "journal";
    };
  };
}
