{
  inputs,
  self,
  ...
}: {
  perSystem = {
    inputs',
    pkgs,
    self',
    system,
    ...
  }: let
    module = self.nixosModules.default;
    vm = args: import ./vm.nix ({inherit pkgs module;} // args);
  in {
    checks = {
      vm = vm {
        name = "lys-wm-drm";
        devices = ["virtio-gpu-pci"];
        outputs = ["Virtual-1"];
      };
      vm-two-gpus = vm {
        name = "lys-wm-two-gpus";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        outputs = ["Virtual-1" "Virtual-2"];
        unplug = {
          card = "card1";
          outputs = ["Virtual-1"];
        };
      };
      vm-configured-gpu = vm {
        name = "lys-wm-configured-gpu";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        lysWMConfig = "gpu: card1";
        rendersOn = "card1";
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-display-only = vm {
        name = "lys-wm-display-only";
        devices = ["virtio-gpu-pci"];
        kernelModules = ["vkms"];
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-screencast = import ./screencast.nix {inherit pkgs module;};
      vm-apps = import ./apps.nix {inherit pkgs module;};
      headless = pkgs.callPackage ./headless.nix {
        inherit (self'.packages) lys-wm;
        inherit (inputs'.lys-ui.packages) lys-ui;
      };
      module = let
        machine = inputs.nixpkgs.lib.nixosSystem {
          modules = [
            module
            {
              nixpkgs.hostPlatform = system;
              programs.lys-wm.enable = true;
              services.greetd.enable = true;
              boot.loader.grub.enable = false;
              fileSystems."/".device = "nodev";
              system.stateVersion = "25.11";
            }
          ];
        };
        sessions = machine.config.services.displayManager.sessionData.desktops;
      in
        assert builtins.elem self'.packages.lys-wm machine.config.xdg.portal.configPackages;
        assert builtins.elem "lys-wm" machine.config.services.displayManager.sessionData.sessionNames;
          pkgs.runCommand "lys-wm-module" {} ''
            grep -q "^Exec=${self'.packages.lys-wm}/bin/lys-wm --systemd$" ${sessions}/share/wayland-sessions/lys-wm.desktop
            touch $out
          '';
    };
  };
}
