{
  pkgs,
  module,
}: let
  python = pkgs.python3.withPackages (ps: [ps.dbus-python ps.pygobject3]);
  frames = pkgs.runCommandCC "pw-frames" {
    nativeBuildInputs = [pkgs.pkg-config];
    buildInputs = [pkgs.pipewire];
  } "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/pw-frames ${./pw-frames.c} $(pkg-config --cflags --libs libpipewire-0.3)";
in
  import ./vm.nix {
    inherit pkgs module;
    name = "tilde-screencast";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {
      config,
      pkgs,
      ...
    }: {
      services.pipewire.enable = true;
      xdg.portal.wlr.settings.screencast = {
        chooser_type = "dmenu";
        chooser_cmd = "head -n1";
      };
      environment.systemPackages = [python frames];
      systemd.user.services.xdg-desktop-portal-wlr.serviceConfig.ExecStart = pkgs.lib.mkForce [
        ""
        "${pkgs.xdg-desktop-portal-wlr}/libexec/xdg-desktop-portal-wlr --loglevel=DEBUG --config=${(pkgs.formats.ini {}).generate "xdg-desktop-portal-wlr.ini" config.xdg.portal.wlr.settings}"
      ];
    };
    extraTest = run: let
      screencast = run "${python}/bin/python3 ${./screencast.py} {source} {cursor} 10 /tmp/{source}-{cursor}";
    in ''
      machine.succeed("setsid ${run "WAYLAND_DISPLAY={display} ${pkgs.weston}/bin/weston-presentation-shm"} > /tmp/animation.log 2>&1 &".format(display=display))
      machine.wait_until_succeeds("${run "tildectl windows"} | jq -e 'length == 1'", timeout=30)
      for source, cursor in [("monitor", "hidden"), ("monitor", "embedded"), ("window", "hidden")]:
          try:
              print(machine.succeed("${screencast}".format(source=source, cursor=cursor)))
          except Exception:
              print(machine.execute("cat /tmp/animation.log; journalctl -b --no-pager _UID=1000 | grep -e xdg-desktop-portal-wlr -e pipewire | tail -150")[1])
              raise
    '';
  }
