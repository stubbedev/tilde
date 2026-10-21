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
        name = "tilde-drm";
        devices = ["virtio-gpu-pci"];
        outputs = ["Virtual-1"];
      };
      vm-two-gpus = vm {
        name = "tilde-two-gpus";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        outputs = ["Virtual-1" "Virtual-2"];
        unplug = {
          card = "card1";
          outputs = ["Virtual-1"];
        };
      };
      vm-configured-gpu = vm {
        name = "tilde-configured-gpu";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        tildeConfig = "gpu: card1";
        rendersOn = "card1";
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-display-only = vm {
        name = "tilde-display-only";
        devices = ["virtio-gpu-pci"];
        kernelModules = ["vkms"];
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-screencast = import ./screencast.nix {inherit pkgs module;};
      vm-apps = import ./apps.nix {inherit pkgs module;};
      headless = pkgs.callPackage ./headless.nix {
        inherit (self'.packages) tilde;
        inherit (inputs'.gelm.packages) gelm;
      };
      module = let
        machine = inputs.nixpkgs.lib.nixosSystem {
          modules = [
            module
            {
              nixpkgs.hostPlatform = system;
              programs.tilde.enable = true;
              services.greetd.enable = true;
              boot.loader.grub.enable = false;
              fileSystems."/".device = "nodev";
              system.stateVersion = "25.11";
            }
          ];
        };
        sessions = machine.config.services.displayManager.sessionData.desktops;
      in
        assert builtins.elem self'.packages.tilde machine.config.xdg.portal.configPackages;
        assert builtins.elem "tilde" machine.config.services.displayManager.sessionData.sessionNames;
          pkgs.runCommand "tilde-module" {} ''
            grep -q "^Exec=${self'.packages.tilde}/bin/tilde --systemd$" ${sessions}/share/wayland-sessions/tilde.desktop
            touch $out
          '';
    };
  };
}
