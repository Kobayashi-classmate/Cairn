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

.PHONY: all build web go go-noui test vet fmt lint check-cjk check package repo release release-keygen smoke repo-test update-test software-test sites-test certs-test databases-test files-test backups-test firewall-test vm-test clean

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
	sed -e 's|$${ARCH}|$(ARCH)|g' -e 's|$${VERSION}|$(VERSION)|g' -e 's|$${DIST}|$(DIST)|g' \
		packaging/nfpm.yaml > $(DIST)/nfpm-$(ARCH).yaml
	for p in deb rpm; do \
		docker run --rm -v $(CURDIR):/src -w /src $(NFPM_IMAGE) \
			package --config $(DIST)/nfpm-$(ARCH).yaml --packager $$p --target $(DIST)/ || exit 1; \
	done

## repo: build the signed apt/dnf repository into repo/
# Needs CAIRN_SIGNING_KEY and CAIRN_REPO_URL; see scripts/build-repo.sh.
repo:
	sh scripts/build-repo.sh

## release-keygen: create the release signing key at KEY, outside this repository
# Prints the public key for internal/cloud/trusted_keys.txt. Never overwrites.
release-keygen:
	@test -n "$(KEY)" || { echo "usage: make release-keygen KEY=/path/outside/the/repo/cairn-release.key" >&2; exit 2; }
	@case "$(abspath $(KEY))" in "$(CURDIR)"/*) echo "KEY must be outside the repository" >&2; exit 2 ;; esac
	mkdir -p $(dir $(abspath $(KEY)))
	docker run --rm -v $(CURDIR):/src -w /src -v $(dir $(abspath $(KEY))):/key \
		-v cairn-gomod:/go/pkg/mod -v cairn-gocache:/root/.cache/go-build \
		-e GOPROXY=$(GOPROXY) -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false \
		$(GO_IMAGE) go run ./tools/release keygen -out /key/$(notdir $(KEY))
	@echo "(/key/ above is $(dir $(abspath $(KEY))) on this machine)"

## release: sign the assets of VERSION into dist/release and dist/feed
# Needs CAIRN_RELEASE_KEY; see scripts/release.sh. CI does this on a tag push.
release:
	VERSION=$(VERSION) sh scripts/release.sh

## smoke: install the packages on every tier-1 distribution (containers)
smoke:
	bash test/smoke/run.sh

## repo-test: build a repository with a throwaway key and run install.sh against it
repo-test:
	bash test/repo/run.sh

## update-test: publish test releases, install with install.sh, update online and roll back
update-test:
	bash test/update/run.sh

## software-test: install catalog software through the API on every tier-1 distribution (containers)
software-test:
	bash test/software/run.sh

## sites-test: create sites through the API with the real nginx and PHP-FPM (Debian/Ubuntu containers)
sites-test:
	bash test/sites/run.sh

## databases-test: manage MySQL/MariaDB, PostgreSQL and Redis through the API (Debian/Ubuntu containers)
databases-test:
	bash test/databases/run.sh

## files-test: file manager and terminal through the API (tier-1 containers)
files-test:
	bash test/files/run.sh

## backups-test: back up and restore through the API into a local S3 server (containers)
backups-test:
	bash test/backups/run.sh

## firewall-test: run the nftables and ufw backends for real and check reachability, firewalld's policy offline (containers)
firewall-test:
	bash test/firewall/run.sh

## certs-test: issue certificates against Pebble (Let's Encrypt's test CA) with a local DNS server (containers)
certs-test:
	bash test/certs/run.sh

## vm-test: the same in a real VM with systemd (QEMU in a container); VM=debian-13|debian-12
vm-test:
	bash test/vm/run.sh $(or $(VM),debian-13)

clean:
	rm -rf $(DIST) internal/ui/dist web/dist repo
