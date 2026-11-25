#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs the firewall backends for real in throwaway containers (with
# CAP_NET_ADMIN in their own network namespace only): nftables on Debian,
# ufw on Ubuntu, and on Rocky firewalld's policy file checked offline, then
# firewalld itself (on a system bus of the container's own) for the port
# forwards. Between the phases a second container checks which ports are
# reachable, and what passes when it uses the server as its router; a
# third is the other host that a port forward sends connections to. The
# target "installer" checks the firewall
# step of packaging/install.sh with a stand-in helper (installer.sh), under
# dash on Debian and bash on Rocky; the target "sshd" checks how the SSH
# ports are found against a real sshd on Debian, Ubuntu and Rocky (sshd.sh).
#
#   bash test/firewall/run.sh
#   FIREWALL_TARGETS="nftables" bash test/firewall/run.sh
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
targets=${FIREWALL_TARGETS:-"installer sshd nftables ufw firewalld"}
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
# The client routes through the server for "routed": it ignores ICMP
# redirects (which it does while it forwards itself), so that it keeps
# sending through the server what the server could hand to the target.
docker run -d --name "$peer" --network "$net" --cap-add NET_ADMIN \
	--sysctl net.ipv4.ip_forward=1 --sysctl net.ipv4.conf.all.accept_redirects=0 debian:trixie sleep 1800 >/dev/null
docker exec -e DEBIAN_FRONTEND=noninteractive "$peer" sh -c "apt-get update -qq && apt-get install -y -qq iproute2" >/dev/null 2>&1
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
# routed open|closed PORT... -> what the client reaches on the target's
# address with the server as its router: a connection the server only
# routes, which no port forward may take.
routed() {
	local state=$1 p got gw
	shift
	gw=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$server")
	docker exec "$peer" ip route replace "$target_ip/32" via "$gw" || return 1
	for p in "$@"; do
		if docker exec "$peer" timeout 3 bash -c "exec 3<>/dev/tcp/$target_ip/$p && head -c2 <&3" >/dev/null 2>&1; then got=open; else got=closed; fi
		if [ "$got" != "$state" ]; then
			echo "FAIL: port $p of the target, routed through the server, is $got, want $state" >&2
			docker exec "$peer" ip route del "$target_ip/32" >/dev/null 2>&1
			return 1
		fi
		echo "   routed port $p $got"
	done
	docker exec "$peer" ip route del "$target_ip/32"
}
phase() { # backend phase
	echo "== $1: $2"
	docker exec -e CAIRN_FW_BACKEND="$1" -e CAIRN_FW_PHASE="$2" -e CAIRN_FW_CLIENT="$client" -e CAIRN_FW_TARGET="$target_ip" "$server" \
		/t/fwtest -test.run '^TestFirewallSystem$' -test.count=1 >"$log" 2>&1 ||
		{ cat "$log"; return 1; }
}

run_installer() {
	local image
	for image in debian:trixie rockylinux:9; do
		echo "== installer: $image"
		docker run --rm -v "$PWD:/src:ro" "$image" sh /src/test/firewall/installer.sh || return 1
	done
}

run_sshd() {
	local image
	for image in debian:trixie ubuntu:24.04 rockylinux:9; do
		echo "== sshd: $image"
		docker run --rm -v "$PWD/test/firewall:/t:ro" "$image" sh /t/sshd.sh || return 1
	done
}

run_backend() {
	local backend=$1 image packages
	if [ "$backend" = installer ]; then
		run_installer
		return
	fi
	if [ "$backend" = sshd ]; then
		run_sshd
		return
	fi
	case $backend in
		nftables) image=debian:trixie packages=nftables ;;
		ufw) image=ubuntu:24.04 packages="ufw nftables" ;;
		firewalld) image=rockylinux:9 packages="firewalld dbus-daemon" ;;
	esac
	docker rm -f "$server" >/dev/null 2>&1 || true
	# IP forwarding is set when the container starts: its /proc/sys is read-only.
	docker run -d --name "$server" --network "$net" --cap-add NET_ADMIN --sysctl net.ipv4.ip_forward=1 \
		-v "$PWD/test/firewall:/t:ro" "$image" sleep 900 >/dev/null
	if [ "$backend" = firewalld ]; then
		docker exec "$server" sh -c "dnf install -y -q $packages" >/dev/null 2>&1 || return 1
		phase firewalld firewalld-offline || return 1
		docker exec "$server" sh -c "mkdir -p /run/dbus && dbus-daemon --system --fork" || return 1
		docker exec -d "$server" firewalld --nofork --nopid || return 1
		docker exec "$server" sh -c 'for i in $(seq 60); do firewall-cmd --state >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1' || return 1
		docker exec -d -e CAIRN_FW_LISTEN=80,8081,28443 "$server" /t/fwtest -test.run '^TestFirewallListen$' || return 1
		sleep 1
		phase firewalld setup || return 1
		expect open 80 28443 || return 1
		expect closed 8081 || return 1
		run_forwards firewalld || return 1
		return
	fi
	docker exec -e DEBIAN_FRONTEND=noninteractive "$server" sh -c "apt-get update -qq && apt-get install -y -qq $packages" >/dev/null 2>&1 || return 1
	docker exec -d -e CAIRN_FW_LISTEN=80,8081,8082,28443,3306 "$server" /t/fwtest -test.run '^TestFirewallListen$' || return 1
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
	# A change left unconfirmed by a process that is gone (as after a
	# reboot) is undone by what runs at boot.
	phase "$backend" unconfirmed || return 1
	expect open 8082 || return 1
	phase "$backend" recover || return 1
	expect closed 8082 || return 1
	expect open 80 8081 || return 1
	# A rollback whose backend command fails stays pending until one works.
	phase "$backend" rollback-fails || return 1
	expect open 80 8081 || return 1
	phase "$backend" managed || return 1
	phase "$backend" ping || return 1
	expect open 80 || return 1
	run_forwards "$backend" || return 1
	phase "$backend" disable || return 1
	expect open 3306 || return 1
}

# run_forwards BACKEND: 8090 goes to the server's port 80, 8091 to the
# target's port 80.
run_forwards() {
	local backend=$1
	phase "$backend" forward || return 1
	expect open 8090 8091 || return 1
	expect closed 8092 || return 1
	# Rules an earlier version wrote are rewritten when the helper starts.
	phase "$backend" reapply || return 1
	expect open 8090 8091 || return 1
	# Not addressed to the server: no forward takes it.
	routed closed 8091 || return 1
	case $backend in
		nftables)
			# Forwarding was on before Cairn (as Docker leaves it): what
			# the server routes is none of Cairn's business.
			routed open 80 || return 1
			# Forwarding switched on by Cairn: the forwards pass, routing
			# for others does not.
			phase "$backend" forward-guard || return 1
			expect open 8090 8091 || return 1
			routed closed 80 || return 1
			;;
		ufw)
			# ufw drops what it would route by default.
			routed closed 80 || return 1
			;;
	esac
	# Blocked addresses and deny rules hold for the forwards too.
	phase "$backend" forward-block || return 1
	expect closed 8090 8091 || return 1
	phase "$backend" rollback || return 1
	expect open 8090 8091 || return 1
	phase "$backend" forward-deny || return 1
	expect closed 8091 || return 1
	expect open 8090 || return 1
	phase "$backend" rollback || return 1
	expect open 8091 || return 1
	phase "$backend" forward-off || return 1
	expect closed 8090 8091 || return 1
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
