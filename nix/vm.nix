{
  pkgs,
  module,
  name,
  devices,
  outputs,
  kernelModules ? [],
  unplug ? null,
  lysWMConfig ? null,
  rendersOn ? "card0",
  machineConfig ? {},
  extraTest ? _: "",
}: let
  user = "alice";
  uid = 1000;
  runtime = "/run/user/${toString uid}";
  run = command: "su ${user} -c 'XDG_RUNTIME_DIR=${runtime} DBUS_SESSION_BUS_ADDRESS=unix:path=${runtime}/bus ${command}'";
in
  pkgs.testers.runNixOSTest {
    inherit name;

    nodes.machine = {config, ...}: {
      imports = [module machineConfig];

      boot.kernelModules = kernelModules;
      systemd.tmpfiles.settings = pkgs.lib.optionalAttrs (lysWMConfig != null) {
        lys-wm = {
          "/home/${user}/.config".d = {
            inherit user;
            group = "users";
          };
          "/home/${user}/.config/lys-wm".d = {
            inherit user;
            group = "users";
          };
          "/home/${user}/.config/lys-wm/config.yaml"."f+" = {
            inherit user;
            group = "users";
            argument = lysWMConfig;
          };
        };
      };
      virtualisation = {
        graphics = true;
        cores = 2;
        memorySize = 2048;
        qemu.options = ["-vga none"] ++ map (d: "-device ${d}") devices;
      };

      hardware.graphics.enable = true;
      programs.lys-wm.enable = true;
      users.users.${user} = {
        isNormalUser = true;
        inherit uid;
      };
      services.greetd = {
        enable = true;
        settings = let
          session = {
            command = "${pkgs.bash}/bin/bash -c '${config.programs.lys-wm.package}/bin/lys-wm --systemd 2>&1 | ${pkgs.systemd}/bin/systemd-cat -t lys-wm'";
            inherit user;
          };
        in {
          initial_session = session;
          default_session = session;
        };
      };
      environment.systemPackages = [pkgs.grim pkgs.jq];
    };

    testScript =
      ''
        want = ${builtins.toJSON outputs}
        machine.wait_for_unit("greetd.service")
        try:
            machine.wait_until_succeeds(f"${run "lys-wmctl outputs"} | jq -e 'length == {len(want)}'", timeout=120)
        except Exception:
            print(machine.execute("journalctl -b --no-pager -t lys-wm -t greetd")[1])
            raise
        print(machine.succeed("journalctl -b --no-pager -t lys-wm"))
        machine.fail("journalctl -b --no-pager -t lys-wm | grep 'drawing the cursor in the frame'")
        machine.succeed("journalctl -b --no-pager -t lys-wm | grep 'rendering on ${rendersOn},'")
        got = sorted(machine.succeed("${run "lys-wmctl outputs"} | jq -r '.[].name'").split())
        assert got == sorted(want), f"outputs {got}, want {want}"
        display = machine.succeed("ls ${runtime} | grep -m1 '^wayland-[0-9]*$'").strip()
        for output in want:
            machine.succeed(f"grep -qx enabled /sys/class/drm/card*-{output}/enabled")
            shot = f"/tmp/{output}.png"
            machine.succeed(f"${run "WAYLAND_DISPLAY={display} grim -o {output} {shot}"}")
            machine.copy_from_machine(shot)
            pixels = machine.succeed(f"${run "WAYLAND_DISPLAY={display} grim -t ppm -o {output} -"} | tail -n +4 | od -An -v -tx1 -w3 | sort -u").split("\n")
            assert [p.strip() for p in pixels if p.strip()] == ["1e 1e 2e"], f"{output} shows {pixels[:4]} besides the background"
      ''
      + extraTest run
      + pkgs.lib.optionalString (unplug != null) ''
        import json
        after = json.dumps(${builtins.toJSON unplug.outputs})
        machine.succeed("echo 1 > /sys/class/drm/${unplug.card}/device/remove")
        machine.wait_until_succeeds(f"${run "lys-wmctl outputs"} | jq -e --argjson want '{after}' 'map(.name) == $want'", timeout=60)
        machine.succeed("pgrep -x lys-wm")
        print(machine.succeed("journalctl -b --no-pager -t lys-wm | tail -5"))
      '';
  }
