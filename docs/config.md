# Config

lys-wm reads one YAML file: `$XDG_CONFIG_HOME/lys-wm/config.yaml`, or
`~/.config/lys-wm/config.yaml` when `XDG_CONFIG_HOME` is unset. `lys-wm --config
<file>` reads another file instead, and that file must exist. With no file at
the default path, lys-wm runs the built-in config,
[`internal/config/default.yaml`](../internal/config/default.yaml), which is a
good starting point to copy.

Unknown keys are errors. Every error gives the file, the line and the key:

```
config.yaml:12: binds[3].action: bad action: unknown action "teleport"
```

`SIGHUP` and the `reload` action read the file again. A file that does not
parse, or whose chords, layout or font do not load, is rejected whole and the
running config stays. Nothing reloads when the file changes.

Every section is optional. An empty file gives the defaults.

## outputs

A list of rules. Each output takes the first rule that matches it.

| Key | Value |
| --- | --- |
| `match` | A connector name (`DP-1`), or `desc:` and the start of the output's description: its EDID make, model and serial, as in `desc:Samsung Electric Company LC32G5xT` |
| `mode` | `preferred` (the default), `highres` (the largest mode, then the fastest), `<width>x<height>`, or `<width>x<height>@<hz>`. Without a rate, the fastest mode of that size; with one, the closest within 1 Hz |
| `position` | `<x>x<y>` in logical pixels, or `auto` (the default): outputs without a position line up to the right of all the others |
| `scale` | 0.25 to 10, in steps of 1/120. Default 1 |
| `disabled` | `true` turns the output off |

A mode the output does not have is reported, and the output keeps its current
mode.

Display tools such as wlr-randr, kanshi, wdisplays and nwg-displays change
outputs while lys-wm runs, through wlr-output-management. Their changes take the
place of these rules for the outputs they name until the config is reloaded,
which applies the rules again. lys-wm refuses a configuration that rotates an
output, turns on adaptive sync, uses a mode the output does not list, or turns
every output off.

While the laptop lid is closed, internal panels (`eDP-*`) are off whatever the
rules say, and their workspaces move to the other outputs; opening the lid
brings the panel back under its rule. libinput turns off the internal keyboard
and touchpad while the lid is closed.

## input.keyboard

| Key | Value |
| --- | --- |
| `rules`, `model`, `layout`, `variant`, `options` | The XKB names, as in `setxkbmap`. With none set, the `XKB_DEFAULT_*` variables apply |
| `repeat_rate` | Repeats per second; 0 turns repeat off. Default 50 |
| `repeat_delay` | Milliseconds before repeat starts. Default 300 |

## input.pointer, input.touchpad and input.devices

`input.touchpad` applies to touchpads (devices that can tap), `input.pointer` to
every other pointing device. `input.devices` maps a libinput device name, as
`libinput list-devices` prints it, to settings that override both for that
device.

```yaml
input:
  pointer:
    accel_profile: flat
  touchpad:
    tap: true
    natural_scroll: true
  devices:
    "Logitech MX Master 3":
      scroll_factor: 0.5
```

| Key | Value |
| --- | --- |
| `tap`, `drag_lock` | Tap to click, and keep a tap-and-drag going after the finger lifts |
| `natural_scroll` | Content follows the fingers |
| `disable_while_typing` | Ignore the touchpad while the keyboard is in use |
| `middle_emulation` | Pressing left and right together is a middle click |
| `click_method` | `button_areas`, `clickfinger` or `none` |
| `scroll_method` | `two_finger`, `edge`, `on_button_down` or `none` |
| `accel_profile` | `adaptive` or `flat` |
| `accel_speed` | -1 to 1 |
| `scroll_factor` | Multiplies scroll distances; above 0, up to 100. Default 1 |

An unset key keeps libinput's default for the device, and a reload that drops a
key restores that default. A device that lacks a setting from `input.pointer`
or `input.touchpad` skips it; one that lacks a setting named for it under
`input.devices` is reported. These settings apply with the drm backend; nested,
the host compositor configures the devices.

## Colours

A colour is a palette name or `#rrggbb[aa]`. The palette starts as
[Catppuccin Mocha](https://catppuccin.com/palette) (`mauve`, `crust`,
`surface0`, …); `palette` adds names or overrides them:

```yaml
palette:
  accent: "#ff8800"
```

## borders

| Key | Default |
| --- | --- |
| `width` | 1 |
| `active` | `mauve` |
| `inactive` | `crust` |

## tabs

| Key | Default |
| --- | --- |
| `height` | 15 |
| `padding` | 5, between the tab strip and the windows |
| `text_height` | 8 |
| `text_padding` | 3 |
| `font` | A TrueType file. Default: `LYS_WM_FONT`, then the DejaVu Sans the package was built with |
| `active`, `inactive`, `urgent` | `background` and `text` colours, both required. Defaults `mauve`/`crust`, `surface0`/`text` and `red`/`crust` |

## autotile

| Key | Default |
| --- | --- |
| `enabled` | `true` |
| `ephemeral` | `true`: groups autotile creates dissolve when one window is left |
| `min_trigger` | 400 |
| `divisor` | 3 |

When one more column would make the columns narrower than `min_trigger` or
the output width divided by `divisor`, whichever is larger, the new window
stacks under the focused one instead.

## binds and submaps

```yaml
binds:
  - chord: SUPER + SHIFT + Q
    action: kill
  - {chord: SUPER + R, action: submap resize}
submaps:
  resize:
    - {chord: Right, action: resizeactive 20 0, flags: [repeating]}
    - {chord: Escape, action: submap reset}
```

A chord is modifiers and a trigger joined by `+`. Modifiers are `SUPER`
(`MOD4`, `WIN`, `LOGO`), `SHIFT`, `CTRL` (`CONTROL`) and `ALT` (`MOD1`). The
trigger is a keysym from the first layout (`Return`, `Q`, `bracketleft`,
`XF86AudioRaiseVolume`), `mouse:<button code>` (`mouse:272` is the left
button) or `mouse_up`/`mouse_down` for the scroll wheel.

`flags` takes `locked` (the bind works while the session is locked) and
`repeating` (the action repeats while the key is held, at the keyboard
repeat rate).

A focused window that inhibits shortcuts (virtual machines, remote desktop
clients) receives every key, button and scroll itself; only `locked` binds
still run, so give the bind that takes focus back from such a window the
`locked` flag.

Actions are listed in [actions.md](actions.md).

## rules

```yaml
rules:
  - {app_id: ^steam$, float: true}
  - {app_id: ^steam$, title: ^Friends List$, size: 400x900}
```

`app_id` and `title` are regular expressions ([RE2](https://github.com/google/re2/wiki/Syntax));
a rule needs at least one. `float: true` or `tile: true` decides where a new
window goes; `size: <w>x<h>` sizes it when it floats. `tearing: true` or
`tearing: false` overrides what the window asks for through
`wp_tearing_control_v1` (see [tearing](#tearing)). Later rules override
earlier ones.

X11 windows match `app_id` against the class half of `WM_CLASS` (`Steam`,
`jetbrains-idea`). Without a rule, a window floats when it has a parent
(`WM_TRANSIENT_FOR` for X11), is modal, is a dialog, utility, splash or
toolbar window, or has a fixed size; `tile: true` overrides that.

## env and autostart

```yaml
env:
  QT_QPA_PLATFORM: wayland;xcb
autostart:
  - waybar
```

lys-wm sets `env` in its own environment at startup, before it starts
anything, so every program it runs inherits it. `XDG_CURRENT_DESKTOP=lys-wm`
and `XDG_SESSION_TYPE=wayland` are set unless `env` overrides them.
`WAYLAND_DISPLAY`, `DISPLAY` and `LYS_WM_SOCKET` belong to lys-wm and cannot be
set here. `XCURSOR_THEME`, `XCURSOR_SIZE` and `XCURSOR_PATH` set here choose the
pointer for lys-wm as well as for its clients. Each `autostart` command runs once with `sh -c`, after the display
and the IPC socket are up. A reload changes neither.

## xwayland

```yaml
xwayland:
  enabled: true
  zero_scaling: true
```

X11 programs run through Xwayland. lys-wm takes a free X display (`:0` and
up, skipping displays whose lock in `/tmp` belongs to a live process),
listens on its sockets, and exports `DISPLAY`; Xwayland itself starts only
when the first X client connects, so a session without X programs never
runs it. If Xwayland exits, the next X client starts it again. `enabled:
false` leaves `DISPLAY` unset and binds no X sockets.

With `zero_scaling: true` (the default), X programs see every output at its
physical resolution with scale 1 and draw at full sharpness; on an output
scaled to 1.5 they look smaller unless they scale themselves (`GDK_SCALE`,
`QT_SCALE_FACTOR`, Steam's and JetBrains' own settings). With `false`,
Xwayland sees the logical layout and lys-wm stretches X windows, which keeps
their size but blurs them on scaled outputs. Both settings are read at
startup; a reload does not change them.

## tearing

```yaml
tearing: true
rules:
  - {app_id: ^cs2$, tearing: true}
```

Off by default. With `tearing: true`, a window that fills an output and
scans out directly is presented with an async page flip, so frames show the
moment they are ready and may tear, when it asks for that through
`wp_tearing_control_v1` (Mesa's Vulkan `IMMEDIATE` present mode does) or a
rule says `tearing: true`. A rule with `tearing: false` keeps a window on
vblank whatever it asks. Drivers without atomic async flips (Linux before
6.8), and frames that change more than the buffer, flip at vblank as
usual.

## gpu

```yaml
gpu: card1
```

The DRM card that renders, as `card1` or `/dev/dri/card1`. Without it lys-wm
picks the boot VGA device if it has a render node, else the first card with
a render node. Every other card shows its outputs through a copy from this
one. Read at startup only.

## Locking

lys-wm speaks `ext-session-lock-v1`, so lockers such as swaylock, hyprlock or
lys-shell's lock screen work. While the session is locked, nothing but the lock
surfaces renders or receives input, and only binds flagged `locked` run.
lys-wm tells the locker it is locked only after every output has drawn a
blank frame. If the locker dies, the session stays locked on a dark red
screen until a new locker takes over; outputs added while locked show the
lock colour until the locker covers them. lys-wm does not set logind's
`LockedHint`: the locker does, as lys-shell does today.

## Session

`lys-wm --systemd` hands `env`, `WAYLAND_DISPLAY`, `DISPLAY` and `LYS_WM_SOCKET`
to D-Bus activation and the systemd user manager. It then starts
`lys-wm-session.target`, which binds `graphical-session.target`, so user
services that want a graphical session start with lys-wm. On exit lys-wm stops
the target and removes those variables from the user manager again. D-Bus
cannot unset a variable, so the activation environment keeps them.

A display manager session should run `lys-wm --systemd`. Leave the flag off
when running lys-wm nested in another session, which owns
`graphical-session.target`.

Portals follow `XDG_CURRENT_DESKTOP=lys-wm`: the package installs
`lys-wm-portals.conf`, which routes every portal to lys-shell's backend and falls
back to xdg-desktop-portal-wlr for screen sharing and screenshots and to
xdg-desktop-portal-gtk for the rest. Portal backends are started by D-Bus
activation, so they reach the session only when lys-wm runs with `--systemd`.
