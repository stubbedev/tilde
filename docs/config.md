# Config

tilde reads one YAML file: `$XDG_CONFIG_HOME/tilde/config.yaml`, or
`~/.config/tilde/config.yaml` when `XDG_CONFIG_HOME` is unset. `tilde --config
<file>` reads another file instead, and that file must exist. With no file at
the default path, tilde runs the built-in config,
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
| `match` | A connector name (`DP-1`), or `desc:` and the start of the output's description (`desc:LG Electronics 27GL850`) |
| `mode` | `<width>x<height>` or `<width>x<height>@<hz>`. Without a rate, the fastest mode of that size; with one, the closest within 1 Hz. Default: the output's preferred mode |
| `position` | `<x>x<y>` in logical pixels. Outputs without a position line up to the right of all the others |
| `scale` | 0.25 to 10, in steps of 1/120. Default 1 |
| `disabled` | `true` turns the output off |

A mode the output does not have is reported, and the output keeps its current
mode.

## input.keyboard

| Key | Value |
| --- | --- |
| `rules`, `model`, `layout`, `variant`, `options` | The XKB names, as in `setxkbmap`. With none set, the `XKB_DEFAULT_*` variables apply |
| `repeat_rate` | Repeats per second; 0 turns repeat off. Default 50 |
| `repeat_delay` | Milliseconds before repeat starts. Default 300 |

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
| `font` | A TrueType file. Default: `TILDE_FONT`, then the DejaVu Sans the package was built with |
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

Actions are listed in [actions.md](actions.md).

## rules

```yaml
rules:
  - {app_id: ^steam$, float: true}
  - {app_id: ^steam$, title: ^Friends List$, size: 400x900}
```

`app_id` and `title` are regular expressions ([RE2](https://github.com/google/re2/wiki/Syntax));
a rule needs at least one. `float: true` or `tile: true` decides where a new
window goes; `size: <w>x<h>` sizes it when it floats. Later rules override
earlier ones.

## env and autostart

```yaml
env:
  XDG_CURRENT_DESKTOP: tilde
autostart:
  - waybar
```

`env` is set before anything starts, and every `autostart` command runs once
with `sh -c`.
