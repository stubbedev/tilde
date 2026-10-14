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
