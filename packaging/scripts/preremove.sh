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
		fi
		;;
esac
exit 0
