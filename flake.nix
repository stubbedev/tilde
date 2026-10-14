{
  description = "tilde - a pure-Go tiling Wayland compositor";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    gelm = {
      url = "github:stubbedev/gelm";
      inputs.nixpkgs.follows = "nixpkgs";
      inputs.flake-utils.follows = "flake-utils";
    };
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    gelm,
  }:
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = nixpkgs.legacyPackages.${system};
        # Nix has no version to read from the source tree, so builds from a
        # checkout are stamped with the commit date and hash.
        rev = self.shortRev or self.dirtyShortRev or "unknown";
        version = "0-unstable-${self.lastModifiedDate or "0"}";
      in {
        packages = rec {
          tilde = pkgs.callPackage ./package.nix {inherit rev version;};
          default = tilde;
        };

        apps = rec {
          tilde = flake-utils.lib.mkApp {drv = self.packages.${system}.tilde;};
          default = tilde;
        };

        checks = {
          vm = import ./nix/vm.nix {
            inherit pkgs;
            module = self.nixosModules.default;
            name = "tilde-drm";
            devices = ["virtio-gpu-pci"];
            outputs = ["Virtual-1"];
          };
          vm-two-gpus = import ./nix/vm.nix {
            inherit pkgs;
            module = self.nixosModules.default;
            name = "tilde-two-gpus";
            devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
            outputs = ["Virtual-1" "Virtual-2"];
            unplug = {
              card = "card1";
              outputs = ["Virtual-1"];
            };
          };
          vm-configured-gpu = import ./nix/vm.nix {
            inherit pkgs;
            module = self.nixosModules.default;
            name = "tilde-configured-gpu";
            devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
            tildeConfig = "gpu: card1";
            rendersOn = "card1";
            outputs = ["Virtual-1" "Virtual-2"];
          };
          vm-display-only = import ./nix/vm.nix {
            inherit pkgs;
            module = self.nixosModules.default;
            name = "tilde-display-only";
            devices = ["virtio-gpu-pci"];
            kernelModules = ["vkms"];
            outputs = ["Virtual-1" "Virtual-2"];
          };
          vm-screencast = import ./nix/screencast.nix {
            inherit pkgs;
            module = self.nixosModules.default;
          };
          vm-apps = import ./nix/apps.nix {
            inherit pkgs;
            module = self.nixosModules.default;
          };
          headless = pkgs.callPackage ./nix/headless.nix {
            tilde = self.packages.${system}.tilde;
            gelm = gelm.packages.${system}.gelm;
          };
          module = let
            machine = nixpkgs.lib.nixosSystem {
              modules = [
                self.nixosModules.default
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
            assert builtins.elem self.packages.${system}.tilde machine.config.xdg.portal.configPackages;
            assert builtins.elem "tilde" machine.config.services.displayManager.sessionData.sessionNames;
              pkgs.runCommand "tilde-module" {} ''
                grep -q "^Exec=${self.packages.${system}.tilde}/bin/tilde --systemd$" ${sessions}/share/wayland-sessions/tilde.desktop
                touch $out
              '';
        };

        formatter = pkgs.alejandra;
      }
    )
    // {
      nixosModules.default = import ./nix/module.nix self;
    };
}
