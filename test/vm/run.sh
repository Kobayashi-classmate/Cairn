#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# End-to-end test in a real virtual machine, with systemd as PID 1:
#
#   bash test/vm/run.sh [debian-13]
#   SCRIPT=security.sh bash test/vm/run.sh ubuntu-24.04   (firewall, SSH, host)
#   SCRIPT=sites.sh bash test/vm/run.sh rocky-9           (sites and databases)
#
# inside.sh (the full test, with the online update) needs Debian; the other
# scripts run on every target.
#
# QEMU runs inside a throwaway container (software emulation; no KVM or extra
# privileges needed), boots the distribution's official cloud image and runs
# test/vm/inside.sh in it as root. The test releases (test/lib/
# release-fixture.sh) are served to the guest from the same container. Only
# one VM runs at a time; downloaded images are kept in the Docker volume
# cairn-vm-images (remove it with `docker volume rm cairn-vm-images`).
#
# SCRIPT=accept.sh is the P1 acceptance; it needs the test services
# (Pebble, a DNS server for "test.", an S3 bucket), which run in the QEMU
# container. KEEP=1 instead installs the panel and leaves the VM running
# for a person to try, its ports published on this machine's 127.0.0.1
# only (reach them through an SSH tunnel). KEEP=1 SCRIPT=accept.sh runs the
# acceptance first and keeps its sites and data (signed in as admin with
# the password in accept.sh), for screenshots and UI tests.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
# shellcheck source=test/lib/images.sh
. test/lib/images.sh
target=${1:-debian-13}
script=${SCRIPT:-inside.sh}
keep=${KEEP:-0}
[ "$keep" = 1 ] && [ "$script" != accept.sh ] && script=keep.sh
case "$target" in
	debian-13 | debian-12) ;;
	ubuntu-22.04 | ubuntu-24.04 | ubuntu-26.04 | rocky-9 | alma-10)
		[ "$script" != inside.sh ] || { echo "$target runs SCRIPT=security.sh, sites.sh or accept.sh" >&2; exit 2; } ;;
	*) echo "unsupported VM target: $target" >&2; exit 2 ;;
esac

work=$(mktemp -d)
cleanup() {
	if [ "$keep" != 1 ]; then
		docker rm -f cairn-vm >/dev/null 2>&1 || true
		rm -rf "$work"
	fi
	rm -rf dist/update-test dist/release dist/feed
}
trap cleanup EXIT

ui=0
if [ "$keep" = 1 ]; then
	# A person uses this one: build the web UI and embed it.
	echo "== web UI"
	make -s web >/dev/null
	ui=1
fi
echo "== signed test releases (throwaway key)"
# QEMU user networking shows the container to the guest as 10.0.2.2.
UI=$ui bash test/lib/release-fixture.sh "$work/fixture" http://10.0.2.2:8080

services=0
case "$script" in accept.sh | keep.sh) services=1 ;; esac
if [ "$services" = 1 ]; then
	# Pebble and rclone come out of their images as they are.
	mkdir -p "$work/tools"
	cid=$(docker create "$PEBBLE_IMAGE")
	docker cp "$cid:/app" "$work/tools/pebble" >/dev/null
	docker cp "$cid:/test" "$work/tools/pebble-test" >/dev/null
	docker rm "$cid" >/dev/null
	cid=$(docker create "$RCLONE_IMAGE")
	docker cp "$cid:/usr/local/bin/rclone" "$work/tools/rclone" >/dev/null
	docker rm "$cid" >/dev/null
fi

echo "== $target VM"
run=(--rm)
if [ "$keep" = 1 ]; then
	docker rm -f cairn-vm >/dev/null 2>&1 || true
	run=(-d -p 127.0.0.1:28443:28443 -p 127.0.0.1:28080:28080 -p 127.0.0.1:28444:28444)
fi
docker run "${run[@]}" --name cairn-vm \
	-v "$PWD/test/vm:/t:ro" -v "$PWD/test/files:/f:ro" -v "$work:/work" -v cairn-vm-images:/images \
	-e TARGET="$target" -e SCRIPT="$script" -e SERVICES="$services" -e KEEP="$keep" \
	-e TRUSTED_KEY="$(awk '{print $1}' "$work/fixture/keys.txt")" \
	debian:trixie bash /t/host.sh
if [ "$keep" = 1 ]; then
	# Wait for the guest to be ready, then show how to reach it.
	for _ in $(seq 360); do
		docker logs cairn-vm 2>&1 | grep -q '^== READY' && break
		if ! docker ps -q --filter name=cairn-vm | grep -q .; then
			docker logs cairn-vm 2>&1 | tail -30
			exit 1
		fi
		sleep 5
	done
	docker logs cairn-vm 2>&1 | sed -n '/^== READY/,$p'
	echo
	echo "Stop it with: docker rm -f cairn-vm && rm -rf $work"
fi
