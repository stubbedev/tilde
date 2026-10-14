{
  pkgs,
  module,
}: let
  python = pkgs.python3.withPackages (ps: [ps.dbus-python ps.pygobject3]);
  plugins = pkgs.lib.makeSearchPathOutput "lib" "lib/gstreamer-1.0" [pkgs.gst_all_1.gstreamer pkgs.gst_all_1.gst-plugins-base pkgs.pipewire];
in
  import ./vm.nix {
    inherit pkgs module;
    name = "tilde-screencast";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    overlays = [
      (_: prev: {
        xdg-desktop-portal-wlr = prev.xdg-desktop-portal-wlr.overrideAttrs (old: {
          patches = (old.patches or []) ++ [./xdpw-in-flight-frame.patch];
        });
      })
    ];
    machineConfig = {
      services.pipewire.enable = true;
      xdg.portal.wlr.settings.screencast = {
        chooser_type = "none";
        output_name = "Virtual-1";
      };
      environment.systemPackages = [python pkgs.gst_all_1.gstreamer];
    };
    extraTest = run: let
      screencast = cursor: run "GST_PLUGIN_SYSTEM_PATH_1_0=${plugins} ${python}/bin/python3 ${./screencast.py} ${cursor} 10 /tmp/${cursor}";
    in ''
      machine.succeed("setsid ${run "WAYLAND_DISPLAY={display} ${pkgs.weston}/bin/weston-presentation-shm"} > /tmp/animation.log 2>&1 &".format(display=display))
      machine.wait_until_succeeds("${run "tildectl windows"} | jq -e 'length == 1'", timeout=30)
      for cursor in ["hidden", "embedded"]:
          try:
              print(machine.succeed("${screencast "{cursor}"}".format(cursor=cursor)))
          except Exception:
              print(machine.execute("cat /tmp/animation.log; journalctl -b --no-pager _UID=1000 | grep -e xdg-desktop-portal -e pipewire | tail -60")[1])
              raise
    '';
  }
