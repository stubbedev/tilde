# Third-party material

## Protocol XML

`internal/protocol/xml/` vendors protocol descriptions at the versions
pinned in `internal/protocol/protocols.json`. Each file carries its own
copyright notice and licence, which are kept verbatim; the generated
bindings under `internal/protocol/server/` and `internal/protocol/client/`
derive from them.

| Source | Licence |
| --- | --- |
| [wayland](https://gitlab.freedesktop.org/wayland/wayland) | MIT |
| [wayland-protocols](https://gitlab.freedesktop.org/wayland/wayland-protocols) | MIT |
| [wlr-protocols](https://gitlab.freedesktop.org/wlroots/wlr-protocols) | MIT-style, per file |
| [wlroots](https://gitlab.freedesktop.org/wlroots/wlroots) (`virtual-keyboard-unstable-v1`) | MIT |

## Ported code

Code ported from Hyprland, aquamarine, hyprwayland-scanner or libwayland is
listed here with its original copyright notice and licence.

| tilde | Ported from | Licence |
| --- | --- | --- |
| `internal/shell/positioner.go` (`Positioner.Place`: flip when it shows more of the popup, slide, resize) | Hyprland [`CXDGPositionerRules::getPosition`](https://github.com/hyprwm/Hyprland/blob/1bd31aaa579a40680ce876e1547e371b60c45522/src/protocols/XDGShell.cpp) | BSD-3-Clause, below |

### Hyprland

```
BSD 3-Clause License

Copyright (c) 2022-2026, vaxerski
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```
