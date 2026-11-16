#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Uses the file manager and the terminal through the panel API in a
# throwaway container per tier-1 distribution. Build the packages first:
# make go package.
#
#   bash test/files/run.sh
#   FILES_TARGETS="debian:trixie" bash test/files/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ls dist/cairn_*_amd64.deb dist/cairn-*.x86_64.rpm >/dev/null
targets=${FILES_TARGETS:-"debian:bookworm debian:trixie ubuntu:22.04 ubuntu:24.04 ubuntu:26.04 rockylinux:9 almalinux:10"}
failed=()
for image in $targets; do
	if ! docker run --rm -v "$PWD/dist:/pkgs:ro" -v "$PWD/test/files:/t:ro" "$image" bash /t/files.sh; then
		failed+=("$image")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Files test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Files test passed on all targets."
