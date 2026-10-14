pkgs: name: command: pkgs.writeShellScript "launch-${name}" "exec ${command} > /tmp/${name}.log 2>&1"
