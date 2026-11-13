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
		rm -rf /etc/cairn /var/lib/cairn
		;;
esac

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
exit 0
