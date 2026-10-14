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
