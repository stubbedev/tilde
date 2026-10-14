# Architecture

The decisions every change builds on. Scope and non-goals are in the
[README](../README.md); the plan is in the issues. When a decision changes,
change it here in the same commit and say why in the issue that changed it.

## Pure Go

tilde builds with `CGO_ENABLED=0`, always. A C library is used only when no
reasonable Go alternative exists, and then it is loaded at runtime with
[purego](https://github.com/ebitengine/purego) (`dlopen`), never linked.

| Need | Source |
| --- | --- |
| EGL, GLES2, GBM | `libEGL`, `libGLESv2`, `libgbm` through purego (#33) |
| Input devices | `libinput` through purego (#42) |
| Keymaps and keyboard state | `libxkbcommon` through purego (#13) |
| Wayland wire protocol | in-repo codec and scanner (#3, #4) |
| DRM/KMS ioctls, epoll, timerfd, signalfd, memfd | [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix) |
| logind | [`github.com/godbus/dbus/v5`](https://github.com/godbus/dbus) |
| udev | netlink, read directly (#39) |
| X11 window manager | [`github.com/jezek/xgb`](https://github.com/jezek/xgb) (#45) |
| Text | [`github.com/go-text/typesetting`](https://github.com/go-text/typesetting), through gelm's `render` |

Adding a purego library, or a dependency to this table, is a decision issue.

libxkbcommon replaced xkb-go (#13): xkb-go's RMLVO compiler drops the
compatibility section, so no key sets a modifier, and serializes keys
with several groups in a form libxkbcommon refuses to parse. Clients read
keymaps with libxkbcommon anyway, so the compositor compiles and serves
them with it too. The purego libraries' paths are baked in at build time
by the Nix package; `TILDE_LIBXKBCOMMON` overrides libxkbcommon's.

## Threading

One goroutine owns all compositor state and runs the event loop (#5). Other
goroutines exist only at the edges, for blocking work such as D-Bus. They
post closures to the loop's mailbox and never read or write state
themselves. Nothing in the compositor takes a lock to protect its state.

## Pure core

`internal/wm` (#21) knows nothing about Wayland, X11 or rendering. It is a
value-level model of the container tree, workspaces and focus, with
functions from state and event to new state and effects. It imports only
the standard library. The protocol glue translates both ways: client
requests and backend input into `wm` events, and `wm` effects into protocol
events and render state.

### Container tree

Each workspace is a tree in the hy3 model (#21): a root group (split
horizontally when the area is wider than tall), split groups that divide
their area by child ratios that always sum to the child count, tab groups
that show one child below a tab bar, and windows as leaves. These rules
were read from hy3 at the pinned commit; its code is not ported.

- A new window goes right after the focused window, in the same group, and
  takes focus.
- Removing a child shares its ratio among its siblings. Focus moves to the
  previous sibling, or to the next one when the removed child came first.
- Removing a window collapses only invalid groups, walking up from where
  the window was: an emptied group goes away; a group left with one child
  collapses into that child when it is an active ephemeral group, a split
  holding a split, or a tab group holding a tab group of one tab. The root
  group keeps a lone window. hy3's configurable `node_collapse_policy`
  (2, empty splits, as configured today) applies only to moves (#23).
- An ephemeral group made around one window is staged, becomes active once
  it holds two children, and then dissolves when it drops back to one.

Layout is a pure function from tree and area to placements. Split edges
are rounded from cumulative ratios, so neighbours share an edge and the
children always cover the area exactly.

### Workspaces

Workspaces follow Hyprland (#24). They are numbered from 1, created when
first used, and dropped once they are empty and no longer shown; a new
output shows the first free one. Switching to a workspace shown on another
output moves focus to that output. `e+N` and `e-N` step through the open
workspaces of every output in ID order, wrapping around. Outputs are
known by name: unplugging one moves its workspaces to the focused output,
and plugging it back returns them and shows the one it was showing.

## Dispatch

One typed `Action` set and one dispatcher, in `wm` (#66). Keybinds,
`tildectl dispatch`, the IPC socket and protocol requests such as
foreign-toplevel activate are all callers of it. Nothing else mutates WM
state.

## Packages

Everything lives under `internal/` except the commands.

| Package | Owns |
| --- | --- |
| `cmd/tilde` | The compositor binary: builds the loop, server and backend, and runs them |
| `compositor` | Connects backend events to the server, places windows and draws each output's frames; `cmd/tilde` and the harness both build on it |
| `cmd/tildectl` | The IPC client (#31) |
| `wire` | The message codec and fd passing (#3), and the object runtime generated bindings build on (#4) |
| `scanner` | Generates `protocol/{server,client}/...` from the XML pinned in `protocol/protocols.json` (#4) |
| `protocol/...` | Generated bindings, one package per protocol and side; never edited by hand |
| `loop` | The event loop: fd sources, timers, signals, idle queue, mailbox (#5) |
| `server` | Display socket, clients, object maps, globals (#6) |
| `wlclient` | The client side of a connection: object map, zombies and dispatch, for the nested backend and tests (#6, #9) |
| `backend` | The backend interface: outputs, frames, input devices (#8) |
| `backend/headless` | Virtual outputs and injected input, for tests (#8) |
| `backend/nested` | Outputs as windows on a parent Wayland compositor (#9) |
| `backend/drm` | Atomic KMS on one DRM device (#40) |
| `render` | The renderer interface (#7) |
| `render/soft` | The CPU renderer, for headless and golden tests (#7) |
| `render/gles` | The GLES2 renderer (#34) |
| `gpu` | purego bindings for EGL, GLES2 and GBM (#33) |
| `surface` | `wl_compositor`, `wl_surface`, `wl_region`, `wl_subcompositor`, `wp_viewporter`, `wp_fractional_scale_v1`, `wp_presentation`: double-buffered state, subsurface trees, roles (#11, #19) |
| `singlepixel` | `wp_single_pixel_buffer_manager_v1`: one-pixel buffers of a solid color (#20) |
| `region` | Pixel regions as disjoint rectangles, for damage and input and opaque regions |
| `output` | `wl_output` and `zxdg_output_v1`: each output's mode, logical position and scale in 120ths, with rounding that keeps logical edges adjacent in device pixels (#19) |
| `shm` | `wl_shm` pools and buffers, read fault-safely when a client truncates its pool (#12) |
| `seat` | `wl_seat`, keyboard, pointer and touch, `wp_cursor_shape_v1` (#13, #14) |
| `data` | `wl_data_device_manager` and `zwp_primary_selection_device_manager_v1`: the clipboard and primary selection offered to the keyboard focus, and drag and drop (#18) |
| `xcursor` | Xcursor files and theme lookup through `XCURSOR_PATH` and `Inherits`, and the built-in arrow (#14) |
| `xkb` | Keymap compilation, sealed keymap fds and modifier state through libxkbcommon (#13) |
| `shell` | xdg-shell and layer-shell glue between clients and `wm`: toplevels, popups placed by their positioner, explicit popup grabs, server-side decorations, activation tokens (#15, #16, #17, #20, #48) |
| `wm` | The pure core and the dispatcher (#21, #66) |
| `xwm` | The X11 window manager for XWayland (#45) |
| `config` | The typed config, its validation and reload (#30) |
| `ipc` | The IPC socket and event stream (#31) |
| `session` | logind, udev and libinput: device access, hotplug and VT switching (#38, #39, #42) |
| `harness` | The compositor-in-the-loop harness (#10) |
| `testclient` | A scripted Wayland client that maps toplevels and animates shm frames, for integration tests |
| `testenv` | Whether a test skips or fails when a dev-shell tool or library is missing |
| `golden` | Golden-image comparison for software-rendered frames |

Dependencies point inward: `wm` imports nothing from tilde; `wire` and
`loop` import nothing from tilde; `protocol/...` imports only `wire` and
other protocol packages of the same side, since extensions name core
interfaces;
backends and renderers do not import `server`, `shell` or `wm`. `cmd/tilde`
is the only place that knows every package.

## Protocol bindings

`internal/protocol/protocols.json` pins every protocol source and maps each
XML file to a package. `go run ./internal/scanner -fetch` refreshes the
vendored XML from it; `go generate ./internal/scanner` regenerates both
sides, and a test fails when the committed tree is stale.

Each interface becomes a type wrapping `wire.Object`. Incoming messages
(requests on the server, events on the client) go to a per-interface
`Handler` with one typed method each, the object first; object arguments
are looked up and type-checked before the handler runs. A message that
creates an object hands the new object to the handler, which returns that
object's handler, so no object exists without one. Outgoing messages are
typed methods, and sending one newer than the bound version is an error,
never a silent drop. The server's client and the client-side display
implement `wire.Peer`: the object map, id allocation and removal.

## Devices

Nothing assumes a single GPU. Each DRM device owns its own `backend/drm`
instance and its own renderer. The primary render device is an explicit
choice passed to whatever needs it, never an implicit global. Multi-GPU
itself lands in M9 (#62), which records the selection rule here.

## Testing

- Unit tests for the pure core and every package with logic of its own.
- Golden images, byte-exact, from the software renderer.
- The compositor-in-the-loop harness (#10) boots the same compositor
  `cmd/tilde` runs on the headless backend, in process, with a private
  `XDG_RUNTIME_DIR`, and drives real clients from the dev shell against it.
  `just headless` runs it with `TILDE_HEADLESS=1`, which turns a missing
  client into a failure; it is part of `just gates`, so CI runs it too.

## Reference implementations

Hyprland no longer uses wlroots.
[aquamarine](https://github.com/hyprwm/aquamarine) is its backend library
(DRM/KMS, session, libinput, GBM, headless and nested backends) and
[hyprwayland-scanner](https://github.com/hyprwm/hyprwayland-scanner)
generates its protocol bindings; the wire format still comes from
libwayland-server. Hyprland's `hyprtester` is its compositor-in-the-loop
suite and the reference for #10. Issues link the relevant files at pinned
commits under a **Reference** heading.

Licences decide how they may be used:

- Hyprland, aquamarine and hyprwayland-scanner are BSD-3-Clause, libwayland
  is MIT. Their logic may be ported; ported code keeps the original
  copyright notice and licence in `THIRD_PARTY.md`.
- hy3 is GPL-3.0. Read it to pin down behaviour (collapse policy, autotile,
  move semantics); never port its code.
