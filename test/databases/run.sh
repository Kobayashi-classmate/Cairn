#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Manages databases through the panel API in a throwaway container per
# Debian/Ubuntu release, with the real MariaDB or MySQL, PostgreSQL and
# Redis or Valkey (EL comes later). Build the packages first:
# make go package.
#
#   bash test/databases/run.sh
#   DB_TARGETS="debian:trixie" bash test/databases/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ls dist/cairn_*_amd64.deb >/dev/null
# image[,mysql software[,redis software]]
targets=${DB_TARGETS:-"debian:bookworm debian:trixie,mariadb,valkey ubuntu:22.04,mysql ubuntu:24.04 ubuntu:26.04"}
failed=()
for target in $targets; do
	IFS=, read -r image my rd <<<"$target"
	if ! docker run --rm -e DB_MYSQL="${my:-mariadb}" -e DB_REDIS="${rd:-redis}" \
		-v "$PWD/dist:/pkgs:ro" -v "$PWD/test/databases:/t:ro" "$image" bash /t/db.sh; then
		failed+=("$target")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Databases test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Databases test passed on all targets."
