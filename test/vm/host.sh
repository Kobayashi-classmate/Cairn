#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside the throwaway QEMU container started by run.sh: serves the test
# releases, boots the cloud image with cloud-init, copies the guest scripts
# into the guest and runs $SCRIPT there over SSH. Exit status is the
# script's.
#
# With SERVICES=1 it also runs the services the acceptance needs, reached
# by the guest at 10.0.2.2: Pebble (Let's Encrypt's test CA) on 14000, an
# authoritative BIND for "test." on 53 (TSIG dynamic updates), and an
# S3 bucket (rclone serve s3) on 8081. With KEEP=1 the VM stays up and its
# panel, HTTP and HTTPS ports are forwarded on 28443, 28080 and 28444.
set -euo pipefail
t0=$(date +%s)
export DEBIAN_FRONTEND=noninteractive
extra=""
[ "${SERVICES:-0}" = 1 ] && extra="bind9 bind9-dnsutils"
apt-get update -qq >/dev/null
# shellcheck disable=SC2086 # a list of packages
apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils \
	openssh-client curl ca-certificates python3 $extra >/dev/null 2>&1

case "$TARGET" in
	debian-13) base=https://cloud.debian.org/images/cloud/trixie/latest img=debian-13-genericcloud-amd64.qcow2 sumfile=SHA512SUMS ;;
	debian-12) base=https://cloud.debian.org/images/cloud/bookworm/latest img=debian-12-genericcloud-amd64.qcow2 sumfile=SHA512SUMS ;;
	ubuntu-22.04) base=https://cloud-images.ubuntu.com/jammy/current img=jammy-server-cloudimg-amd64.img sumfile=SHA256SUMS ;;
	ubuntu-24.04) base=https://cloud-images.ubuntu.com/noble/current img=noble-server-cloudimg-amd64.img sumfile=SHA256SUMS ;;
	ubuntu-26.04) base=https://cloud-images.ubuntu.com/resolute/current img=resolute-server-cloudimg-amd64.img sumfile=SHA256SUMS ;;
	rocky-9) base=https://dl.rockylinux.org/pub/rocky/9/images/x86_64 img=Rocky-9-GenericCloud-Base.latest.x86_64.qcow2
		sumfile=$img.CHECKSUM ;;
	alma-10) base=https://repo.almalinux.org/almalinux/10/cloud/x86_64/images img=AlmaLinux-10-GenericCloud-latest.x86_64.qcow2
		sumfile=CHECKSUM ;;
esac
# SHA512SUMS / SHA256SUMS ("hash  file", "hash *file") or BSD style
# ("SHA256 (file) = hash").
sums=$(curl -fsSL --retry 5 --retry-all-errors "$base/$sumfile")
want=$(printf '%s\n' "$sums" | grep -E "(^| |\*|\()$img(\)| |$)" | grep -oE '\b[0-9a-f]{64,128}\b' | head -1)
[ -n "$want" ] || { echo "no checksum for $img"; exit 1; }
case ${#want} in 128) sum=sha512sum ;; *) sum=sha256sum ;; esac
if [ ! -f "/images/$img" ] || [ "$($sum "/images/$img" | cut -d' ' -f1)" != "$want" ]; then
	curl -fsSL --retry 5 --retry-all-errors -C - -o "/images/$img.part" "$base/$img"
	[ "$($sum "/images/$img.part" | cut -d' ' -f1)" = "$want" ] || { echo "image checksum mismatch"; exit 1; }
	mv "/images/$img.part" "/images/$img"
fi
echo "   image $img verified (${sum%sum})"

cd /work
python3 -m http.server 8080 --bind 127.0.0.1 --directory /work/fixture/srv >/work/http.log 2>&1 &

tsig=""
if [ "${SERVICES:-0}" = 1 ]; then
	tsig=$(head -c 32 /dev/urandom | base64)
	mkdir -p /work/bind /work/s3/cairn-backups
	cat >/etc/bind/named.conf <<EOF
options {
    directory "/var/cache/bind";
    listen-on { 127.0.0.1; };
    listen-on-v6 { none; };
    allow-query { any; };
    recursion no;
    dnssec-validation no;
};
key "cairn." {
    algorithm hmac-sha256;
    secret "$tsig";
};
zone "test." {
    type primary;
    file "/var/lib/bind/test.zone";
    allow-update { key "cairn."; };
    allow-transfer { key "cairn."; };
};
EOF
	# Every name points at this container: Pebble reaches the guest's port
	# 80 through the forward on 18080.
	cat >/var/lib/bind/test.zone <<EOF
\$TTL 60
@          IN SOA ns.test. admin.test. 1 60 60 600 60
@          IN NS  ns.test.
ns         IN A   127.0.0.1
site       IN A   127.0.0.1
www.site   IN A   127.0.0.1
app        IN A   127.0.0.1
shop       IN A   127.0.0.1
*          IN A   127.0.0.1
EOF
	chown bind:bind /var/lib/bind/test.zone
	named -u bind -c /etc/bind/named.conf
	cat >/work/pebble.json <<EOF
{"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
  "certificate": "/work/tools/pebble-test/certs/localhost/cert.pem", "privateKey": "/work/tools/pebble-test/certs/localhost/key.pem",
  "httpPort": 18080, "tlsPort": 18443, "ocspResponderURL": "", "externalAccountBindingRequired": false,
  "retryAfter": {"authz": 1, "order": 1}, "keyAlgorithm": "ecdsa"}}
EOF
	PEBBLE_VA_NOSLEEP=1 PEBBLE_WFE_NONCEREJECT=0 /work/tools/pebble -config /work/pebble.json -dnsserver 127.0.0.1:53 >/work/pebble.log 2>&1 &
	/work/tools/rclone serve s3 --auth-key TESTKEY,TESTSECRET --addr 0.0.0.0:8081 /work/s3 >/work/s3.log 2>&1 &
	for _ in $(seq 30); do
		dig +short @127.0.0.1 site.test | grep -q 127.0.0.1 && curl -sk -o /dev/null https://127.0.0.1:14000/dir && break
		sleep 1
	done
	echo "   test services: BIND (test.), Pebble, S3 bucket"
fi

qemu-img create -q -f qcow2 -b "/images/$img" -F qcow2 disk.qcow2 10G
[ -f key ] || ssh-keygen -q -t ed25519 -N '' -f key
cat >user-data <<EOF
#cloud-config
users:
  - name: tester
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys: [$(cat key.pub)]
EOF
echo "instance-id: cairn-vm" >meta-data
cloud-localds seed.img user-data meta-data

fwd="hostfwd=tcp:127.0.0.1:2222-:22,hostfwd=tcp:127.0.0.1:18081-:8081,hostfwd=tcp:127.0.0.1:18083-:8083"
fwd="$fwd,hostfwd=tcp:127.0.0.1:18080-:80"
[ "${KEEP:-0}" = 1 ] && fwd="$fwd,hostfwd=tcp:0.0.0.0:28080-:80,hostfwd=tcp:0.0.0.0:28444-:443"
qemu-system-x86_64 -machine q35 -accel tcg,thread=multi -cpu max -smp 4 -m 2048 \
	-drive file=disk.qcow2,if=virtio -drive file=seed.img,if=virtio,format=raw \
	-nic "user,model=virtio-net-pci,$fwd" -monitor unix:/work/qemu.mon,server,nowait \
	-display none -serial file:/work/serial.log -daemonize
ssh_opts=(-i key -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR)
for _ in $(seq 120); do ssh "${ssh_opts[@]}" tester@127.0.0.1 true 2>/dev/null && break; sleep 5; done
ssh "${ssh_opts[@]}" tester@127.0.0.1 true || { tail -30 /work/serial.log; echo "VM did not come up"; exit 1; }
echo "   VM reachable after $(($(date +%s) - t0))s"

files=(/t/inside.sh /t/security.sh /t/sites.sh /t/accept.sh /t/keep.sh /f/ws.py)
[ "${SERVICES:-0}" = 1 ] && cp /work/tools/pebble-test/certs/pebble.minica.pem /work/minica.pem && files+=(/work/minica.pem)
scp -q -P 2222 -i key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${files[@]}" tester@127.0.0.1:/tmp/
status=0
ssh "${ssh_opts[@]}" tester@127.0.0.1 "sudo TRUSTED_KEY='$TRUSTED_KEY' TSIG_SECRET='$tsig' bash /tmp/$SCRIPT" | tee /work/guest.log || status=$?
if [ "$SCRIPT" = security.sh ] && [ $status = 0 ]; then
	# From outside the VM (QEMU forwards from 10.0.2.2): the firewall lets
	# 8081 through and drops 8083.
	if curl -fs -m 10 -o /dev/null http://127.0.0.1:18081/ && ! curl -fs -m 10 -o /dev/null http://127.0.0.1:18083/; then
		echo "  - from outside the VM: port 8081 reachable, 8083 blocked by the firewall"
	else
		echo "FAIL: reachability from outside the VM (8081 should answer, 8083 not)"
		status=1
	fi
	ssh "${ssh_opts[@]}" tester@127.0.0.1 "sudo bash /tmp/security.sh purge" || status=$?
fi
if [ "$SCRIPT" = accept.sh ] && [ $status = 0 ]; then
	# After every firewall change: SSH still logs in, the site answers
	# from outside on port 80 (with its redirect to HTTPS).
	loc=$(curl -s -m 10 -o /dev/null -w '%{redirect_url}' -H 'Host: site.test' http://127.0.0.1:18080/x)
	if ssh "${ssh_opts[@]}" tester@127.0.0.1 true && [ "$loc" = https://site.test/x ]; then
		echo "  - from outside the VM: SSH logs in after the firewall changes; site.test answers on port 80 with its HTTPS redirect"
	else
		echo "FAIL: from outside the VM (ssh, or the redirect: '$loc')"
		status=1
	fi
fi
if [ "${KEEP:-0}" = 1 ]; then
	[ $status = 0 ] || exit $status
	port=$(ssh "${ssh_opts[@]}" tester@127.0.0.1 "sudo sed -n 's/^listen = \"0.0.0.0:\([0-9]*\)\"/\1/p' /etc/cairn/config.toml")
	python3 - "$port" <<'PY'
import socket, sys
s = socket.socket(socket.AF_UNIX)
s.connect("/work/qemu.mon")
s.sendall(("hostfwd_add tcp:0.0.0.0:28443-:%s\n" % sys.argv[1]).encode())
s.recv(4096)
s.close()
PY
	echo "== READY"
	sed -n '/^-- setup$/,$p' /work/guest.log | sed '1d'
	[ "$SCRIPT" = accept.sh ] && echo "Panel: https://127.0.0.1:28443/ (the acceptance's admin account, see accept.sh)"
	echo "TSIG key \"cairn.\" (hmac-sha256): $tsig"
	exec sleep infinity
fi
ssh "${ssh_opts[@]}" tester@127.0.0.1 'sudo poweroff' 2>/dev/null || true
echo "   total $(($(date +%s) - t0))s"
exit $status
