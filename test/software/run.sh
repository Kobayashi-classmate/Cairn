#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Installs catalog software through the panel API in a throwaway container
# per distribution, with the real package managers and the real vendor
# repositories (keys downloaded and checked against the pinned
# fingerprints). Build the packages first: make go package.
#
#   bash test/software/run.sh            # every target
#   SOFTWARE_TARGETS="debian:trixie" bash test/software/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
ls dist/cairn_*_amd64.deb dist/cairn-*.x86_64.rpm >/dev/null

# image|software@line[#source] list; the last entry is removed again. The
# runtime rows prove the prebuilt builds start on each glibc generation and
# that every approved mirror serves the official files.
runs=(
	"debian:trixie|nginx@1.26 php@8.4 php@8.3 mysql@8.4 postgresql@18 php@8.3"
	"debian:trixie|nodejs@24#npmmirror python@3.14#npmmirror go@1.27#google-cn java@21#tuna dotnet@10 nodejs@24"
	"debian:bookworm|php@8.5 postgresql@17 php@8.5"
	"ubuntu:22.04|php@8.3 nodejs@26#tuna python@3.12 php@8.3"
	"ubuntu:24.04|php@8.2 mysql@8.4 redis@7.0 php@8.2"
	"ubuntu:26.04|nginx@1.28 php@8.5 postgresql@16 valkey@9.0 valkey@9.0"
	"rockylinux:9|nginx@1.26 php@8.2 postgresql@17 mariadb@11.8 nodejs@22 python@3.13 php@8.2"
	"almalinux:10|php@8.4 valkey@8.0 mysql@8.4 php@8.4"
)
targets=${SOFTWARE_TARGETS:-"debian:trixie debian:bookworm ubuntu:22.04 ubuntu:24.04 ubuntu:26.04 rockylinux:9 almalinux:10"}
failed=()
for run in "${runs[@]}"; do
	image=${run%%|*}
	case " $targets " in *" $image "*) ;; *) continue ;; esac
	if ! docker run --rm -v "$PWD/dist:/pkgs:ro" -v "$PWD/test/software:/t:ro" -e CASES="${run#*|}" \
		"$image" bash /t/install.sh; then
		failed+=("$image")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Software test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Software test passed on all targets."
