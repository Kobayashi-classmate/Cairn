#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Creates static, PHP and reverse proxy sites through the panel API in a
# throwaway container per Debian/Ubuntu release, with the real nginx and
# PHP-FPM (EL waits for the SELinux verification). Build the packages
# first: make go package.
#
#   bash test/sites/run.sh
#   SITES_TARGETS="debian:trixie" bash test/sites/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ls dist/cairn_*_amd64.deb >/dev/null
targets=${SITES_TARGETS:-"debian:bookworm debian:trixie ubuntu:22.04 ubuntu:24.04 ubuntu:26.04"}
failed=()
for image in $targets; do
	if ! docker run --rm -v "$PWD/dist:/pkgs:ro" -v "$PWD/test/sites:/t:ro" "$image" bash /t/sites.sh; then
		failed+=("$image")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Sites test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Sites test passed on all targets."
