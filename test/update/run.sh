#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# End-to-end test of publishing, installing and updating:
#   1. build packages 0.1.0 and 0.2.0, and a 0.3.0 that cannot be installed
#      (it depends on a package that does not exist),
#   2. sign the three releases with a throwaway key (scripts/release.sh),
#   3. serve a copy of the GitHub release layout from a container on a
#      private Docker network,
#   4. per distribution (update.sh): install 0.1.0 with the published
#      install.sh, update to 0.2.0 through the panel API, then try 0.3.0 and
#      check that 0.2.0 is restored,
#   5. check that the installer refuses a tampered checksum list, a package
#      that does not match it and a release directory of another version.
# The throwaway key is never used for anything else. Packages are built into
# dist/update-test/, which is removed afterwards.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
net=cairn-updatetest
cleanup() {
	docker rm -f cairn-rel >/dev/null 2>&1 || true
	docker network rm $net >/dev/null 2>&1 || true
	rm -rf "$work" dist/update-test dist/release dist/feed
}
trap cleanup EXIT

echo "== signed test releases 0.1.0, 0.2.0 and an uninstallable 0.3.0 (throwaway key)"
bash test/lib/release-fixture.sh "$work" http://cairn-rel
pub=$(awk '{print $1}' "$work/keys.txt")
srv=$work/srv/releases

# Broken mirrors for the installer's refusals.
bad=$work/srv
mkdir -p "$bad/tampered/latest" "$bad/swapped/latest" "$bad/wrongdir/download"
cp -r "$srv/download/v0.1.0" "$bad/tampered/latest/download"
# Flip the first hex digit of the first checksum.
sed -i '1{s/^0/X/; s/^[^X]/0/; s/^X/1/}' "$bad/tampered/latest/download/SHA256SUMS"
cmp -s "$bad/tampered/latest/download/SHA256SUMS" "$srv/download/v0.1.0/SHA256SUMS" && { echo "tampering did not change the file"; exit 1; }
cp -r "$srv/download/v0.1.0" "$bad/swapped/latest/download"
for f in "$bad/swapped/latest/download"/cairn*0.1.0*.{deb,rpm}; do
	n=${f##*/}
	cp "$srv/download/v0.2.0/${n//0.1.0/0.2.0}" "$f"
done
cp -r "$srv/download/v0.1.0" "$bad/wrongdir/download/v0.2.0"

docker network create $net >/dev/null
docker run -d --name cairn-rel --network $net -v "$work/srv:/srv:ro" python:3.12-slim \
	python -m http.server 80 --directory /srv >/dev/null

failed=()
for image in ${UPDATE_TEST_TARGETS:-debian:trixie rockylinux:9}; do
	docker run --rm --network $net -v "$PWD/test/update:/t:ro" -e TRUSTED_KEY="$pub" "$image" bash /t/update.sh ||
		failed+=("$image")
done

echo "== installer refusals"
docker run --rm --network $net -v "$PWD/packaging:/template:ro" debian:trixie bash -euc '
	# Without recommends there is no ca-certificates and so no openssl: the
	# installer must fetch openssl itself before it can check signatures.
	apt-get update -qq >/dev/null && apt-get install -y -qq --no-install-recommends curl >/dev/null 2>&1
	! command -v openssl >/dev/null || { echo "openssl already present"; exit 1; }
	curl -fsSL http://cairn-rel/releases/latest/download/install.sh -o /tmp/install.sh
	try() { # name expected-text args...
		name=$1 want=$2
		shift 2
		if sh /tmp/install.sh --lang en --yes --no-systemd-check "$@" >/tmp/out.log 2>&1; then
			echo "installer accepted: $name"; exit 1
		fi
		grep -q "$want" /tmp/out.log || { cat /tmp/out.log; echo "unexpected failure for: $name"; exit 1; }
		! dpkg -s cairn >/dev/null 2>&1 || { echo "cairn got installed: $name"; exit 1; }
		echo "   refused: $name"
	}
	try "tampered checksum list" "not signed by a trusted key" --mirror http://cairn-rel/tampered
	grep -q "Installing openssl" /tmp/out.log && command -v openssl >/dev/null || { echo "openssl not installed by the installer"; exit 1; }
	try "package not matching the list" "does not match the signed checksum" --mirror http://cairn-rel/swapped
	try "release directory of another version" "no package for this system" --mirror http://cairn-rel/wrongdir --version 0.2.0
	# The template in the repository trusts no key until it is published.
	cp /template/install.sh /tmp/install.sh
	try "unpublished template" "no signing key built in" --mirror http://cairn-rel/releases
' || failed+=("refusals")

if [ ${#failed[@]} -gt 0 ]; then
	echo "Update test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Update test passed."
