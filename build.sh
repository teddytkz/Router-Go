#!/usr/bin/env bash
# Build the dashboard (web/dist) and the single router-go binary. Same steps as `make build`.
# Cross-compile: GOOS=windows GOARCH=amd64 ./build.sh   (output gets .exe on windows)
set -euo pipefail
cd "$(dirname "$0")"

for t in go bun; do command -v "$t" >/dev/null || { echo "error: '$t' not found in PATH" >&2; exit 1; }; done

out=bin/router-go
[ "${GOOS:-$(go env GOOS)}" = windows ] && out=router-go.exe

echo "==> dashboard"
(cd web && bun install --frozen-lockfile && bun run build)

echo "==> binary"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$out" ./cmd/router-go

echo "built ./$out ($(du -h "$out" | cut -f1))"
