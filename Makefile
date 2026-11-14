# SPDX-License-Identifier: AGPL-3.0-only
#
# Every build step runs inside a throwaway container, so nothing has to be
# installed on the host besides Docker.

GO_IMAGE   ?= golang:1.27.3
NODE_IMAGE ?= node:24-trixie-slim
NFPM_IMAGE ?= goreleaser/nfpm:v2.49.0
LINT_IMAGE ?= golangci/golangci-lint:v2.15.0

# goproxy.cn first: proxy.golang.org downloads are unreachable on some networks.
GOPROXY ?= https://goproxy.cn,https://proxy.golang.org,direct
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo 0.0.0-dev)
ARCH    ?= amd64
GO_TAGS ?= embedui
DIST    := dist

MODULE  := github.com/Kobayashi-classmate/Cairn
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION)
BINARIES := cairn-web cairn-helper cairn

GO_RUN = docker run --rm \
	-v $(CURDIR):/src -w /src \
	-v cairn-gomod:/go/pkg/mod -v cairn-gocache:/root/.cache/go-build \
	-e GOPROXY=$(GOPROXY) -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
	$(GO_IMAGE)

NODE_RUN = docker run --rm \
	-v $(CURDIR)/web:/web -w /web \
	-v cairn-npm-cache:/root/.npm \
	$(NODE_IMAGE)

.PHONY: all build web go go-noui test vet fmt lint check-cjk check package repo smoke repo-test clean

all: check build

build: web go

## web: build the frontend and stage it for embedding into cairn-web
web:
	$(NODE_RUN) sh -c 'npm ci && npm run check:locales && npm run build'
	rm -rf internal/ui/dist && cp -r web/dist internal/ui/dist

## go: build all binaries (embeds the UI; run `make web` first)
go:
	$(GO_RUN) sh -c 'set -e; for b in $(BINARIES); do \
		GOOS=linux GOARCH=$(ARCH) go build -trimpath -tags "$(GO_TAGS)" \
		-ldflags "$(LDFLAGS)" -o $(DIST)/$(ARCH)/$$b ./cmd/$$b; done'

## go-noui: build without the embedded UI (serves a placeholder page)
go-noui:
	$(MAKE) go GO_TAGS=

test:
	$(GO_RUN) go test ./...

vet:
	$(GO_RUN) go vet ./...

fmt:
	$(GO_RUN) gofmt -l -w cmd internal

lint:
	docker run --rm -v $(CURDIR):/src -w /src \
		-v cairn-gomod:/go/pkg/mod -v cairn-golangci:/root/.cache \
		-e GOPROXY=$(GOPROXY) $(LINT_IMAGE) golangci-lint run ./...

check-cjk:
	sh scripts/check-cjk.sh

check: vet test check-cjk

## package: build .deb and .rpm packages for $(ARCH) into dist/
# nfpm does not expand variables in file sources, so render the config first.
package:
	mkdir -p $(DIST)
	sed -e 's|$${ARCH}|$(ARCH)|g' -e 's|$${VERSION}|$(VERSION)|g' packaging/nfpm.yaml > $(DIST)/nfpm-$(ARCH).yaml
	for p in deb rpm; do \
		docker run --rm -v $(CURDIR):/src -w /src $(NFPM_IMAGE) \
			package --config $(DIST)/nfpm-$(ARCH).yaml --packager $$p --target $(DIST)/ || exit 1; \
	done

## repo: build the signed apt/dnf repository into repo/
# Needs CAIRN_SIGNING_KEY and CAIRN_REPO_URL; see scripts/build-repo.sh.
repo:
	sh scripts/build-repo.sh

## smoke: install the packages on every tier-1 distribution (containers)
smoke:
	bash test/smoke/run.sh

## repo-test: build a repository with a throwaway key and run install.sh against it
repo-test:
	bash test/repo/run.sh

clean:
	rm -rf $(DIST) internal/ui/dist web/dist repo
