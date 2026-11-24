#!/bin/sh
# Run with installed Go and the pinned Lean toolchain. Installs nothing.
set -eu
cd -- "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
export GOTOOLCHAIN=local
export GOWORK=off
takt_go=${TAKT_GO:-go}
takt_gofmt="$("$takt_go" env GOROOT)/bin/gofmt"
unformatted=$("$takt_gofmt" -l .)
if [ -n "$unformatted" ]; then
    printf '%s\n' "$unformatted" >&2
    exit 1
fi
"$takt_go" mod tidy -diff
"$takt_go" vet -mod=readonly ./...
"$takt_go" test -mod=readonly -race -cover -shuffle=on -count=1 -timeout=120s ./...
for takt_arch in amd64 arm64 riscv64 loong64; do
    GOOS=linux GOARCH="$takt_arch" "$takt_go" build -mod=readonly ./...
done
sh proofs/run.sh
