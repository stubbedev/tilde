# tilde dev tasks.

# List all recipes with their descriptions.
default:
    @just --list

# Every gate a commit must pass: formatting, a tidy go.mod, the release
# gates, the vulnerability scan, the headless harness and the race detector.
# `devenv test` and CI run this.
gates: fmt-check tidy-check check vuln headless race

# The packages whose tests need dev-shell programs: the compositor-in-the-loop
# harness, which boots tilde on the headless backend and drives real clients
# against it, the session bus and GPU tests, and the end-to-end session in
# cmd/tilde.
headless_packages := "./internal/harness/... ./internal/backend/nested/... ./internal/systemd/... ./internal/session/... ./internal/drm/... ./internal/syncobj/... ./internal/gpu/... ./internal/render/gles/... ./internal/xwayland/... ./cmd/tilde/..."

# Run the tests that need dev-shell programs. TILDE_HEADLESS=1 turns a missing
# program into a failure instead of a skip.
headless:
    TILDE_HEADLESS=1 go test -count=1 {{headless_packages}}

# The tests that need a real GPU, and the GLES harness tests, which the dev
# shell otherwise runs on Mesa's llvmpipe.
hardware_tests := "OnHardware|WithoutMaster|TestAStackOnARenderNode|TestDMABUFClientsOnTheGLESRenderer|TestImageCopyCaptureIntoDMABUFs|TestFullscreenDMABUFsScanOutDirectly|TestDestroyingAReplacedBufferStillOnScreenSendsNoRelease|TestAFullscreenSurfaceIsOfferedThePlaneFormats"

# Run the GPU tests on the host's own EGL vendors instead of the dev shell's
# Mesa, then check frame pacing: headless GLES tilde in a private runtime dir,
# with Xwayland off, runs es2gears_wayland and fails below min_fps.
hardware min_fps="57":
    #!/usr/bin/env bash
    set -euo pipefail
    unset __EGL_VENDOR_LIBRARY_DIRS LIBGL_ALWAYS_SOFTWARE
    TILDE_HEADLESS=1 go test -count=1 -v -run '{{hardware_tests}}' ./internal/render/gles/ ./internal/backend/drm/ ./internal/harness/ | grep -E '^(--- |ok|FAIL)|_test.go'
    dir=$(mktemp -d)
    trap 'rm -rf "$dir"' EXIT
    mkdir -m 0700 "$dir/run"
    go build -o "$dir/tilde" ./cmd/tilde
    printf 'xwayland:\n  enabled: false\n' > "$dir/config.yaml"
    XDG_RUNTIME_DIR="$dir/run" WAYLAND_DISPLAY= "$dir/tilde" -backend headless -renderer gles -config "$dir/config.yaml" 2> "$dir/tilde.log" &
    tilde=$!
    trap 'kill "$tilde" 2>/dev/null || true; wait "$tilde" 2>/dev/null || true; rm -rf "$dir"' EXIT
    for _ in $(seq 50); do
        socket=$(find "$dir/run" -maxdepth 1 -name 'wayland-*' ! -name '*.lock' -printf '%f\n' | head -1)
        [[ -n "$socket" ]] && break
        sleep 0.1
    done
    if [[ -z "$socket" ]]; then
        echo "hardware: tilde did not start" >&2
        cat "$dir/tilde.log" >&2
        exit 1
    fi
    XDG_RUNTIME_DIR="$dir/run" WAYLAND_DISPLAY="$socket" timeout 12 stdbuf -oL es2gears_wayland > "$dir/gears.log" 2>&1 || true
    cat "$dir/gears.log"
    fps=$(grep -oE '= [0-9.]+ FPS' "$dir/gears.log" | tail -1 | grep -oE '[0-9.]+' || true)
    if [[ -z "$fps" ]]; then
        echo "hardware: es2gears_wayland reported no frame rate" >&2
        exit 1
    fi
    echo "pacing: $fps fps on headless GLES tilde at 60 Hz"
    awk -v fps="$fps" -v min="{{min_fps}}" 'BEGIN { exit !(fps >= min) }' || { echo "hardware: $fps fps is below {{min_fps}}" >&2; exit 1; }

# Fuzz the compositor with raw clients that bind every global and send
# requests with valid headers and arbitrary arguments. The gates run only the
# seed corpus; this explores for as long as asked.
fuzz duration="5m":
    TILDE_HEADLESS=1 go test -run='^$' -fuzz=FuzzRequests -fuzztime={{duration}} ./internal/harness/

# Run every test, headless ones included, and the patched xgb fork's under
# the race detector, which needs cgo; the binaries tilde ships keep
# CGO_ENABLED=0.
race:
    CGO_ENABLED=1 go test -race -count=1 ./...
    CGO_ENABLED=1 TILDE_HEADLESS=1 go test -race -count=1 {{headless_packages}}
    cd third_party/xgb && CGO_ENABLED=1 go test -race -count=1 ./...

# Run every release gate in order: vet, lint, test, build.
check: vet lint test build

# Static analysis of every package with go vet.
vet:
    go vet ./...

# Lint every package; settings live in .golangci.yml.
lint:
    golangci-lint run

# Run the test suite for every package.
test:
    go test ./...

# Compile-check every package; the output is discarded.
build:
    go build -o /dev/null ./...

# Regenerate the protocol bindings from the vendored XML.
generate:
    go generate ./internal/scanner

# Download the protocol XML pinned in internal/protocol/protocols.json, then
# regenerate the bindings.
protocols-fetch:
    go run ./internal/scanner -dir internal/protocol -fetch
    go generate ./internal/scanner

# Format every Go source in place with the formatters in .golangci.yml.
fmt:
    golangci-lint fmt ./...

# The formatting gate: fail, showing the diff, when any source is not
# formatted.
fmt-check:
    golangci-lint fmt --diff ./...

# Fail, showing the diff, when go.mod or go.sum is not tidy.
tidy-check:
    go mod tidy -diff

# Fail when a reachable dependency has a known vulnerability.
vuln:
    govulncheck ./...

# Move an issue to In Progress on the tilde project board.
start issue:
    #!/usr/bin/env bash
    set -euo pipefail
    issue={{quote(issue)}}
    if [[ ! "$issue" =~ ^[0-9]+$ ]]; then
        echo "start: issue must be a number, got '$issue'" >&2
        exit 1
    fi
    item=$(gh api graphql -F number="$issue" \
        -f query='query($number: Int!) { repository(owner: "stubbedev", name: "tilde") { issue(number: $number) { projectItems(first: 20) { nodes { id project { number } } } } } }' \
        --jq '.data.repository.issue.projectItems.nodes[] | select(.project.number == 3) | .id')
    if [[ -z "$item" ]]; then
        echo "start: issue #$issue is not on the tilde board; add it with: gh project item-add 3 --owner stubbedev --url https://github.com/stubbedev/tilde/issues/$issue" >&2
        exit 1
    fi
    gh project item-edit --project-id PVT_kwHOBU6Wkc4Bm521 --id "$item" \
        --field-id PVTSSF_lAHOBU6Wkc4Bm521zhlg6Ew --single-select-option-id 47fc9ee4 >/dev/null
    echo "#$issue is In Progress"
