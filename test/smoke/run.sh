#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the package smoke test (smoke.sh) in a throwaway container per target
# distribution. Build the packages first: make go package.
# Containers get no extra privileges and are removed afterwards.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# image=install-language pairs; both languages are covered across the matrix.
targets=${SMOKE_TARGETS:-"debian:bookworm=en debian:trixie=zh-CN ubuntu:22.04=en ubuntu:24.04=zh-CN ubuntu:26.04=en rockylinux:9=zh-CN almalinux:10=en"}
verify_on=${SMOKE_VERIFY_ON:-"debian:trixie rockylinux:9"}

ls dist/cairn_*_amd64.deb dist/cairn-*.x86_64.rpm >/dev/null

failed=()
for t in $targets; do
	image=${t%%=*}
	lang=${t#*=}
	verify=0
	case " $verify_on " in *" $image "*) verify=1 ;; esac
	if ! docker run --rm -v "$PWD/dist:/pkgs:ro" -v "$PWD/test/smoke:/smoke:ro" \
		-e SMOKE_LANG="$lang" -e SMOKE_SYSTEMD_VERIFY="$verify" "$image" bash /smoke/smoke.sh; then
		failed+=("$image")
	fi
done

if [ ${#failed[@]} -gt 0 ]; then
	echo "Smoke test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Smoke test passed on all targets."
