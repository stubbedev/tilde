{
  perSystem = {
    inputs',
    pkgs,
    self',
    ...
  }: {
    devShells.default = pkgs.mkShell {
      inputsFrom = [self'.packages.tilde];

      packages = with pkgs;
        [
          gopls
          golangci-lint
          delve
          govulncheck
          git
          gh
          mesa-demos
        ]
        ++ import ./test-programs.nix {
          inherit pkgs;
          inherit (inputs'.gelm.packages) gelm;
        };

      env =
        import ./test-env.nix pkgs
        // {
          CGO_ENABLED = "0";
          GOTOOLCHAIN = "local";
        };
    };

    formatter = pkgs.alejandra;
  };
}
