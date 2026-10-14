{
  pkgs,
  module,
}: let
  launch = import ./launch.nix pkgs;
  region = pkgs.writeShellScript "region" ''
    set -euo pipefail
    ${pkgs.grim}/bin/grim -t ppm -g "$1,$2 $3x$4" /tmp/region.ppm
    tail -n +4 /tmp/region.ppm > /tmp/region.rgb
    colors=$(${pkgs.coreutils}/bin/od -An -v -tx1 -w3 /tmp/region.rgb | sort -u | wc -l)
    first=$(${pkgs.coreutils}/bin/od -An -v -tx1 -w3 -N3 /tmp/region.rgb | tr -d ' ')
    echo "$(sha256sum < /tmp/region.rgb | cut -c1-16) $colors $first"
  '';
  waybarConfig = pkgs.writeText "waybar.json" (builtins.toJSON {
    layer = "top";
    position = "top";
    height = 30;
    modules-left = ["clock"];
  });
  image = "${pkgs.nixos-icons}/share/icons/hicolor/256x256/apps/nix-snowflake.png";
  vkcube = pkgs.writeShellScript "vkcube-fifo" ''
    set -euo pipefail
    start=$(${pkgs.coreutils}/bin/date +%s%N)
    status=0
    WAYLAND_DEBUG=1 ${pkgs.vulkan-tools}/bin/vkcube --wsi wayland --present_mode 2 --c 300 || status=$?
    echo "vkcube exit=$status ms=$(( ($(${pkgs.coreutils}/bin/date +%s%N) - start) / 1000000 ))"
  '';
  windows = {
    foot = "${pkgs.foot}/bin/foot";
    alacritty = "${pkgs.alacritty}/bin/alacritty";
    kitty = "${pkgs.kitty}/bin/kitty";
    gtk3 = "${pkgs.gtk3.dev}/bin/gtk3-demo";
    gtk4 = "${pkgs.gtk4.dev}/bin/gtk4-demo";
    qt6 = "${pkgs.kdePackages.kcalc}/bin/kcalc";
    imv = "${pkgs.imv}/bin/imv ${image}";
    mpv = "${pkgs.mpv}/bin/mpv --no-config --vo=gpu --gpu-context=wayland --gpu-sw --loop=inf av://lavfi:testsrc=size=640x360:rate=30";
    xterm = "${pkgs.xterm}/bin/xterm";
    xeyes = "${pkgs.xorg.xeyes}/bin/xeyes";
    pcmanfm = "${pkgs.pcmanfm}/bin/pcmanfm";
  };
  others = {
    waybar = "${pkgs.waybar}/bin/waybar --config ${waybarConfig}";
    mako = "${pkgs.mako}/bin/mako";
    notify = "${pkgs.libnotify}/bin/notify-send tilde hello";
    fuzzel = "${pkgs.fuzzel}/bin/fuzzel";
    rofi = "${pkgs.rofi}/bin/rofi -show drun";
    swaylock = "${pkgs.swaylock}/bin/swaylock -c 336699";
    vkcube = "${vkcube}";
  };
in
  import ./vm.nix {
    inherit pkgs module;
    name = "tilde-apps";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {
      virtualisation.memorySize = pkgs.lib.mkForce 4096;
      users.users.alice.password = "alice";
      security.pam.services.swaylock = {};
      fonts.packages = [pkgs.dejavu_fonts];
    };
    extraTest = run: let
      params = builtins.toJSON {
        as_alice = run "{}";
        region = "${region}";
        wlr_randr = "${pkgs.wlr-randr}/bin/wlr-randr";
        windows = builtins.attrNames windows;
        launchers = builtins.mapAttrs launch (windows // others);
      };
    in ''
      import json
      params = json.loads(${builtins.toJSON params})
      exec(open("${./apps.py}").read())
    '';
  }
