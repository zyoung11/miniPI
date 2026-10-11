#!/usr/bin/env bash
# Build the pi binary to ./pi.
set -euo pipefail
cd "$(dirname "$0")"

CGO_ENABLED=0 go build -buildvcs=false -trimpath \
	-ldflags "-s -w -X main.Build=$(git rev-parse --short HEAD 2>/dev/null || echo dev)" \
	-o pi ./cmd/pi

echo "built ./pi $(./pi --version)"
