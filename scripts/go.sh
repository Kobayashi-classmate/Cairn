#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs a command in the Go build container with the repository mounted, the
# same way the Makefile does. For quick iterations:
#
#   sh scripts/go.sh go test ./internal/helper/
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
exec docker run --rm -v "$root:/src" -w /src \
	-v cairn-gomod:/go/pkg/mod -v cairn-gocache:/root/.cache/go-build \
	-e GOPROXY="${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}" \
	-e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false "${GO_IMAGE:-golang:1.27.4}" "$@"
