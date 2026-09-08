#!/usr/bin/env bash
# The gates CI runs, run here, before you push.
#
# WHY THIS EXISTS. `go vet` and `go test` are not the gate set. CI also runs
# golangci-lint TWICE over gui/ -- once for the GUI build and once for the
# service build -- and staticcheck lives inside it, which means a green
# `go test ./...` says nothing about whether the push will be green. That is
# not hypothetical: a test asserting `agentKeyID(a) != agentKeyID(a)` passed
# vet, passed the suite, and was refused by staticcheck (SA4000) after it had
# already been merged. The lint was right, and nothing local had asked it.
#
# The server repository has tests/local-gates.sh for exactly this reason. This
# is the client's.
#
#   scripts/local-gates.sh          everything below
#   scripts/local-gates.sh lint     just the two golangci-lint passes
#
# NOT COVERED, because they need something this cannot have: the Windows smoke
# job (real VSS, DPAPI and icacls -- they compile on Linux and demonstrate
# nothing there), the signed MSI builds, the frontend gate (npm), and
# govulncheck (its vulnerability database is deliberately live, so it is a
# statement about today rather than about this commit). Run those in CI.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
ROOT="$PWD"

# Pinned to match .github/workflows/build-and-release.yml exactly. A local
# gate that runs a different version of the tool answers a different question
# than the one that will decide your push.
GOLANGCI_VERSION="v2.12.2"
GO_VERSION="1.26.6"

export GOWORK=off
fail=0
step() { printf '\n=== %s ===\n' "$1"; }
bad()  { printf '\nFAILED: %s\n' "$1"; fail=1; }

command -v go >/dev/null 2>&1 || { echo "go is not installed; this script needs the toolchain"; exit 1; }
have="$(go env GOVERSION)"
[ "$have" = "go${GO_VERSION}" ] || echo "NOTE: go is $have, CI pins go${GO_VERSION} -- results can differ"

# golangci-lint goes in .tools/ rather than onto the PATH, at the pinned
# version, for the same reason CI pins it: a tool release can change output or
# exit codes independently of any finding, and then red means "something
# changed" instead of "something is wrong".
TOOLS="$ROOT/.tools"
LINT="$TOOLS/golangci-lint"
lint_version_ok() { [ -x "$LINT" ] && "$LINT" --version 2>/dev/null | grep -q "${GOLANGCI_VERSION#v}"; }
if ! lint_version_ok; then
    step "installing golangci-lint $GOLANGCI_VERSION into .tools"
    mkdir -p "$TOOLS"
    if ! curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
        | sh -s -- -b "$TOOLS" "$GOLANGCI_VERSION" >/dev/null 2>&1; then
        bad "could not install golangci-lint (network?); the lint passes were NOT run"
        LINT=""
    fi
fi

if [ "${1:-all}" != "lint" ]; then
    step "gofmt"
    unformatted="$(gofmt -l . | grep -v '^vendor/' || true)"
    if [ -n "$unformatted" ]; then echo "$unformatted"; bad "gofmt"; fi

    # The three compile views CI checks. Nothing else checks them all: the
    # default build hides service-tagged code and vice versa.
    step "go vet (linux, windows, windows+service)"
    ( cd gui && CGO_ENABLED=1 GOOS=linux   go vet ./... ) || bad "vet linux"
    ( cd gui && CGO_ENABLED=0 GOOS=windows go vet ./... ) || bad "vet windows"
    ( cd gui && CGO_ENABLED=0 GOOS=windows go vet -tags service ./... ) || bad "vet windows+service"
fi

if [ -n "$LINT" ]; then
    step "golangci-lint (default / GUI build)"
    ( cd gui && "$LINT" run --path-prefix=gui --timeout=5m ) || bad "golangci-lint (default)"

    # `unused` is disabled here and only here, matching CI: the service build
    # excludes GUI-only files, so it would flag every helper the front end owns.
    step "golangci-lint (service build)"
    ( cd gui && "$LINT" run --path-prefix=gui --timeout=5m --build-tags=service --disable=unused ) \
        || bad "golangci-lint (service)"
fi

if [ "${1:-all}" != "lint" ]; then
    step "gosec (controlplane)"
    if command -v gosec >/dev/null 2>&1; then
        ( cd controlplane && gosec -severity high -confidence high ./... ) || bad "gosec"
    else
        echo "SKIP: gosec not installed (go install github.com/securego/gosec/v2/cmd/gosec@latest)"
    fi

    step "tests: controlplane and gui"
    ( cd controlplane && go test -race -count=1 ./... ) || bad "controlplane tests"
    ( cd gui          && go test -race -count=1 ./... ) || bad "gui tests"

    # Keep this list in sync with go.work + pkg/*, as the CI job says.
    step "tests: every other workspace module"
    for m in clientcommon pbscommon snapshot imagebrowse directorybackup machinebackup nbd pkg/retry pkg/security gui/api; do
        printf -- '--- %s\n' "$m"
        ( cd "$m" && go test -race -count=1 ./... ) || bad "module tests: $m"
    done
fi

echo
if [ "$fail" -eq 0 ]; then echo "ALL LOCAL GATES PASSED"; else echo "LOCAL GATES FAILED"; fi
exit $fail
