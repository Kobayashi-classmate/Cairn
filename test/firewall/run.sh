#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the firewall backends for real in throwaway containers (with
# CAP_NET_ADMIN in their own network namespace only): nftables on Debian,
# ufw on Ubuntu, and firewalld's policy file checked offline on Rocky
# (firewalld itself needs a VM). Between the phases a second container
# checks which ports are reachable; a third is the other host that a port
# forward sends connections to.
#
#   bash test/firewall/run.sh
#   FIREWALL_TARGETS="nftables" bash test/firewall/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
targets=${FIREWALL_TARGETS:-"nftables ufw firewalld"}
bin=test/firewall/fwtest
sh scripts/go.sh sh -c "CGO_ENABLED=0 go test -c -tags fwsystem -o $bin ./internal/helper"
net=cairn-fw-$$
server=cairn-fw-server-$$
peer=cairn-fw-client-$$
target=cairn-fw-target-$$
log=$(mktemp)
cleanup() {
	docker rm -f "$server" "$peer" "$target" >/dev/null 2>&1 || true
	rm -f "$log"
	docker network rm "$net" >/dev/null 2>&1 || true
	rm -f "$bin"
}
trap cleanup EXIT
docker network create "$net" >/dev/null
docker run -d --name "$peer" --network "$net" debian:trixie sleep 1800 >/dev/null
client=$(docker exec "$peer" hostname -I | awk '{print $1}')
docker run -d --name "$target" --network "$net" -v "$PWD/test/firewall:/t:ro" -e CAIRN_FW_LISTEN=80 debian:trixie \
	/t/fwtest -test.run '^TestFirewallListen$' >/dev/null
target_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$target")

# reach PORT -> 0 when the server accepts a connection from the client.
reach() {
	docker exec "$peer" timeout 3 bash -c "exec 3<>/dev/tcp/$server/$1 && head -c2 <&3" >/dev/null 2>&1
}
expect() { # open|closed PORT...
	local state=$1 p
	shift
	for p in "$@"; do
		if reach "$p"; then got=open; else got=closed; fi
		if [ "$got" != "$state" ]; then
			echo "FAIL: port $p is $got, want $state" >&2
			return 1
		fi
		echo "   port $p $got"
	done
}
phase() { # backend phase
	echo "== $1: $2"
	docker exec -e CAIRN_FW_BACKEND="$1" -e CAIRN_FW_PHASE="$2" -e CAIRN_FW_CLIENT="$client" -e CAIRN_FW_TARGET="$target_ip" "$server" \
		/t/fwtest -test.run '^TestFirewallSystem$' -test.count=1 >"$log" 2>&1 ||
		{ cat "$log"; return 1; }
}

run_backend() {
	local backend=$1 image packages
	case $backend in
		nftables) image=debian:trixie packages=nftables ;;
		ufw) image=ubuntu:24.04 packages="ufw nftables" ;;
		firewalld) image=rockylinux:9 packages=firewalld ;;
	esac
	docker rm -f "$server" >/dev/null 2>&1 || true
	# IP forwarding is set when the container starts: its /proc/sys is read-only.
	docker run -d --name "$server" --network "$net" --cap-add NET_ADMIN --sysctl net.ipv4.ip_forward=1 \
		-v "$PWD/test/firewall:/t:ro" "$image" sleep 900 >/dev/null
	if [ "$backend" = firewalld ]; then
		docker exec "$server" dnf install -y -q "$packages" >/dev/null 2>&1 || return 1
		phase firewalld firewalld-offline || return 1
		return
	fi
	docker exec -e DEBIAN_FRONTEND=noninteractive "$server" sh -c "apt-get update -qq && apt-get install -y -qq $packages" >/dev/null 2>&1 || return 1
	docker exec -d -e CAIRN_FW_LISTEN=80,8081,28443,3306 "$server" /t/fwtest -test.run '^TestFirewallListen$' || return 1
	sleep 1
	expect open 80 8081 || return 1
	phase "$backend" setup || return 1
	expect open 80 28443 || return 1
	expect closed 8081 3306 || return 1
	phase "$backend" allow8081 || return 1
	expect open 8081 || return 1
	phase "$backend" blockclient || return 1
	expect closed 80 8081 || return 1
	phase "$backend" rollback || return 1
	expect open 80 8081 || return 1
	phase "$backend" managed || return 1
	phase "$backend" ping || return 1
	expect open 80 || return 1
	phase "$backend" forward || return 1
	expect open 8090 8091 || return 1
	expect closed 8092 || return 1
	phase "$backend" forward-off || return 1
	expect closed 8090 8091 || return 1
	phase "$backend" disable || return 1
	expect open 3306 || return 1
}

failed=()
for b in $targets; do
	if ! run_backend "$b"; then
		failed+=("$b")
	fi
done
if [ ${#failed[@]} -gt 0 ]; then
	echo "Firewall test failed on: ${failed[*]}" >&2
	exit 1
fi
echo "Firewall test passed on all backends."
