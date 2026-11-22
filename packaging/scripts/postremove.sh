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
		# /etc/cairn/apps stays: app sites' environment files belong to the
		# websites, whose units keep running.
		if [ -d /etc/cairn ]; then
			find /etc/cairn -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} +
			rmdir /etc/cairn 2>/dev/null || true
		fi
		rm -rf /var/lib/cairn /var/lib/cairn-helper
		rm -rf /etc/cairn-cron
		rm -f /etc/systemd/system/cairn-cron-*.service /etc/systemd/system/cairn-cron-*.timer
		# Cairn's own nftables tables go (its firewall, and the port forwards
		# on ufw systems); rules in firewalld or ufw stay (removing them could
		# close the SSH port), except the block that let port forwards
		# through ufw, which now has nothing to forward.
		if command -v nft >/dev/null 2>&1; then
			nft delete table inet cairn >/dev/null 2>&1 || true
			nft delete table inet cairn_forward >/dev/null 2>&1 || true
		fi
		for f in /etc/ufw/before.rules /etc/ufw/before6.rules; do
			[ -f "$f" ] && sed -i '/^# BEGIN Cairn port forwards/,/^# END Cairn port forwards/d' "$f"
		done
		rm -rf /etc/cairn-firewall
		# IP forwarding stays as it is until the next boot.
		rm -f /etc/sysctl.d/60-cairn-forward.conf
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
