#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Publishes three test releases with a throwaway key, in the layout of the
# GitHub releases, for the update and VM tests:
#
#   release-fixture.sh OUT BASE_URL
#
#   OUT/keys.txt              trusted key list (the throwaway public key)
#   OUT/srv/releases/...      download/v0.1.0, v0.2.0, v0.3.0, download/feed
#                             (releases.json listing all three) and
#                             latest/download (a copy of 0.1.0)
#
# 0.3.0 cannot be installed: it depends on a package that does not exist.
# BASE_URL is where OUT/srv will be served (package URLs in releases.json).
# Packages are built into dist/update-test/ (amd64, without the web UI; with
# UI=1 the UI staged by `make web` is embedded); the caller removes it,
# together with dist/release and dist/feed.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
out=$1
base=${2%/}
build=dist/update-test
mkdir -p "$out" "$build"

goal=go-noui
[ "${UI:-0}" = 1 ] && goal=go
for v in 0.1.0 0.2.0 0.3.0; do
	make -s $goal VERSION=$v DIST=$build/$v >/dev/null
done
make -s package VERSION=0.1.0 DIST=$build/0.1.0 >/dev/null
make -s package VERSION=0.2.0 DIST=$build/0.2.0 >/dev/null
sed -e "s|\${ARCH}|amd64|g" -e "s|\${VERSION}|0.3.0|g" -e "s|\${DIST}|$build/0.3.0|g" \
	-e 's|^      - passwd$|      - passwd\n      - cairn-test-missing-dependency|' \
	-e 's|^      - shadow-utils$|      - shadow-utils\n      - cairn-test-missing-dependency|' \
	packaging/nfpm.yaml >$build/0.3.0/nfpm.yaml
[ "$(grep -c cairn-test-missing-dependency $build/0.3.0/nfpm.yaml)" -eq 2 ]
for p in deb rpm; do
	docker run --rm -v "$PWD:/src" -w /src goreleaser/nfpm:v2.49.0 \
		package --config $build/0.3.0/nfpm.yaml --packager $p --target $build/0.3.0/ >/dev/null
done

sh scripts/go.sh sh -euc 'mkdir -p /tmp/k
	go run ./tools/release keygen -out /tmp/k/test.key >/dev/null
	go run ./tools/release pubkey -key /tmp/k/test.key >/tmp/k/keys.txt
	cat /tmp/k/test.key /tmp/k/keys.txt' >"$out/key-and-list"
# Split the PEM private key from the key list line printed after it.
sed -n '/BEGIN PRIVATE KEY/,/END PRIVATE KEY/p' "$out/key-and-list" >"$out/test.key"
sed '/BEGIN PRIVATE KEY/,/END PRIVATE KEY/d' "$out/key-and-list" >"$out/keys.txt"
rm -f "$out/key-and-list"
chmod 0600 "$out/test.key"

srv=$out/srv/releases
mkdir -p "$srv/download/feed"
feed=""
for v in 0.1.0 0.2.0 0.3.0; do
	VERSION=$v PKG_DIR=$build/$v CAIRN_RELEASE_KEY="$out/test.key" CAIRN_TRUSTED_KEYS="$out/keys.txt" \
		ASSETS_URL=$base/releases/download/v$v FEED_IN=$feed sh scripts/release.sh >/dev/null
	mkdir -p "$srv/download/v$v" "$out/feed-$v"
	cp dist/release/* "$srv/download/v$v/"
	cp dist/feed/* "$out/feed-$v/"
	feed=$out/feed-$v/releases.json
done
cp "$out"/feed-0.3.0/* "$srv/download/feed/"
rm -rf "$out"/feed-*
mkdir -p "$srv/latest"
cp -r "$srv/download/v0.1.0" "$srv/latest/download"
