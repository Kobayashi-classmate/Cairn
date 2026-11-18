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
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
target=${1:-debian-13}
script=${SCRIPT:-inside.sh}
case "$target" in
	debian-13 | debian-12) ;;
	ubuntu-24.04 | rocky-9 | alma-10) [ "$script" != inside.sh ] || { echo "$target runs SCRIPT=security.sh or sites.sh" >&2; exit 2; } ;;
	*) echo "unsupported VM target: $target" >&2; exit 2 ;;
esac

work=$(mktemp -d)
cleanup() {
	docker rm -f cairn-vm >/dev/null 2>&1 || true
	rm -rf "$work" dist/update-test dist/release dist/feed
}
trap cleanup EXIT

echo "== signed test releases (throwaway key)"
# QEMU user networking shows the container to the guest as 10.0.2.2.
bash test/lib/release-fixture.sh "$work/fixture" http://10.0.2.2:8080

echo "== $target VM"
docker run --rm --name cairn-vm \
	-v "$PWD/test/vm:/t:ro" -v "$PWD/test/files:/f:ro" -v "$work:/work" -v cairn-vm-images:/images \
	-e TARGET="$target" -e SCRIPT="$script" -e TRUSTED_KEY="$(awk '{print $1}' "$work/fixture/keys.txt")" \
	debian:trixie bash /t/host.sh
