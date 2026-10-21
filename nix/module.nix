{self, ...}: {
  flake.nixosModules.default = {
    config,
    lib,
    pkgs,
    modulesPath,
    ...
  }: let
    cfg = config.programs.tilde;
  in {
    options.programs.tilde = {
      enable = lib.mkEnableOption "tilde, a tiling Wayland compositor, as a display manager session";
      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.stdenv.hostPlatform.system}.tilde;
        defaultText = lib.literalExpression "tilde.packages.\${pkgs.stdenv.hostPlatform.system}.tilde";
        description = "The tilde package to run.";
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
