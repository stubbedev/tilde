# IPC

tilde listens on `$XDG_RUNTIME_DIR/tilde/<WAYLAND_DISPLAY>.sock` and exports
the path as `TILDE_SOCKET` to everything it starts. `tildectl` finds it
through `--socket`, then `TILDE_SOCKET`, then the only socket in
`$XDG_RUNTIME_DIR/tilde`.

## tildectl

```sh
tildectl dispatch workspace 3     # any action from actions.md
tildectl reload                   # the same as: tildectl dispatch reload
tildectl outputs | workspaces | windows | activewindow | version
tildectl watch                    # events as JSON lines, until tilde exits
```

Queries print JSON. A failed request prints the error and exits 1.

## Protocol

A connection sends one request as a JSON line and reads one response line,
then the server closes it:

```json
{"request":"dispatch","action":"movetoworkspace 3"}
{"version":1}
```

`request` is one of `version`, `outputs`, `workspaces`, `windows`,
`activewindow`, `dispatch` (which needs `action`) and `watch`. Unknown
requests and fields are errors. The response always has `version`, which is
1 until the protocol changes incompatibly; it then has either `error` or, for
queries, `result`. A client should refuse to talk to a version it does not
know.

Requests must arrive within 5 seconds and be under 64 KiB.

### Results

`outputs`: a list of

```json
{"name":"DP-1","description":"LG Electronics 27GL850","rect":{"x":0,"y":0,"width":2048,"height":1152},
 "scale":1.25,"mode":{"width":2560,"height":1440,"refresh_mhz":143912},"workspace":1,"focused":true,"powered":true}
```

`rect` is in logical pixels; `workspace` is the one the output shows;
`powered` is false while `dpms off` or an idle daemon has the output off.

`workspaces`: a list of open workspaces, in ID order:

```json
{"id":1,"output":"DP-1","windows":3,"visible":true,"focused":true}
```

`windows`: a list of mapped windows, oldest first. `activewindow` is the
focused one, or `null`:

```json
{"id":7,"app_id":"foot","title":"~","pid":4242,"workspace":1,"output":"DP-1",
 "rect":{"x":0,"y":0,"width":1024,"height":1152},"floating":false,"fullscreen":false,"urgent":false,"focused":true}
```

Window IDs are never reused while tilde runs.

`version`: the number 1.

## Events

`watch` answers `{"version":1}`, then writes one JSON object per line. Each
has an `event` field and the fields below. Events that happen together
arrive in this order: dispatches, then each output with the workspace it
shows, the focused output, windows, focus, submap and layout.

| `event` | Fields | When |
| --- | --- | --- |
| `dispatch` | `action` | An action ran, from a bind or from IPC |
| `output` | `change` (`added`, `removed`, `changed`), `output` | An output appeared, went away, or changed its geometry, scale, mode, description or power |
| `workspace` | `output`, `workspace` | An output shows another workspace |
| `focusedoutput` | `output` | Focus moved to another output |
| `open` | `window` (the full window) | A window mapped |
| `close` | `window` (its ID) | A window unmapped |
| `move` | `window`, `workspace` | A window moved to another workspace |
| `title` | `window`, `title` | A window changed its title |
| `urgent` | `window`, `urgent` | A window asked for attention, or got it |
| `focus` | `window` (ID or `null`) | Keyboard focus moved to another window, or to none |
| `submap` | `submap` (`""` for the default binds) | The bind submap changed |
| `layout` | `layout` (`index`, `name`) | The keyboard layout group changed |

A watcher that falls 256 events behind is disconnected. Reconnect and
re-query to resynchronise.
