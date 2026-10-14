# tilde

A tiling Wayland compositor in pure Go (`CGO_ENABLED=0`), built to replace a
Hyprland + hy3 setup with something smaller.

## Scope

- **Tiling like hy3**: a tree of horizontal and vertical splits and tab
  groups, autotile by container width, directional focus and move,
  workspaces 1–10 per output, floating windows, window rules, submaps.
- **Drawing**: client surfaces, 1px borders and tab bars. Nothing else.
- **Desktop protocols** for a full session: layer-shell, session lock, idle
  notify and inhibit, data control, screen capture, foreign toplevels,
  workspaces, virtual input, XWayland.
- **Hardware**: DRM/KMS through logind, libinput, NVIDIA (explicit sync) and
  Mesa, fractional scaling, and multi-GPU.

## Non-goals

Animations, blur, shadows, rounded corners, plugins, a scripting language in
the config, VR, HDR.

## Pure Go

No cgo. The few C libraries with no reasonable alternative (EGL, GLES2, GBM,
libinput and libxkbcommon) are loaded at runtime with
[purego](https://github.com/ebitengine/purego); everything else is Go. See
[`docs/architecture.md`](docs/architecture.md).

## Config

One YAML file, `~/.config/tilde/config.yaml`; see
[`docs/config.md`](docs/config.md) for every key and
[`docs/actions.md`](docs/actions.md) for the actions binds can run.
`tildectl` queries and controls a running tilde; see [`docs/ipc.md`](docs/ipc.md).

## Roadmap

The plan lives in the issues, grouped by milestone. Each milestone has exit
criteria in its description.

| Milestone | Outcome |
| --- | --- |
| M0 Foundation | Wire codec, scanner, event loop, server core, headless and nested backends, test harness |
| M1 Core protocols | Ordinary clients run nested on shm buffers |
| M2 Tiling core | hy3-equivalent tiling, workspaces, floating, rules, binds |
| M3 Config and IPC | One validated config file; `tildectl` and an event stream |
| M4 GPU rendering | GLES via purego, dmabuf, explicit sync, damage tracking |
| M5 Native session | KMS, libinput, logind, hotplug, lid, suspend/resume |
| M6 XWayland | Steam and JetBrains IDEs work |
| M7 Desktop parity | wayle, rofi, cliphist, hypridle, screen sharing |
| M8 Daily driver | Packaged for NixOS; Hyprland removed |
| M9 Multi-GPU | Outputs on any GPU, proven in a VM rig |

## Development

```sh
devenv shell   # Go, gopls, golangci-lint, govulncheck, just
just gates     # formatting, vet, lint, test, build, govulncheck
```

`devenv.nix` is the whole development environment: Go pinned to the version
`go.mod` declares, `CGO_ENABLED=0` and `GOTOOLCHAIN=local`. The flake only
builds the package (`nix build`).
