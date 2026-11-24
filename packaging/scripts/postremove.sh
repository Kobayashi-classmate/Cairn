#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Post-remove script shared by the .deb and .rpm packages.
#   deb: $1 = "remove" | "purge" | "upgrade" | ...
#   rpm: $1 = 0 on erase, 1 on upgrade
# "apt remove" and "dnf remove" keep the configuration and the panel's data.
# "apt purge" deletes them, but keeps the local backups and database dumps
# in /var/lib/cairn/backups unless CAIRN_PURGE_BACKUPS=1 is set: they may be
# the only copy. RPM has no purge step. README.md ("Uninstall") lists what
# each one leaves behind. Websites and databases managed by Cairn, and what
# they need to keep running, are never deleted here.
set -e

data=/var/lib/cairn
backups=$data/backups
readme=https://github.com/Kobayashi-classmate/Cairn#uninstall

# has_entries DIR: DIR is a directory with something in it.
has_entries() {
	[ -d "$1" ] && [ -n "$(ls -A "$1" 2>/dev/null)" ]
}

case "$1" in
	remove | 0)
		if [ -d /etc/cairn ] || [ -d "$data" ]; then
			echo "Cairn: configuration and data were kept in /etc/cairn, $data and /var/lib/cairn-helper"
			echo "Cairn: (local backups and database dumps: $backups)."
			if [ "$1" = remove ]; then
				echo "Cairn: purging the package deletes them, except the backups; see $readme"
			else
				echo "Cairn: to delete them, see $readme"
			fi
		fi
		;;
	purge)
		# /etc/cairn/apps stays while it holds app sites' environment
		# files: they belong to the websites, whose units keep running.
		if [ -d /etc/cairn ]; then
			find /etc/cairn -mindepth 1 -maxdepth 1 ! -name apps -exec rm -rf {} +
			rmdir /etc/cairn/apps 2>/dev/null || true
			rmdir /etc/cairn 2>/dev/null || true
		fi
		# The data directory belongs to the panel's account, so nothing in
		# it is trusted: rm and find remove links, never follow them.
		if [ "${CAIRN_PURGE_BACKUPS:-}" = 1 ]; then
			rm -rf "$data"
		elif [ -d "$data" ] && [ ! -L "$data" ]; then
			find "$data" -mindepth 1 -maxdepth 1 ! -name backups -exec rm -rf {} +
			if [ -L "$backups" ] || { [ -e "$backups" ] && [ ! -d "$backups" ]; }; then
				rm -f "$backups"
			fi
			# Directories made on first use hold no backup. Tidying up
			# never fails the purge.
			if [ -d "$backups" ]; then
				find "$backups" -depth -type d -empty -delete 2>/dev/null || true
			fi
			rmdir "$data" 2>/dev/null || true
		else
			rm -f "$data"
		fi
		if [ -d "$backups" ]; then
			echo "Cairn: local backups and database dumps were kept in $backups."
			echo "Cairn: a new installation finds them there; to delete them: rm -rf $data"
		elif [ "${CAIRN_PURGE_BACKUPS:-}" = 1 ]; then
			echo "Cairn: local backups and database dumps were deleted (CAIRN_PURGE_BACKUPS=1)."
		fi
		# Backup plans (with their bucket settings) and scheduled tasks are
		# the panel's: its database, which lists them, is gone. Their copies
		# in buckets stay. Cairn never stores the private keys of encrypted
		# backups, so nothing needed to decrypt them is lost.
		if has_entries /var/lib/cairn-helper/backup/plans; then
			echo "Cairn: backup plans and their storage settings were deleted; backups already in a bucket were not."
		fi
		rm -rf /var/lib/cairn-helper /var/lib/cairn-unpack
		if has_entries /etc/cairn-cron; then
			echo "Cairn: scheduled tasks were deleted."
		fi
		rm -rf /etc/cairn-cron
		rm -f /etc/systemd/system/cairn-cron-*.service /etc/systemd/system/cairn-cron-*.timer
		# Left enabled when removed without systemd running (a chroot).
		rm -f /etc/systemd/system/timers.target.wants/cairn-cron-*.timer
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
