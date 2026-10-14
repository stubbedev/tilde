# Actions

Every keybind and `tildectl dispatch` takes one of these. This file is generated from `wm.Actions`; run `go test ./internal/wm -update` after changing them.

| Action | Arguments | Effect |
| --- | --- | --- |
| `focus` | `<left\|right\|up\|down>` | Move focus to the neighbouring window, or to the next output at the edge. |
| `move` | `<left\|right\|up\|down>` | Move the focused window, hy3 style; at the workspace edge nothing happens. |
| `workspace` | `<n\|e+n\|e-n>` | Show workspace n, or the n-th open workspace after or before the current one. |
| `movetoworkspace` | `<n\|e+n\|e-n>` | Move the focused window to a workspace without following it. |
| `kill` |  | Ask the focused window to close. |
| `exec` | `<command>` | Run a shell command with sh -c. |
| `makegroup` | `<h\|v\|tab> [ephemeral]` | Wrap the focused window in a new group; an ephemeral group dissolves when one window is left. |
| `submap` | `<name\|reset>` | Switch binds to the named submap, or back to the default ones. |
| `togglefloating` |  | Float the focused window centred at half the output, or tile it again. |
| `fullscreen` |  | Make the focused window cover its output, above the top layer, or end that. |
| `movewindow` |  | Mouse binds only: drag the window under the pointer; tiled windows drop into the tree where released. |
| `resizewindow` |  | Mouse binds only: resize the window under the pointer from its nearest corner; tiled windows change their split ratios. |
