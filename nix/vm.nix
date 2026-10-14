{
  pkgs,
  module,
  name,
  devices,
  outputs,
  kernelModules ? [],
}: let
  user = "alice";
  uid = 1000;
  run = command: "su ${user} -c 'XDG_RUNTIME_DIR=/run/user/${toString uid} ${command}'";
in
  pkgs.testers.runNixOSTest {
    inherit name;

    nodes.machine = {config, ...}: {
      imports = [module];

      boot.kernelModules = kernelModules;
      virtualisation = {
        graphics = true;
        cores = 2;
        memorySize = 2048;
        qemu.options = ["-vga none"] ++ map (d: "-device ${d}") devices;
      };

      hardware.graphics.enable = true;
      programs.tilde.enable = true;
      users.users.${user} = {
        isNormalUser = true;
        inherit uid;
      };
      services.greetd = {
        enable = true;
        settings = let
          session = {
            command = "${pkgs.bash}/bin/bash -c '${config.programs.tilde.package}/bin/tilde --systemd 2>&1 | ${pkgs.systemd}/bin/systemd-cat -t tilde'";
            inherit user;
          };
        in {
          initial_session = session;
          default_session = session;
        };
      };
      environment.systemPackages = [pkgs.grim pkgs.jq];
    };

    testScript = ''
      want = ${builtins.toJSON outputs}
      machine.wait_for_unit("greetd.service")
      try:
          machine.wait_until_succeeds(f"${run "tildectl outputs"} | jq -e 'length == {len(want)}'", timeout=120)
      except Exception:
          print(machine.execute("journalctl -b --no-pager -t tilde -t greetd")[1])
          raise
      print(machine.succeed("journalctl -b --no-pager -t tilde"))
      got = sorted(machine.succeed("${run "tildectl outputs"} | jq -r '.[].name'").split())
      assert got == sorted(want), f"outputs {got}, want {want}"
      display = machine.succeed("ls /run/user/${toString uid} | grep -m1 '^wayland-[0-9]*$'").strip()
      for output in want:
          machine.succeed(f"grep -qx enabled /sys/class/drm/card*-{output}/enabled")
          shot = f"/tmp/{output}.png"
          machine.succeed(f"${run "WAYLAND_DISPLAY={display} grim -o {output} {shot}"}")
          machine.copy_from_machine(shot)
    '';
  }
