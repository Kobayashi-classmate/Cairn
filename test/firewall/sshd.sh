#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks how Cairn finds the SSH ports against a real OpenSSH (and, on
# Debian and Ubuntu, a real Dropbear) in the ways that the "port" lines of
# sshd -T alone get wrong. Runs as root inside a throwaway container (see
# run.sh) with the test binary at /t/fwtest; it installs the SSH servers
# and rewrites their configuration.
set -eu
# shellcheck disable=SC1091
. /etc/os-release
deb=1
case " $ID ${ID_LIKE:-} " in *" rhel "* | *" fedora "*) deb=0 ;; esac
if [ "$deb" = 1 ]; then
	export DEBIAN_FRONTEND=noninteractive
	{ apt-get update -qq && apt-get install -y -qq openssh-server dropbear-bin; } >/tmp/install.log 2>&1 || { tail -20 /tmp/install.log; exit 1; }
else
	dnf install -y -q openssh-server >/tmp/install.log 2>&1 || { tail -20 /tmp/install.log; exit 1; }
fi
mkdir -p /run/sshd /etc/ssh/sshd_config.d
ssh-keygen -A >/dev/null
grep -q '^Include /etc/ssh/sshd_config.d/' /etc/ssh/sshd_config || { echo "FAIL: sshd_config does not include sshd_config.d" >&2; exit 1; }

# found PORTS UNREAD WHAT: the detection finds exactly PORTS, and says
# whether sshd's configuration could be read.
found() {
	if ! CAIRN_SSH_EXPECT="$1" CAIRN_SSH_UNREAD="$2" /t/fwtest -test.run '^TestSSHDetectSystem$' -test.count=1 >/tmp/detect.log 2>&1; then
		cat /tmp/detect.log >&2
		echo "FAIL: $3" >&2
		echo "--- sshd -T:" >&2
		/usr/sbin/sshd -T 2>&1 | grep -E '^(port|listenaddress) |line [0-9]' >&2 || true
		exit 1
	fi
	echo "   $3: ${1:-no port}"
}
stop() {
	for p in /proc/[0-9]*; do
		case "$(cat "$p/comm" 2>/dev/null)" in
			sshd | dropbear) kill "${p#/proc/}" 2>/dev/null || true ;;
		esac
	done
	sleep 0.5
}
drop=/etc/ssh/sshd_config.d

found "22" no "the default configuration"

printf 'ListenAddress 0.0.0.0:2222\n' >$drop/10-listen.conf
found "22 2222" no "ListenAddress with a port and no Port (sshd -T still prints port 22)"
/usr/sbin/sshd
sleep 1
found "22 2222" no "the same with sshd running on 2222 only"
stop
rm -f $drop/10-listen.conf

printf 'Port 2222\nPort 2200\n' >$drop/00-cairn.conf
found "2200 2222" no "two Port lines"
/usr/sbin/sshd
sleep 1
printf 'BogusOption yes\n' >$drop/99-bad.conf
found "2200 2222" yes "a file sshd cannot read while sshd runs on 2200 and 2222"
stop
found "" yes "the same with sshd stopped"
rm -f $drop/99-bad.conf $drop/00-cairn.conf

# An OpenSSH installed elsewhere: no /usr/sbin/sshd to ask, but a process
# named sshd that listens.
mkdir -p /usr/local/sbin
mv /usr/sbin/sshd /usr/local/sbin/sshd
found "" no "no sshd at /usr/sbin and nothing listening"
/usr/local/sbin/sshd -p 2022
sleep 1
found "2022" no "an sshd from /usr/local/sbin listening on 2022"
stop
mv /usr/local/sbin/sshd /usr/sbin/sshd

if [ "$deb" = 1 ]; then
	mv /usr/sbin/sshd /usr/sbin/sshd.off
	dropbear -R -p 2201
	sleep 1
	found "2201" no "Dropbear listening on 2201, no sshd"
	stop
	mv /usr/sbin/sshd.off /usr/sbin/sshd
fi
echo "   SSH port detection passed on ${PRETTY_NAME} ($(/usr/sbin/sshd -V 2>&1 | grep -m1 -o 'OpenSSH_[^ ,]*' || echo OpenSSH))"
