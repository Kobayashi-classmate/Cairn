#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Post-install script shared by the .deb and .rpm packages.
#   deb: $1 = "configure", $2 = previously configured version (empty on first install)
#   rpm: $1 = 1 on first install, 2 on upgrade
# It creates the system account and directories, writes the configuration on
# first install only, starts the services and prints the one-time setup link.
# It never touches system-wide settings outside Cairn's own files.
set -e

first_install=0
case "$1" in
	configure) [ -z "${2:-}" ] && first_install=1 ;;
	1) first_install=1 ;;
esac

# Map CAIRN_LANG, then the system locale, onto a supported panel language.
pick_locale() {
	for v in "${CAIRN_LANG:-}" "${LC_ALL:-}" "${LANG:-}"; do
		case "$v" in
			zh*) echo "zh-CN"; return ;;
			en*) echo "en"; return ;;
		esac
	done
	echo "en"
}

# Random port in 20000-59999 so the panel is not on a well-known port.
random_port() {
	n=$(od -An -N2 -tu2 /dev/urandom | tr -d ' ')
	echo $((n % 40000 + 20000))
}

if ! getent group cairn >/dev/null 2>&1; then
	groupadd --system cairn
fi
if ! getent passwd cairn >/dev/null 2>&1; then
	# Minimal images may lack util-linux, which ships nologin.
	shell=/bin/false
	for s in /usr/sbin/nologin /sbin/nologin; do
		if [ -x "$s" ]; then shell=$s; break; fi
	done
	useradd --system --gid cairn --home-dir /var/lib/cairn --no-create-home \
		--shell "$shell" --comment "Cairn panel" cairn
fi

install -d -m 0750 -o root -g cairn /etc/cairn
install -d -m 0700 -o cairn -g cairn /var/lib/cairn
# Root helper state: update status and the packages kept for rollback.
install -d -m 0700 -o root -g root /var/lib/cairn-helper

if [ ! -f /etc/cairn/config.toml ]; then
	locale=$(pick_locale)
	port=$(random_port)
	umask 027
	cat >/etc/cairn/config.toml <<EOF
# SPDX-License-Identifier: AGPL-3.0-only
# Cairn configuration. Managed by Cairn; edit with care.

[server]
listen = "0.0.0.0:${port}"

[ui]
default_locale = "${locale}"

[paths]
data_dir = "/var/lib/cairn"
helper_socket = "/run/cairn/helper.sock"
EOF
	chown root:cairn /etc/cairn/config.toml
	chmod 0640 /etc/cairn/config.toml
fi

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	if [ "$first_install" = 1 ]; then
		systemctl enable --now cairn-helper.socket cairn-web.service
	else
		systemctl try-restart cairn-helper.service cairn-web.service
	fi
fi

if [ "$first_install" = 1 ]; then
	lang=$(sed -n 's/^default_locale *= *"\(.*\)"/\1/p' /etc/cairn/config.toml)
	echo
	/usr/bin/cairn setup-link --lang "${lang:-en}" || true
	echo
fi
exit 0
