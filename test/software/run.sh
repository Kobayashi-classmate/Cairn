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

# image|software@line[~origin][#source] list; the last entry is removed
# again. The runtime rows prove the prebuilt builds start on each glibc
# generation and that every approved mirror serves the official files.
# "~origin" installs from that source of the line. "pin:sw@line" checks
# that updates keep to the line. "ext:" rows install
# and remove PHP extensions, "cfg:" rows edit every configuration file of a
# line (see install.sh). "src:sw@line" installs a system line from an
# official mirror chosen for that installation only. "set:a@1,b@2"
# installs the lines the wizard's way: one probe, one job, one package
# manager transaction (the suites "PHP site" and "Node/Java projects").
runs=(
	"debian:trixie|src:nginx@1.26 php@8.4 php@8.3 mysql@8.4 postgresql@18 ext:php@8.4:intl,redis ext:php@8.3:gd cfg:nginx@1.26 cfg:php@8.4 cfg:php@8.3 cfg:mysql@8.4 cfg:postgresql@18 php@8.3"
	"debian:trixie|nodejs@24#npmmirror python@3.14#npmmirror go@1.27#google-cn java@21#tuna dotnet@10 nodejs@24"
	"debian:bookworm|set:nginx@1.22,php@8.2,mariadb@10.11,redis@7.0 set:nodejs@24#tuna,java@21 php@8.5 postgresql@17 ext:php@8.2:redis,zip ext:php@8.5:intl cfg:mariadb@10.11 cfg:redis@7.0 cfg:php@8.5 php@8.5"
	"ubuntu:22.04|php@8.3 nodejs@26#tuna python@3.12 ext:php@8.3:mbstring,imagick cfg:php@8.3 php@8.3"
	"ubuntu:24.04|php@8.2 mysql@8.4 redis@7.0 ext:php@8.2:zip cfg:mysql@8.4 php@8.2"
	"ubuntu:26.04|nginx@1.28 php@8.5 postgresql@16 valkey@9.0 ext:php@8.5:intl,redis cfg:php@8.5 cfg:valkey@9.0 cfg:postgresql@16 valkey@9.0"
	"rockylinux:9|src:nginx@1.26 php@8.2 postgresql@17 mariadb@11.8 nodejs@22 python@3.13 ext:php@8.2:redis,intl cfg:php@8.2 cfg:nginx@1.26 cfg:mariadb@11.8 php@8.2"
	"almalinux:10|php@8.4 valkey@8.1 mysql@8.4 set:nginx@1.26,php@8.3,nodejs@24 ext:php@8.3:redis,zip ext:php@8.4:imagick cfg:php@8.3 cfg:mysql@8.4 cfg:valkey@8.1 php@8.4"
	# The official repositories: nginx.org, MariaDB, Redis (pinned to the
	# installed line), MySQL 9.7, PGDG's newest; "~origin" installs a line
	# the system has from the official repository instead.
	"debian:trixie|nginx@1.30 cfg:nginx@1.30 mariadb@12.3 cfg:mariadb@12.3 redis@8.10 cfg:redis@8.10 pin:redis@8.10 redis@8.10"
	"debian:trixie|mariadb@11.8~mariadb cfg:mariadb@11.8 mariadb@11.8"
	"ubuntu:24.04|nginx@1.31 cfg:nginx@1.31 mysql@9.7 cfg:mysql@9.7 nginx@1.31"
	"rockylinux:9|nginx@1.30 cfg:nginx@1.30 redis@8.12 cfg:redis@8.12 pin:redis@8.12 mariadb@12.3 cfg:mariadb@12.3 mariadb@12.3"
	"almalinux:10|redis@8.10 pin:redis@8.10 mariadb@11.4 cfg:mariadb@11.4 postgresql@19 redis@8.10"
)
targets=${SOFTWARE_TARGETS:-"debian:trixie debian:bookworm ubuntu:22.04 ubuntu:24.04 ubuntu:26.04 rockylinux:9 almalinux:10"}
failed=()
for run in "${runs[@]}"; do
	image=${run%%|*}
	case " $targets " in *" $image "*) ;; *) continue ;; esac
	# SYS_NICE: EL's mysqld carries that file capability, and a binary whose
	# file capabilities exceed the container's bounding set does not start
	# (the configuration check runs it).
	if ! docker run --rm --cap-add SYS_NICE -v "$PWD/dist:/pkgs:ro" -v "$PWD/test/software:/t:ro" -e CASES="${run#*|}" \
		"$image" bash /t/install.sh; then
		failed+=("$image")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Software test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Software test passed on all targets."
