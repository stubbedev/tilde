{
  pkgs,
  inputs,
  ...
}: {
  env =
    import ./nix/test-env.nix pkgs
    // {
      CGO_ENABLED = "0";
      GOTOOLCHAIN = "local";
    };

  packages = with pkgs;
    [
      go_1_27
      gopls
      golangci-lint
      delve
      govulncheck
      git
      gh
      mesa-demos
    ]
    ++ import ./nix/test-programs.nix {
      inherit pkgs;
      gelm = inputs.gelm.packages.${pkgs.stdenv.hostPlatform.system}.gelm;
    };

  enterTest = ''
    just gates
  '';
}
