import json

BAR = 30


def as_alice(command):
    return params["as_alice"].format(command)


def windows():
    return {w["id"]: w for w in json.loads(machine.succeed(as_alice("tildectl windows")))}


def region_command(x, y, w, h):
    return as_alice(f"WAYLAND_DISPLAY={display} {params['region']} {x} {y} {w} {h}")


def region(x, y, w, h):
    digest, colors, first = machine.succeed(region_command(x, y, w, h)).split()
    return digest, int(colors), first


def wait_for_region(x, y, w, h, condition, timeout):
    machine.wait_until_succeeds(f"{region_command(x, y, w, h)} | awk '{condition}' | grep -q .", timeout=timeout)


def launch(name):
    machine.succeed(as_alice(f"tildectl dispatch exec {params['launchers'][name]}"))


def dispatch(*args):
    machine.succeed(as_alice("tildectl dispatch " + " ".join(args)))


def protocol_errors(name):
    return machine.execute(f"grep -i -e 'protocol error' -e 'wl_display.*error' /tmp/{name}.log")[1].strip()


def dump():
    print(machine.execute("for f in /tmp/*.log; do echo \"== $f\"; tail -30 \"$f\"; done")[1])
    print(machine.execute("journalctl -b --no-pager -t tilde | tail -60")[1])
    print(machine.execute(as_alice("tildectl windows") + " | jq -c '.[] | {id, app_id, title, rect}'")[1])


def bar_drawn():
    wait_for_region(0, 0, 1280, BAR, "$2 > 1", 300)


def check_windows():
    checked = {}
    for workspace, name in enumerate(params["windows"], start=2):
        dispatch("workspace", str(workspace))
        before = set(windows())
        launch(name)
        machine.wait_until_succeeds(
            as_alice("tildectl windows") + f" | jq -e 'map(.id) - {json.dumps(sorted(before))} | length > 0'",
            timeout=300,
        )
        machine.sleep(5)
        [window] = [w for i, w in windows().items() if i not in before]
        rect = window["rect"]
        print(f"{name}: window {window['id']} {window['app_id']!r} {window['title']!r} at {rect}")
        assert rect["y"] >= BAR, f"{name} overlaps the bar's exclusive zone: {rect}"
        inset = 8
        _, colors, _ = region(rect["x"] + inset, rect["y"] + inset, rect["width"] - 2 * inset, rect["height"] - 2 * inset)
        assert colors > 1, f"{name} drew nothing: its region has {colors} colour"
        checked[name] = window["id"]
    still = windows()
    for name, id in checked.items():
        assert id in still, f"{name} closed its window"
        errors = protocol_errors(name)
        assert not errors, f"{name} hit a protocol error: {errors}"


def check_notification():
    dispatch("workspace", "1")
    launch("mako")
    machine.wait_until_succeeds("pgrep -u alice mako", timeout=60)
    digest, _, _ = region(880, BAR, 400, 200)
    launch("notify")
    wait_for_region(880, BAR, 400, 200, f"$1 != \"{digest}\"", 60)


def check_launcher(name):
    digest, _, _ = region(0, BAR, 1280, 800 - BAR)
    launch(name)
    wait_for_region(0, BAR, 1280, 800 - BAR, f"$1 != \"{digest}\"", 120)
    machine.sleep(2)
    machine.send_key("esc")
    machine.wait_until_fails(f"pgrep -u alice {name}", timeout=60)


def check_lock():
    launch("swaylock")
    wait_for_region(2, 2, 4, 4, "$2 == 1 && $3 == \"336699\"", 120)
    machine.sleep(2)
    machine.send_chars("alice\n")
    machine.wait_until_fails("pgrep -u alice swaylock", timeout=60)
    bar_drawn()


def randr(args=""):
    return as_alice(f"WAYLAND_DISPLAY={display} {params['wlr_randr']} {args}")


def check_randr():
    listing = machine.succeed(randr())
    print(listing)
    assert "Virtual-1" in listing, "wlr-randr does not list the output"
    outputs = as_alice("tildectl outputs")
    machine.succeed(randr("--output Virtual-1 --scale 2"))
    machine.wait_until_succeeds(f"{outputs} | jq -e '.[0].scale == 2 and .[0].rect.width == 640'", timeout=30)
    machine.succeed(randr("--output Virtual-1 --mode 1024x768"))
    machine.wait_until_succeeds(f"{outputs} | jq -e '.[0].mode.width == 1024 and .[0].mode.height == 768'", timeout=30)
    machine.succeed(randr("--output Virtual-1 --mode 1280x800 --scale 1"))
    machine.wait_until_succeeds(f"{outputs} | jq -e '.[0].scale == 1 and .[0].mode.width == 1280'", timeout=30)
    machine.fail(randr("--output Virtual-1 --transform 90"))


try:
    check_randr()
    launch("waybar")
    bar_drawn()
    check_windows()
    check_notification()
    check_launcher("fuzzel")
    check_launcher("rofi")
    check_lock()
    for name in ["waybar", "mako", "fuzzel", "rofi", "swaylock"]:
        errors = protocol_errors(name)
        assert not errors, f"{name} hit a protocol error: {errors}"
except Exception:
    dump()
    raise
