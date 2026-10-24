{self, ...}: {
  flake.nixosModules.default = {
    config,
    lib,
    pkgs,
    modulesPath,
    ...
  }: let
    cfg = config.programs.lys-wm;
  in {
    options.programs.lys-wm = {
      enable = lib.mkEnableOption "lys-wm, a tiling Wayland compositor, as a display manager session";
      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.lys-wm;
        defaultText = lib.literalExpression "lys-wm.packages.\${pkgs.stdenv.hostPlatform.system}.lys-wm";
        description = "The lys-wm package to run.";
      };
    };

    config = lib.mkIf cfg.enable (lib.mkMerge [
      {
        environment.systemPackages = [cfg.package];
        services.displayManager.sessionPackages = [cfg.package];
        xdg.portal = {
          enable = true;
          configPackages = lib.mkDefault [cfg.package];
        };
      }
      (import (modulesPath + "/programs/wayland/wayland-session.nix") {
        inherit lib pkgs;
        enableXWayland = false;
      })
    ]);
  };
}
