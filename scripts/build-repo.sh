#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Builds the signed package repository from dist/*.deb and dist/*.rpm (run
# `make go package` for each architecture first):
#
#   repo/apt/                     Debian/Ubuntu repository: dists/stable with a
#                                 signed InRelease and Release.gpg
#   repo/rpm/<arch>/              dnf repository per architecture: signed
#                                 packages and a signed repomd.xml
#   repo/cairn-archive-keyring.*  public key (.pgp binary for apt, .asc for dnf)
#   repo/install.sh(.asc)         online installer with the URL and key
#                                 fingerprint filled in, plus its signature
#
# Environment:
#   CAIRN_SIGNING_KEY  path to the ASCII-armored private signing key, without a
#                      passphrase. Keep it out of this repository; in CI, pass
#                      it as a secret file.
#   CAIRN_REPO_URL     public base URL the repository will be served from.
#   OUT                output directory (default: repo).
#
# Everything runs in throwaway containers; nothing is installed on the host.
set -eu

cd "$(git rev-parse --show-toplevel)"
: "${CAIRN_SIGNING_KEY:?set CAIRN_SIGNING_KEY to the private signing key file}"
: "${CAIRN_REPO_URL:?set CAIRN_REPO_URL to the public base URL}"
OUT=${OUT:-repo}

key_dir=$(cd "$(dirname "$CAIRN_SIGNING_KEY")" && pwd)
key_file=$(basename "$CAIRN_SIGNING_KEY")
ls dist/*.deb dist/*.rpm >/dev/null

rm -rf "$OUT"
mkdir -p "$OUT"
out_abs=$(cd "$OUT" && pwd)

echo "== apt repository and installer"
docker run --rm -v "$PWD/dist:/dist:ro" -v "$PWD/packaging:/packaging:ro" -v "$out_abs:/out" \
	-v "$key_dir:/key:ro" -e KEY="/key/$key_file" -e REPO_URL="$CAIRN_REPO_URL" debian:trixie sh -euc '
	apt-get update -qq >/dev/null
	apt-get install -y -qq apt-utils gnupg >/dev/null 2>&1
	export GNUPGHOME=$(mktemp -d)
	gpg --batch --quiet --import "$KEY"
	fpr=$(gpg --batch --with-colons --list-secret-keys | awk -F: "/^fpr:/ {print \$10; exit}")
	sign() { gpg --batch --yes --local-user "$fpr" "$@"; }

	mkdir -p /out/apt/pool/main
	cp /dist/*.deb /out/apt/pool/main/
	cd /out/apt
	for arch in amd64 arm64; do
		d=dists/stable/main/binary-$arch
		mkdir -p "$d"
		apt-ftparchive --arch "$arch" packages pool >"$d/Packages"
		gzip -9kf "$d/Packages"
	done
	apt-ftparchive \
		-o APT::FTPArchive::Release::Origin=Cairn -o APT::FTPArchive::Release::Label=Cairn \
		-o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
		-o "APT::FTPArchive::Release::Architectures=amd64 arm64" -o APT::FTPArchive::Release::Components=main \
		release dists/stable >/tmp/Release
	mv /tmp/Release dists/stable/Release
	sign --clearsign -o dists/stable/InRelease dists/stable/Release
	sign --detach-sign --armor -o dists/stable/Release.gpg dists/stable/Release

	gpg --batch --export "$fpr" >/out/cairn-archive-keyring.pgp
	gpg --batch --armor --export "$fpr" >/out/cairn-archive-keyring.asc
	# Fill in only the two assignment lines: the placeholders also appear in the
	# checks that detect an unpublished script, and those must stay intact.
	sed -e "s|^REPO_URL_DEFAULT=\"@REPO_URL@\"\$|REPO_URL_DEFAULT=\"$REPO_URL\"|" \
		-e "s|^KEY_FINGERPRINT=\"@KEY_FINGERPRINT@\"\$|KEY_FINGERPRINT=\"$fpr\"|" /packaging/install.sh >/out/install.sh
	grep -q "^REPO_URL_DEFAULT=\"$REPO_URL\"\$" /out/install.sh && grep -q "^KEY_FINGERPRINT=\"$fpr\"\$" /out/install.sh
	[ "$(grep -c "@REPO_URL@" /out/install.sh)" -eq 1 ] && [ "$(grep -c "@KEY_FINGERPRINT@" /out/install.sh)" -eq 1 ]
	sign --detach-sign --armor -o /out/install.sh.asc /out/install.sh
	echo "$fpr" >/out/KEY_FINGERPRINT
'

echo "== rpm repositories"
docker run --rm -v "$PWD/dist:/dist:ro" -v "$out_abs:/out" -v "$key_dir:/key:ro" -e KEY="/key/$key_file" \
	rockylinux:9 sh -euc '
	dnf install -y -q createrepo_c rpm-sign gnupg2 >/dev/null 2>&1
	export GNUPGHOME=$(mktemp -d)
	gpg --batch --quiet --import "$KEY"
	fpr=$(cat /out/KEY_FINGERPRINT)
	for arch in x86_64 aarch64; do
		ls /dist/*."$arch".rpm >/dev/null 2>&1 || continue
		d=/out/rpm/$arch
		mkdir -p "$d"
		cp /dist/*."$arch".rpm "$d/"
		rpmsign --define "_gpg_name $fpr" --define "_gpg_path $GNUPGHOME" \
			--define "_gpg_sign_cmd_extra_args --batch --pinentry-mode loopback" --addsign "$d"/*.rpm >/dev/null
		createrepo_c --quiet "$d"
		gpg --batch --yes --local-user "$fpr" --detach-sign --armor "$d/repodata/repomd.xml"
	done
'

echo "Repository written to $OUT/ (key $(cat "$OUT/KEY_FINGERPRINT"))"
