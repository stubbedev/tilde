{
  pkgs,
  tilde,
  gelm,
}:
tilde.overrideAttrs (old: {
  pname = "tilde-headless";
  doCheck = true;
  nativeCheckInputs = import ./test-programs.nix {inherit pkgs gelm;};
  env = old.env // import ./test-env.nix pkgs;
  checkPhase = ''
    runHook preCheck
    export HOME="$TMPDIR"
    just headless
    runHook postCheck
  '';
})
