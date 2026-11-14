#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside the throwaway QEMU container started by run.sh: serves the test
# releases, boots the cloud image with cloud-init, copies inside.sh into the
# guest and runs it over SSH. Exit status is that of inside.sh.
set -euo pipefail
t0=$(date +%s)
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils \
	openssh-client curl ca-certificates python3 >/dev/null 2>&1

case "$TARGET" in
	debian-13) base=https://cloud.debian.org/images/cloud/trixie/latest img=debian-13-genericcloud-amd64.qcow2 ;;
	debian-12) base=https://cloud.debian.org/images/cloud/bookworm/latest img=debian-12-genericcloud-amd64.qcow2 ;;
esac
sums=$(curl -fsSL --retry 5 --retry-all-errors "$base/SHA512SUMS")
want=$(printf '%s\n' "$sums" | awk -v f="$img" '$2 == f {print $1}')
[ -n "$want" ] || { echo "no checksum for $img"; exit 1; }
if [ ! -f "/images/$img" ] || [ "$(sha512sum "/images/$img" | cut -d' ' -f1)" != "$want" ]; then
	curl -fsSL --retry 5 --retry-all-errors -C - -o "/images/$img.part" "$base/$img"
	[ "$(sha512sum "/images/$img.part" | cut -d' ' -f1)" = "$want" ] || { echo "image checksum mismatch"; exit 1; }
	mv "/images/$img.part" "/images/$img"
fi
echo "   image $img verified (SHA512)"

cd /work
python3 -m http.server 8080 --bind 127.0.0.1 --directory /work/fixture/srv >/work/http.log 2>&1 &
qemu-img create -q -f qcow2 -b "/images/$img" -F qcow2 disk.qcow2 10G
ssh-keygen -q -t ed25519 -N '' -f key
cat >user-data <<EOF
#cloud-config
users:
  - name: tester
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys: [$(cat key.pub)]
EOF
echo "instance-id: cairn-vm" >meta-data
cloud-localds seed.img user-data meta-data

qemu-system-x86_64 -machine q35 -accel tcg,thread=multi -cpu max -smp 4 -m 2048 \
	-drive file=disk.qcow2,if=virtio -drive file=seed.img,if=virtio,format=raw \
	-nic user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:2222-:22 \
	-display none -serial file:/work/serial.log -daemonize
ssh_opts=(-i key -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR)
for _ in $(seq 120); do ssh "${ssh_opts[@]}" tester@127.0.0.1 true 2>/dev/null && break; sleep 5; done
ssh "${ssh_opts[@]}" tester@127.0.0.1 true || { tail -30 /work/serial.log; echo "VM did not come up"; exit 1; }
echo "   VM reachable after $(($(date +%s) - t0))s"

scp -q -P 2222 -i key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null /t/inside.sh tester@127.0.0.1:/tmp/inside.sh
status=0
ssh "${ssh_opts[@]}" tester@127.0.0.1 "sudo TRUSTED_KEY='$TRUSTED_KEY' bash /tmp/inside.sh" || status=$?
ssh "${ssh_opts[@]}" tester@127.0.0.1 'sudo poweroff' 2>/dev/null || true
echo "   total $(($(date +%s) - t0))s"
exit $status
