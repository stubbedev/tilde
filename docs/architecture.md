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
| Config file | [`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml), strict, as wayle uses (#30) |

Adding a purego library, or a dependency to this table, is a decision issue.

libxkbcommon replaced xkb-go (#13): xkb-go's RMLVO compiler drops the
compatibility section, so no key sets a modifier, and serializes keys
with several groups in a form libxkbcommon refuses to parse. Clients read
keymaps with libxkbcommon anyway, so the compositor compiles and serves
them with it too. The purego libraries' paths are baked in at build time
by the Nix package; `TILDE_LIBXKBCOMMON` overrides libxkbcommon's.

Tab titles are drawn with gelm's public `render` package. gelm's font
discovery is internal to it, so the tab font is baked in the same way:
the Nix package sets DejaVu Sans as the default and `TILDE_FONT` overrides
it, until the config (#30) names one.

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

## GPU

`internal/gpu` (#33) loads glvnd's `libEGL.so.1` and `libGLESv2.so.2` and
Mesa's `libgbm.so.1` through purego. The Nix build bakes their store paths
in, and `TILDE_LIBEGL`, `TILDE_LIBGLESV2` and `TILDE_LIBGBM` override them.
glvnd picks the vendor driver, NVIDIA or Mesa, from
`/run/opengl-driver/share/glvnd/egl_vendor.d`. Displays come from the GBM
platform for KMS, the device platform, or Mesa's surfaceless platform; each
has one configless GLES2 context and no surfaces, and renders into dmabufs
or textures.

Without cgo, `os.Setenv` does not reach libc's `getenv`, so variables that
drivers read must be in the process environment from the start. The dev
shell sets `__EGL_VENDOR_LIBRARY_DIRS` to Mesa's vendor and
`LIBGL_ALWAYS_SOFTWARE=1`, so GPU tests render on llvmpipe the same way
locally and in CI. Run them with both unset to exercise the machine's
driver: the dmabuf test only runs where a vendor drives a render node.

The pointer rides a cursor plane where the output has one (#83). The
compositor renders the cursor image, a theme cursor or a client's cursor
surface, into an offscreen target, and the drm backend copies it into a
linear ARGB8888 dumb buffer, the only buffer every cursor plane takes:
NVIDIA's cursor planes scan out linear only, and its EGL renders to no
linear modifier. Pointer motion then moves the plane in an atomic commit of
its own, or in the next frame's commit, and repaints nothing. An image
larger than the plane, or a plane whose test commit refuses it, puts the
cursor back into the frame; a refusal is logged and lasts for the output's
lifetime.

tilde drives every DRM card on its seat, one KMS backend each (#62), and
renders every output on one primary GPU, because client buffers become
textures in that GPU's context. The primary is the card named by `gpu` in
the config, else the boot VGA device (`boot_vga` in sysfs) if it has a
render node, else the first card with a render node, else the first card,
rendering in software; the choice and its reason are logged at startup.
The other cards show outputs the primary rendered (#63): their frames are
drawn into the primary's offscreen targets and copied into the card's
linear dumb buffers before each flip, the same copy the cursor plane uses,
so display-only devices such as vkms and DisplayLink work too. Outputs keep
the kernel's connector names, which are unique across cards because the
kernel numbers the connectors of each type globally. Cards come and go with udev: a
new card is driven as a secondary, a removed secondary drops its outputs
without touching the gone device, and losing the primary is fatal.

A frame that is only the background and one opaque, untransformed client
dmabuf covering the output skips composition (#101): the buffer goes to the
primary plane directly when the cursor has its own plane and a test commit
takes it. The client's wl_buffer release, and its explicit-sync release
point, wait until a later flip takes the buffer off screen. While a surface
fills an output this way, its dmabuf surface feedback leads with a scanout
tranche (#64): the primary plane's opaque formats and modifiers that the
renderer also imports, targeted at the plane's DRM device, so the client
allocates buffers that plane can show.

## Content updates

Each `wl_surface.commit` makes a content update (#117). Extensions attach
what they need to the update when it is committed: gates it must pass
before it applies (an explicit-sync acquire point, a fifo barrier, a commit
timing target) and state that becomes current with it (a release point, an
alpha multiplier, a pointer constraint region). Updates apply strictly in
commit order, so a held one holds back the ones behind it rather than being
overwritten by them, which is what lets fifo queue frames instead of
dropping them. A synchronized subsurface keeps one cached update that
absorbs its commits and enters its queue when the parent applies.

A frame latches what it draws: the presentation feedback of every surface
it shows moves from the surface to that frame, and the output's next vblank
reports it presented (#121). An update that applies after the latch, as a fifo
waiter does, neither discards the latched feedback nor is reported for a
refresh that did not show it. The fifo barrier clears at that latch, at once
when no powered output shows the surface, and when an output powers off or
goes away. A commit timing target is due once the earliest next flip of an
output showing the surface reaches it, or at the target time itself, which a
timer guarantees while the outputs are idle.

## Xwayland

`internal/xwayland` (#44) starts Xwayland lazily. At startup tilde takes an
X display number with a lock file in `/tmp`, binds `/tmp/.X11-unix/X<n>` and
the abstract socket of the same name, and exports `DISPLAY`. The first
connection launches Xwayland with both sockets as `-listenfd`, a socketpair
as its Wayland connection (`WAYLAND_SOCKET`), another as `-wm`, and
`-displayfd` to learn when it is up; Xwayland then accepts the pending
connection itself. Sessions without X clients pay nothing, and after a crash
the sockets stay with tilde, so the next connection restarts it. Starting at
login instead would be a small change on top, which is why the choice was
cheap to settle.

Xwayland listens on its sockets only once a window manager owns `WM_S0`, so
tilde's window manager connects over the `-wm` socket as an X client with
`jezek/xgb`, built from a copy in `third_party/xgb` whose extension
initialisation cannot race the connection's reader (#120). X requests block until Xwayland answers, and Xwayland makes
Wayland roundtrips to tilde while answering, so that connection lives in its
own goroutine and reaches compositor state only by posting to the loop.
`xwayland_shell_v1` is visible to the Xwayland client alone and maps the
`WL_SURFACE_SERIAL` of each X window to its `wl_surface` on commit.

With zero scaling (#47), the Xwayland client alone sees each output in
physical pixels with scale 1, placed at its layout position times the
largest output scale, so outputs never overlap in X space whatever their
scales. A window's X geometry is its layout rectangle mapped through the
output it is on, and its surface tree is drawn and hit at that output's
scale, so the X buffer lands pixel for pixel and pointer coordinates reach
Xwayland in X pixels.

`CLIPBOARD` and `PRIMARY` are bridged both ways (#46). When a Wayland client
sets a selection, the window manager takes the X selection with targets
interned from the offered mime types (`text/plain;charset=utf-8` is
`UTF8_STRING`, `text/plain` is `TEXT`, other types keep their name) and
answers X requests by reading the Wayland source. When an X client that has
focus takes a selection, XFixes reports it, the window manager reads its
`TARGETS` and offers the matching mime types as a Wayland source. Transfers
larger than 64 KiB use INCR in both directions, and every pipe has a 30
second deadline. Drag and drop between X and Wayland is not bridged.

## IPC

`tildectl`, bars and scripts talk to tilde over a Unix socket at
`$XDG_RUNTIME_DIR/tilde/<WAYLAND_DISPLAY>.sock`, exported as `TILDE_SOCKET`
(#31). Each connection sends one JSON request line and gets one JSON response
line; a `watch` request turns the connection into an event stream of JSON
lines. Every response carries the protocol version.

The server follows the threading rule: a goroutine per connection, which
posts its query or action to the loop and waits for the answer. Events are
not raised at the places that change state. Before the loop waits, it takes
a snapshot of outputs, workspaces, windows, submap and keyboard layout,
and diffs it against the previous one, so no change goes unreported. Only
dispatches are reported where they happen, because they leave no trace in
the state. With no watcher connected, nothing is snapshotted. A watcher
that falls 256 events behind is disconnected rather than buffered without
bound. [`docs/ipc.md`](ipc.md) has the protocol.

## Portals

tilde ships no portal backend of its own (#57). It sets
`XDG_CURRENT_DESKTOP=tilde`, and the package installs
`share/xdg-desktop-portal/tilde-portals.conf`, which xdg-desktop-portal reads
for that desktop:

| Interface | Backends, in order |
| --- | --- |
| ScreenCast, Screenshot | wayle, then xdg-desktop-portal-wlr |
| everything else | wayle, then xdg-desktop-portal-gtk |

wayle's portal is the shell the session runs anyway. It implements every
portal interface without depending on a compositor: it captures through
ext-image-copy-capture or wlr-screencopy (#53), names windows by their
ext-foreign-toplevel identifier and outputs by connector name, and drives
RemoteDesktop through the virtual keyboard and pointer protocols (#52).
xdg-desktop-portal-wlr speaks wlr-screencopy, so screen sharing still works
without wayle. GlobalShortcuts needs `hyprland-global-shortcuts-v1` in the
compositor (#89).

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
| `server` | Display socket, clients, object maps, globals; sandbox sockets whose clients carry a security context and do not see restricted globals (#6, #118) |
| `securitycontext` | `wp_security_context_manager_v1`: Flatpak and other sandbox engines register a socket for their apps; the compositor restricts screen capture, data control, foreign toplevels, output management, virtual input, session lock, layer shell, workspaces and global shortcuts and gamma control to unsandboxed clients (#118, #123) |
| `wlclient` | The client side of a connection: object map, zombies and dispatch, for the nested backend and tests (#6, #9) |
| `backend` | The backend interface: outputs, frames, input devices (#8) |
| `backend/headless` | Virtual outputs and injected input, for tests (#8) |
| `backend/nested` | Outputs as windows on a parent Wayland compositor (#9) |
| `backend/drm` | Atomic KMS on one DRM device (#40) |
| `render` | The renderer interface (#7) |
| `render/soft` | The CPU renderer, for headless and golden tests (#7) |
| `render/gles` | The GLES2 renderer (#34) |
| `gpu` | purego bindings for EGL, GLES2 and GBM (#33) |
| `surface` | `wl_compositor`, `wl_surface`, `wl_region`, `wl_subcompositor`, `wp_viewporter`, `wp_fractional_scale_v1`, `wp_presentation`: double-buffered state, a queue of content updates that apply in order once their gates open, subsurface trees, roles (#11, #19, #117) |
| `tearingcontrol` | `wp_tearing_control_manager_v1`: the vsync or async presentation hint, which travels with its content update; with `tearing` on, a fullscreen direct scanout of a surface that asks or a rule allows goes out as an async page flip of the primary plane's `FB_ID` alone (#122) |
| `fifo` | `wp_fifo_manager_v1`: a barrier an update sets when it applies and the next latching deadline clears, which a later update can wait for (#117) |
| `committiming` | `wp_commit_timing_manager_v1`: an update waits until the next refresh of an output showing it would not present it before its target time (#117) |
| `gammacontrol` | `zwlr_gamma_control_manager_v1`: one client per output sets its gamma ramps, which the drm backend tests and writes to the CRTC's `GAMMA_LUT`; the identity returns when the client lets go (#123) |
| `singlepixel` | `wp_single_pixel_buffer_manager_v1`: one-pixel buffers of a solid color (#20) |
| `region` | Pixel regions as disjoint rectangles, for damage and input and opaque regions |
| `output` | `wl_output` and `zxdg_output_v1`: each output's mode, logical position and scale in 120ths, with rounding that keeps logical edges adjacent in device pixels (#19) |
| `outputmanagement` | `zwlr_output_manager_v1`: outputs as heads with their modes for display configuration tools, whose configurations override the config file's output rules until a reload (#107) |
| `shm` | `wl_shm` pools and buffers, read fault-safely when a client truncates its pool (#12) |
| `seat` | `wl_seat`, keyboard, pointer and touch, `wp_cursor_shape_v1`, and touchpad gestures through `zwp_pointer_gestures_v1`, which stay with the surface they began on (#13, #14, #108) |
| `data` | `wl_data_device_manager` and `zwp_primary_selection_device_manager_v1`: the clipboard and primary selection offered to the keyboard focus, and drag and drop (#18) |
| `xcursor` | Xcursor files and theme lookup through `XCURSOR_PATH` and `Inherits`, and the built-in arrow (#14) |
| `xkb` | Keymap compilation, sealed keymap fds and modifier state through libxkbcommon (#13) |
| `shell` | xdg-shell and layer-shell glue between clients and `wm`: toplevels, popups placed by their positioner, explicit popup grabs, server-side decorations, activation tokens, and xdg-foreign handles that make another client's toplevel, such as a portal dialog, a child of the exported window, and xdg-dialog's modal hint; focus that would land on a window with a mapped modal dialog goes to the dialog, for X windows with `_NET_WM_STATE_MODAL` too (#15, #16, #17, #20, #48, #110, #111) |
| `wm` | The pure core and the dispatcher (#21, #66) |
| `binds` | Typed chords (modifiers with a keycode from the first layout, a button or a scroll direction), submaps and bind flags, matched to `wm` actions (#29) |
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
