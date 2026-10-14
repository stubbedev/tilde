{
  pkgs,
  module,
}: let
  user = "alice";
  uid = 1000;
  run = command: "su ${user} -c 'XDG_RUNTIME_DIR=/run/user/${toString uid} ${command}'";
in
  pkgs.testers.runNixOSTest {
    name = "tilde-drm";

    nodes.machine = {config, ...}: {
      imports = [module];

      virtualisation = {
        graphics = true;
        cores = 2;
        memorySize = 2048;
        qemu.options = [
          "-vga none"
          "-device virtio-gpu-pci"
        ];
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
      machine.wait_for_unit("greetd.service")
      try:
          machine.wait_until_succeeds("${run "tildectl outputs"}", timeout=120)
      except Exception:
          print(machine.execute("journalctl -b --no-pager -t tilde -t greetd")[1])
          raise
      outputs = machine.succeed("${run "tildectl outputs"} | jq -r '.[].name'").split()
      assert outputs == ["Virtual-1"], f"outputs {outputs}"
      display = machine.succeed("ls /run/user/${toString uid} | grep -m1 '^wayland-[0-9]*$'").strip()
      machine.succeed("${run "WAYLAND_DISPLAY=$display grim /tmp/screen.png"}".replace("$display", display))
      machine.copy_from_machine("/tmp/screen.png")
    '';
  }
