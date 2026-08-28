#!/usr/bin/env bash
# Build the linux/arm64 image for the target host.
#
# The binary is compiled here rather than inside the image: the build daemon has
# no outbound network, and the Go toolchain on this host already produces a
# static binary for the target. See the Dockerfile for the full reasoning.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${DOCKER_HOST:=unix://$HOME/.colima/default/docker.sock}"
export DOCKER_HOST GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=arm64

TAG="${1:-$(git rev-parse --short HEAD 2>/dev/null || date +%Y%m%d)-$(date +%H%M%S)}"

echo "==> compiling dist/obagent (linux/arm64)"
mkdir -p dist
go build -trimpath -ldflags="-s -w" -o dist/obagent ./cmd/obagent
file dist/obagent | sed 's/^/    /'

echo "==> building image opportunity-bridge:${TAG}"
# --provenance/--sbom off: buildx otherwise emits a manifest LIST with an
# attestation entry, which `ctr images import` on the node handles poorly. A
# single-platform image is what the node actually wants.
docker build --platform linux/arm64 --provenance=false --sbom=false \
  -t "opportunity-bridge:${TAG}" -t opportunity-bridge:latest .

echo "==> exporting dist/opportunity-bridge-${TAG}.tar.gz"
docker save "opportunity-bridge:${TAG}" | gzip -9 > "dist/opportunity-bridge-${TAG}.tar.gz"
ls -lh "dist/opportunity-bridge-${TAG}.tar.gz" | sed 's/^/    /'
echo "${TAG}" > dist/TAG
