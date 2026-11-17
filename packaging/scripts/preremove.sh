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
			systemctl disable --now cairn-web.service cairn-helper.socket cairn-helper.service >/dev/null 2>&1 || true
			# Scheduled tasks run the helper binary, which is going away.
			for t in /etc/systemd/system/cairn-cron-*.timer; do
				[ -e "$t" ] && systemctl disable --now "${t##*/}" >/dev/null 2>&1 || true
			done
			# A pending firewall rollback would run the helper too.
			systemctl stop 'cairn-fw-rollback-*.timer' 'cairn-ssh-port-*.timer' >/dev/null 2>&1 || true
			# Cairn's nftables table stays loaded until the next boot; its
			# unit file goes away with the package. firewalld and ufw keep
			# the rules made in the panel.
			systemctl disable cairn-firewall.service >/dev/null 2>&1 || true
		fi
		;;
esac
exit 0
