{
  pkgs,
  module,
}: let
  firefoxPrefs = pkgs.writeText "user.js" ''
    user_pref("media.navigator.permission.disabled", true);
    user_pref("browser.aboutwelcome.enabled", false);
    user_pref("browser.shell.checkDefaultBrowser", false);
    user_pref("browser.startup.homepage_override.mstone", "ignore");
    user_pref("datareporting.policy.dataSubmissionEnabled", false);
    user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
  '';
  launch = name: command: pkgs.writeShellScript "launch-${name}" "exec ${command} > /tmp/${name}.log 2>&1";
  animation = launch "animation" "${pkgs.weston}/bin/weston-presentation-shm";
  firefox = launch "firefox" "${pkgs.firefox}/bin/firefox --no-remote --profile /tmp/firefox file://${./screencast.html}";
  chromium = launch "chromium" "${pkgs.chromium}/bin/chromium --use-fake-ui-for-media-stream --no-first-run --user-data-dir=/tmp/chromium file://${./screencast.html}";
  python = pkgs.python3.withPackages (ps: [ps.dbus-python ps.pygobject3]);
  frames = pkgs.runCommandCC "pw-frames" {
    nativeBuildInputs = [pkgs.pkg-config];
    buildInputs = [pkgs.pipewire];
  } "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/pw-frames ${./pw-frames.c} $(pkg-config --cflags --libs libpipewire-0.3)";
in
  import ./vm.nix {
    inherit pkgs module;
    name = "tilde-screencast";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {
      config,
      pkgs,
      ...
    }: {
      services.pipewire.enable = true;
      virtualisation.memorySize = pkgs.lib.mkForce 4096;
      xdg.portal.wlr.settings.screencast = {
        chooser_type = "dmenu";
        chooser_cmd = "head -n1";
      };
      environment.systemPackages = [python frames];
      systemd.user.services.xdg-desktop-portal-wlr.serviceConfig.ExecStart = pkgs.lib.mkForce [
        ""
        "${pkgs.xdg-desktop-portal-wlr}/libexec/xdg-desktop-portal-wlr --loglevel=DEBUG --config=${(pkgs.formats.ini {}).generate "xdg-desktop-portal-wlr.ini" config.xdg.portal.wlr.settings}"
      ];
    };
    extraTest = run: let
      screencast = run "${python}/bin/python3 ${./screencast.py} {source} {cursor} 10 /tmp/{source}-{cursor}";
    in ''
      machine.succeed("${run "tildectl dispatch exec ${animation}"}")
      machine.wait_until_succeeds("${run "tildectl windows"} | jq -e 'length == 1'", timeout=30)
      for source, cursor in [("monitor", "hidden"), ("monitor", "embedded"), ("window", "hidden")]:
          try:
              print(machine.succeed("${screencast}".format(source=source, cursor=cursor)))
          except Exception:
              print(machine.execute("cat /tmp/animation.log; journalctl -b --no-pager _UID=1000 | grep -e xdg-desktop-portal-wlr -e pipewire | tail -150")[1])
              raise

      def share_in_browser(name, launcher, shared):
          machine.succeed(f"${run "tildectl dispatch exec {launcher}"}")
          windows = "${run "tildectl windows"}"
          try:
              machine.wait_until_succeeds(f"{windows} | jq -e 'any(.[]; .title | startswith(\"waiting\"))'", timeout=180)
              machine.sleep(5)
              machine.send_key("ret")
              machine.wait_until_succeeds(f"{windows} | jq -e 'all(.[]; .title | startswith(\"waiting\") | not)'", timeout=120)
              machine.wait_until_succeeds(f"{windows} | jq -e 'all(.[]; .title | test(\"^(sharing|waiting)\") | not)'", timeout=120)
          finally:
              print(machine.succeed(f"{windows} | jq -r '.[].title'"))
              print(machine.execute(f"tail -40 /tmp/{name}.log")[1])
          machine.succeed(f"{windows} | jq -e 'all(.[]; .title | test(\"^(sharing|waiting|failed)\") | not)'")
          machine.succeed(f"{windows} | jq -e '[.[] | select(.title | startswith(\"shared 1280x800\"))] | length == {shared}'")

      machine.succeed("${run "mkdir -p /tmp/firefox && cp ${firefoxPrefs} /tmp/firefox/user.js"}")
      share_in_browser("firefox", "${firefox}", 1)
      share_in_browser("chromium", "${chromium}", 2)
    '';
  }
