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
# The root helper extracts 7z and RAR archives as this account, in a unit of
# its own, so that the extractor never runs as root or as a user whose
# processes could outlive it. It owns nothing outside its scratch directory
# (/var/lib/cairn-unpack, made by the helper when needed).
if ! getent group cairn-unpack >/dev/null 2>&1; then
	groupadd --system cairn-unpack
fi
if ! getent passwd cairn-unpack >/dev/null 2>&1; then
	shell=/bin/false
	for s in /usr/sbin/nologin /sbin/nologin; do
		if [ -x "$s" ]; then shell=$s; break; fi
	done
	useradd --system --gid cairn-unpack --home-dir /nonexistent --no-create-home \
		--shell "$shell" --comment "Cairn archive extraction" cairn-unpack
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

# SELinux (EL): /usr/lib is lib_t, and systemd runs a lib_t program in its
# own init_t domain, where many operations are refused without a trace.
# Labeled bin_t, the helper runs as an unconfined service, like cairn-web.
# Done on every install and upgrade, before the services (re)start.
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
	if command -v semanage >/dev/null 2>&1; then
		semanage fcontext -a -t bin_t '/usr/lib/cairn(/.*)?' >/dev/null 2>&1 ||
			semanage fcontext -m -t bin_t '/usr/lib/cairn(/.*)?' >/dev/null 2>&1 || true
		restorecon -R /usr/lib/cairn >/dev/null 2>&1 || true
	fi
	chcon -R -t bin_t /usr/lib/cairn >/dev/null 2>&1 || true
fi

# Installed again after a remove that kept the data (a deb without purge
# arrives here as an upgrade, an rpm as a first install): preremove.sh listed
# the units that were enabled then, scheduled tasks and the firewall included.
reenable=/var/lib/cairn-helper/enabled-before-remove
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	if [ "$first_install" = 1 ]; then
		systemctl enable --now cairn-helper.socket cairn-web.service
	fi
	if [ -s "$reenable" ]; then
		while read -r u; do
			case "$u" in cairn-*.service | cairn-*.socket | cairn-cron-*.timer) ;; *) continue ;; esac
			systemctl enable --now "$u" >/dev/null 2>&1 || true
		done <"$reenable"
	elif [ "$first_install" != 1 ]; then
		systemctl try-restart cairn-helper.service cairn-web.service
	fi
fi
rm -f "$reenable"


# Only how to get the setup link: the link itself would be kept in the
# package manager's logs (apt's term.log is readable by the adm group) while
# it is valid. install.sh prints the link after the package is installed.
if [ "$first_install" = 1 ]; then
	lang=$(sed -n 's/^default_locale *= *"\(.*\)"/\1/p' /etc/cairn/config.toml)
	echo
	/usr/bin/cairn setup-link --hint --lang "${lang:-en}" || true
	echo
fi
exit 0
