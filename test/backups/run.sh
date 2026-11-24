#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Backs up a site, a directory and (on Debian/Ubuntu) a MariaDB database
# through the panel API into an S3-compatible bucket served by
# "rclone serve s3" in a throwaway Docker network, encrypted with a fresh
# age key pair, and restores them. Build the packages first:
# make go package.
#
#   bash test/backups/run.sh
#   BACKUP_TARGETS="debian:trixie" bash test/backups/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
# shellcheck source=test/lib/images.sh
. test/lib/images.sh
ls dist/cairn_*_amd64.deb dist/cairn-*.x86_64.rpm >/dev/null
targets=${BACKUP_TARGETS:-"debian:trixie ubuntu:22.04 rockylinux:9"}
mapfile -t key < <(sh scripts/go.sh go run ./test/backups/agekey)
net=cairn-backups-$$
s3=cairn-s3-$$
cleanup() {
	docker rm -f "$s3" >/dev/null 2>&1 || true
	docker network rm "$net" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker network create "$net" >/dev/null
docker run -d --rm --name "$s3" --network "$net" --network-alias s3 --entrypoint sh "$RCLONE_IMAGE" \
	-c 'mkdir -p /data/cairn-backups && exec rclone serve s3 --auth-key TESTKEY,TESTSECRET --addr :8080 /data' >/dev/null
failed=()
for image in $targets; do
	if ! docker run --rm --network "$net" -e AGE_RECIPIENT="${key[0]}" -e AGE_IDENTITY="${key[1]}" \
		-v "$PWD/dist:/pkgs:ro" -v "$PWD/test/backups:/t:ro" "$image" bash /t/backups.sh; then
		failed+=("$image")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Backups test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Backups test passed on all targets."
