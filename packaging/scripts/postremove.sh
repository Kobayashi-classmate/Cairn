#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Post-remove script shared by the .deb and .rpm packages.
#   deb: $1 = "remove" | "purge" | "upgrade" | ...
#   rpm: $1 = 0 on erase, 1 on upgrade
# "apt purge" also deletes the configuration and the panel's own data. RPM has
# no purge step, so on RPM systems they are kept and must be removed by hand.
# Websites and databases managed by Cairn are never deleted here.
set -e

case "$1" in
	purge)
		rm -rf /etc/cairn /var/lib/cairn /var/lib/cairn-helper
		rm -rf /etc/cairn-cron
		rm -f /etc/systemd/system/cairn-cron-*.service /etc/systemd/system/cairn-cron-*.timer
		# Cairn's own nftables table goes; rules in firewalld or ufw stay
		# (removing them could close the SSH port).
		if command -v nft >/dev/null 2>&1; then
			nft delete table inet cairn >/dev/null 2>&1 || true
		fi
		rm -rf /etc/cairn-firewall
		;;
esac

# rpm erase: drop the SELinux label rule postinstall added.
if [ "$1" = 0 ] && command -v semanage >/dev/null 2>&1; then
	semanage fcontext -d '/usr/lib/cairn(/.*)?' >/dev/null 2>&1 || true
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
exit 0
