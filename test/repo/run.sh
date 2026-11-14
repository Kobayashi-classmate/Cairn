#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# End-to-end test of the signed repository and the online installer:
#   1. create a throwaway signing key (never used for releases),
#   2. build the repository with scripts/build-repo.sh,
#   3. serve it from a container on a private Docker network,
#   4. run the published install.sh in a container per distribution and check
#      that apt/dnf verified the signatures and the package came up with the
#      chosen language.
# Build the packages first: make go package (and ARCH=arm64 for arm64).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
net=cairn-repotest
trap 'docker rm -f cairn-repo >/dev/null 2>&1 || true; docker network rm $net >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

echo "== throwaway signing key"
docker run --rm -v "$work:/k" debian:trixie sh -euc '
	apt-get update -qq >/dev/null && apt-get install -y -qq gnupg >/dev/null 2>&1
	export GNUPGHOME=$(mktemp -d)
	gpg --batch --passphrase "" --quick-gen-key "Cairn Test Key <test@cairn.invalid>" rsa3072 sign 1d >/dev/null 2>&1
	gpg --batch --armor --export-secret-keys >/k/signing.asc'

OUT="$work/repo" CAIRN_SIGNING_KEY="$work/signing.asc" CAIRN_REPO_URL=http://cairn-repo sh scripts/build-repo.sh

docker network create $net >/dev/null
docker run -d --name cairn-repo --network $net -v "$work/repo:/srv:ro" python:3.12-slim \
	python -m http.server 80 --directory /srv >/dev/null

targets=${REPO_TEST_TARGETS:-"debian:trixie=zh-CN ubuntu:24.04=en rockylinux:9=zh-CN"}
failed=()
for t in $targets; do
	image=${t%%=*}
	lang=${t#*=}
	echo "== $image (--lang $lang)"
	if ! docker run --rm --network $net -e LANG_CHOICE="$lang" "$image" bash -euc '
		if command -v apt-get >/dev/null; then
			apt-get update -qq >/dev/null && apt-get install -y -qq curl ca-certificates gnupg >/dev/null 2>&1
		fi
		curl -fsSL http://cairn-repo/install.sh -o /tmp/install.sh
		sh /tmp/install.sh --lang "$LANG_CHOICE" --no-systemd-check >/tmp/out.log 2>&1 || { tail -30 /tmp/out.log; exit 1; }
		grep -q "/setup?token=" /tmp/out.log || { echo "no setup link"; exit 1; }
		grep -qE "default_locale = .$LANG_CHOICE." /etc/cairn/config.toml || { echo "language not applied"; exit 1; }
		if command -v apt-get >/dev/null; then
			grep -q "Signed-By: /etc/apt/keyrings/cairn.pgp" /etc/apt/sources.list.d/cairn.sources
			dpkg -s cairn | grep -q "Status: install ok installed"
		else
			grep -q "repo_gpgcheck=1" /etc/yum.repos.d/cairn.repo
			# Header signature by the repository key (last 16 hex digits = key ID).
			keyid=$(sed -n "s/^KEY_FINGERPRINT=\"\(.*\)\"$/\1/p" /tmp/install.sh | tail -c 17 | tr "A-F" "a-f")
			rpm -qi cairn | grep -E "^Signature +:" | grep -qi "Key ID $keyid" || { rpm -qi cairn | grep Signature; echo "package not signed by the repository key"; exit 1; }
		fi
		echo "   installed from the signed repository; setup link printed; language $LANG_CHOICE"
	'; then
		failed+=("$image")
	fi
done

# The installer must refuse a key whose fingerprint differs from the one it
# was published with (simulated by editing the expected fingerprint).
echo "== key fingerprint mismatch aborts the installer"
docker run --rm --network $net rockylinux:9 bash -euc '
	curl -fsSL http://cairn-repo/install.sh -o /tmp/install.sh
	sed -i "s/^KEY_FINGERPRINT=\".*\"\$/KEY_FINGERPRINT=\"0000000000000000000000000000000000000000\"/" /tmp/install.sh
	if sh /tmp/install.sh --lang en --no-systemd-check >/tmp/out.log 2>&1; then
		echo "installer accepted a key with the wrong fingerprint"; exit 1
	fi
	grep -q "fingerprint mismatch" /tmp/out.log || { cat /tmp/out.log; exit 1; }
	[ ! -e /etc/yum.repos.d/cairn.repo ] || { echo "repository configured despite the mismatch"; exit 1; }
	echo "   installer aborted before configuring the repository"
' || failed+=("fingerprint-check")

# A repository signed by another key must be rejected.
echo "== tampered signature is rejected (apt)"
docker run --rm --network $net debian:trixie bash -euc '
	apt-get update -qq >/dev/null && apt-get install -y -qq curl gnupg >/dev/null 2>&1
	export GNUPGHOME=$(mktemp -d)
	gpg --batch --passphrase "" --quick-gen-key "Impostor <x@invalid>" rsa3072 sign 1d >/dev/null 2>&1
	install -d /etc/apt/keyrings && gpg --batch --export >/etc/apt/keyrings/cairn.pgp
	printf "Types: deb\nURIs: http://cairn-repo/apt\nSuites: stable\nComponents: main\nSigned-By: /etc/apt/keyrings/cairn.pgp\n" >/etc/apt/sources.list.d/cairn.sources
	if apt-get update >/tmp/u.log 2>&1 && ! grep -qi "NO_PUBKEY\|not signed\|signature" /tmp/u.log; then
		echo "apt accepted the repository with the wrong key"; exit 1
	fi
	echo "   apt refused the repository: signature does not match the configured key"
' || failed+=("tamper-check")

if [ ${#failed[@]} -gt 0 ]; then
	echo "Repository test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Repository test passed."
