#!/usr/bin/env bash
# Rebuild the Linux binary + gateway image, then replace just that container.
# The clients and dev/data volume are left alone.
set -euo pipefail
cd "$(dirname "$0")/.."

arch=$(go env GOARCH)
echo "==> go build linux/$arch"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o bin/useg-linux ./cmd/useg

echo "==> docker compose build gateway"
TARGETARCH="$arch" docker compose build gateway

echo "==> restart gateway"
TARGETARCH="$arch" docker compose up -d --no-deps gateway

docker compose logs --tail=20 gateway
