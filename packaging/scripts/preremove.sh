#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Pre-remove script shared by the .deb and .rpm packages.
#   deb: $1 = "remove" | "upgrade" | "deconfigure"
#   rpm: $1 = 0 on erase, 1 on upgrade
# Services are stopped only when the package is really going away.
set -e

case "$1" in
	remove | 0)
		if [ -d /run/systemd/system ]; then
			# Remember what was enabled: a deb removed without purging and
			# installed again counts as an upgrade, and postinstall.sh then
			# enables exactly these again (not what the user had turned off).
			# The list lives in the helper's root-only directory: the panel
			# user owns /var/lib/cairn and could plant a link there.
			if [ -d /var/lib/cairn-helper ]; then
				: >/var/lib/cairn-helper/enabled-before-remove
				for u in cairn-helper.socket cairn-web.service cairn-firewall.service cairn-forward.service \
					/etc/systemd/system/cairn-cron-*.timer; do
					u=${u##*/}
					case "$u" in *'*'*) continue ;; esac
					systemctl is-enabled --quiet "$u" 2>/dev/null && echo "$u" >>/var/lib/cairn-helper/enabled-before-remove
				done
			fi
			systemctl disable --now cairn-web.service cairn-helper.socket cairn-helper.service >/dev/null 2>&1 || true
			# Scheduled tasks run the helper binary, which is going away.
			for t in /etc/systemd/system/cairn-cron-*.timer; do
				[ -e "$t" ] && systemctl disable --now "${t##*/}" >/dev/null 2>&1 || true
			done
			# A pending firewall rollback would run the helper too.
			systemctl stop 'cairn-fw-rollback-*.timer' 'cairn-ssh-port-*.timer' >/dev/null 2>&1 || true
			# Cairn's nftables tables (its firewall, and the port forwards
			# on ufw systems) stay loaded until the next boot; their unit
			# files go away with the package. firewalld and ufw keep the
			# rules made in the panel.
			systemctl disable cairn-firewall.service cairn-forward.service >/dev/null 2>&1 || true
		fi
		;;
esac
exit 0
