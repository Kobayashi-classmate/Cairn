#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Assembles the signed files of one release (build the packages first:
# `make web`, then `make go package` for each ARCH):
#
#   dist/release/   packages, install.sh (trusted keys filled in), SHA256SUMS
#                   and SHA256SUMS.sig: the assets of the GitHub release vX.Y.Z
#   dist/feed/      releases.json(.sig): the index the panels read, with this
#                   release added; replaces the asset of the "feed" release
#
# Environment:
#   VERSION             release version, without "v"
#   CAIRN_RELEASE_KEY   path to the release signing key (make release-keygen);
#                       keep it outside this repository
#   FEED_IN             current releases.json (its .sig next to it); omit for
#                       the very first release
#   NOTES_EN, NOTES_ZH  files with the release notes (optional)
#   SECURITY            1 for a release that fixes a security problem
#   PKG_DIR             where the packages are (default: dist)
#   ASSETS_URL          where the packages will be downloaded from (default:
#                       the GitHub release vVERSION)
#   CAIRN_TRUSTED_KEYS  key list replacing internal/cloud/trusted_keys.txt
#                       (tests and self-hosted mirrors only)
#
# Everything runs in a throwaway container; nothing is installed on the host.
set -eu

cd "$(git rev-parse --show-toplevel)"
: "${VERSION:?set VERSION, for example 0.2.0}"
: "${CAIRN_RELEASE_KEY:?set CAIRN_RELEASE_KEY to the release signing key file}"
GO_IMAGE=${GO_IMAGE:-golang:1.27.4}
GOPROXY=${GOPROXY:-https://goproxy.cn,https://proxy.golang.org,direct}
ASSETS_URL=${ASSETS_URL:-https://github.com/Kobayashi-classmate/Cairn/releases/download/v$VERSION}

rel=dist/release
feed=dist/feed
in=dist/release-in
rm -rf "$rel" "$feed" "$in"
mkdir -p "$rel" "$feed" "$in"
pkgs=${PKG_DIR:-dist}
# The packages spell a pre-release with "~" (0.2.0~beta.1), as the Makefile
# builds them, so that apt and rpm sort it before 0.2.0.
pkg_version=$(printf '%s' "$VERSION" | sed -E 's/-(alpha|beta|rc)\./~\1./')
# Package release "1" (packaging/nfpm.yaml); exact names, so that 0.2.0 does
# not also pick up 0.2.0~beta.1.
cp "$pkgs"/cairn_"$pkg_version"-1_*.deb "$pkgs"/cairn-"$pkg_version"-1.*.rpm "$rel"/
# GitHub renames release assets with special characters such as "~", which
# would break the names in SHA256SUMS and the feed: the files are uploaded
# as 0.2.0-beta.1 (the version inside the packages keeps its "~").
for f in "$rel"/*~*; do
	[ -e "$f" ] || continue
	mv "$f" "$(printf '%s' "$f" | tr '~' -)"
done

# A stable release's installer refuses to install anything older without
# --version (a mirror could otherwise serve an older signed release as the
# newest). A pre-release's installer does not: "latest" is then the stable
# release before it, and sort -V orders pre-releases after their release.
minver=""
if printf '%s\n' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	minver="-version $VERSION"
fi

# Inputs are copied next to the outputs so that one bind mount is enough.
keys=""
if [ -n "${CAIRN_TRUSTED_KEYS:-}" ]; then
	cp "$CAIRN_TRUSTED_KEYS" "$in/trusted_keys.txt"
	keys="-keys $in/trusted_keys.txt"
fi
opts=""
if [ -n "${FEED_IN:-}" ]; then
	cp "$FEED_IN" "$in/releases.json"
	cp "$FEED_IN.sig" "$in/releases.json.sig"
	opts="$opts -in $in/releases.json"
fi
if [ -n "${NOTES_EN:-}" ]; then cp "$NOTES_EN" "$in/notes.en"; opts="$opts -notes en=$in/notes.en"; fi
if [ -n "${NOTES_ZH:-}" ]; then cp "$NOTES_ZH" "$in/notes.zh-CN"; opts="$opts -notes zh-CN=$in/notes.zh-CN"; fi
if [ "${SECURITY:-0}" = 1 ]; then opts="$opts -security"; fi

key_dir=$(cd "$(dirname "$CAIRN_RELEASE_KEY")" && pwd)
key=/key/$(basename "$CAIRN_RELEASE_KEY")

docker run --rm -v "$PWD:/src" -w /src -v "$key_dir:/key:ro" \
	-v cairn-gomod:/go/pkg/mod -v cairn-gocache:/root/.cache/go-build \
	-e GOPROXY="$GOPROXY" -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false "$GO_IMAGE" sh -euc "
	go build -o /tmp/release ./tools/release
	r=/tmp/release
	\$r installer $keys $minver -in packaging/install.sh -out $rel/install.sh
	\$r sums -out $rel/SHA256SUMS $rel/*.deb $rel/*.rpm $rel/install.sh
	\$r sign -key $key $rel/SHA256SUMS
	\$r releases $keys $opts -version $VERSION -assets-url $ASSETS_URL -out $feed/releases.json $rel/*.deb $rel/*.rpm
	\$r sign -key $key $feed/releases.json
	\$r verify $keys $rel/SHA256SUMS $feed/releases.json
"
rm -rf "$in"
echo "Release $VERSION: assets in $rel/, feed index in $feed/"
