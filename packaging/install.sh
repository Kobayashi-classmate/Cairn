#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Cairn online installer.
#
#   curl -fsSL https://<repo>/install.sh | sudo sh
#   curl -fsSL https://<repo>/install.sh | sudo sh -s -- --lang zh-CN --yes
#
# It detects the system, asks for the panel's default language, adds the
# signed Cairn package repository and installs the "cairn" package. It changes
# nothing else: system mirrors, /bin/sh, /etc/hosts and other global settings
# are left alone. Every step can be reproduced by hand; see the README.
#
# Options:
#   --lang en|zh-CN   default language of the panel (skips the question)
#   --repo URL        package repository (default: the one this script came from)
#   --yes             do not ask questions; use --lang or the system locale
#   --no-systemd-check  install even if systemd is not running (containers,
#                     image builds, tests); the services are then not started
set -eu

# Replaced when the repository is published (scripts/build-repo.sh).
REPO_URL_DEFAULT="@REPO_URL@"
KEY_FINGERPRINT="@KEY_FINGERPRINT@"

REPO_URL=${CAIRN_REPO_URL:-$REPO_URL_DEFAULT}
CHOSEN_LANG=""
ASSUME_YES=0
CHECK_SYSTEMD=1

while [ $# -gt 0 ]; do
	case "$1" in
		--no-systemd-check) CHECK_SYSTEMD=0; shift ;;
		--lang) CHOSEN_LANG=${2:-}; shift 2 ;;
		--lang=*) CHOSEN_LANG=${1#*=}; shift ;;
		--repo) REPO_URL=${2:-}; shift 2 ;;
		--repo=*) REPO_URL=${1#*=}; shift ;;
		--yes | -y) ASSUME_YES=1; shift ;;
		*) echo "Unknown option: $1" >&2; exit 2 ;;
	esac
done

# ---- Messages --------------------------------------------------------------
# The only user-facing text of this script. Translations live here, next to
# the English source, because the script must work as a single file.
UI_LANG=en
case "${LC_ALL:-${LANG:-}}" in zh*) UI_LANG=zh-CN ;; esac

msg() {
	key=$1
	shift
	if [ "$UI_LANG" = zh-CN ]; then
		case "$key" in
			need_root) text='请以 root 身份运行此安装程序（例如在命令前加 sudo）。' ;;
			need_systemd) text='Cairn 需要 systemd，当前系统没有以 systemd 启动。' ;;
			unsupported_os) text='不支持的系统：%s。支持 Debian 12+、Ubuntu 22.04+、RHEL/Rocky/AlmaLinux 8+。' ;;
			unsupported_arch) text='不支持的 CPU 架构：%s。支持 x86_64 和 aarch64。' ;;
			no_repo) text='没有设置软件源地址，请使用 --repo 指定。' ;;
			http_warning) text='警告：软件源使用 HTTP。软件包仍会校验签名，但建议使用 HTTPS。' ;;
			no_download) text='需要 curl 或 wget 来下载签名密钥。' ;;
			key_mismatch) text='签名密钥的指纹不符（得到 %s，期望 %s），已中止安装。' ;;
			detected) text='检测到系统：%s（%s）' ;;
			choose_lang) text='请选择面板默认语言（以后可以在面板里随时修改）：' ;;
			lang_prompt) text='输入序号 [%s]：' ;;
			adding_repo) text='正在添加 Cairn 软件源……' ;;
			installing) text='正在安装 Cairn……' ;;
			done) text='安装完成。请打开上面显示的初始化链接，创建管理员账号。' ;;
		esac
	else
		case "$key" in
			need_root) text='Please run this installer as root (for example with sudo).' ;;
			need_systemd) text='Cairn needs systemd, but this system was not booted with systemd.' ;;
			unsupported_os) text='Unsupported system: %s. Supported: Debian 12+, Ubuntu 22.04+, RHEL/Rocky/AlmaLinux 8+.' ;;
			unsupported_arch) text='Unsupported CPU architecture: %s. Supported: x86_64 and aarch64.' ;;
			no_repo) text='No package repository is configured; pass --repo.' ;;
			http_warning) text='Warning: the repository uses HTTP. Packages are still signature-checked, but HTTPS is recommended.' ;;
			no_download) text='curl or wget is needed to download the signing key.' ;;
			key_mismatch) text='Signing key fingerprint mismatch (got %s, expected %s); aborting.' ;;
			detected) text='Detected system: %s (%s)' ;;
			choose_lang) text='Choose the panel default language (you can change it in the panel at any time):' ;;
			lang_prompt) text='Enter a number [%s]: ' ;;
			adding_repo) text='Adding the Cairn package repository...' ;;
			installing) text='Installing Cairn...' ;;
			done) text='Done. Open the setup link shown above to create the administrator account.' ;;
		esac
	fi
	# shellcheck disable=SC2059 # the message is the format string
	printf "$text" "$@"
}

die() {
	msg "$@" >&2
	echo >&2
	exit 1
}

# ---- Checks ----------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die need_root
[ "$CHECK_SYSTEMD" -eq 0 ] || [ -d /run/systemd/system ] || die need_systemd
[ -n "$REPO_URL" ] && [ "$REPO_URL" != "@REPO_URL@" ] || die no_repo
REPO_URL=${REPO_URL%/}
case "$REPO_URL" in http://*) msg http_warning; echo ;; esac

arch=$(uname -m)
case "$arch" in x86_64 | aarch64) ;; *) die unsupported_arch "$arch" ;; esac

[ -r /etc/os-release ] || die unsupported_os "unknown"
# shellcheck disable=SC1091
. /etc/os-release
major=${VERSION_ID%%.*}
family=""
case "${ID:-}" in
	debian) [ "$major" -ge 12 ] 2>/dev/null && family=deb ;;
	ubuntu) [ "$major" -ge 22 ] 2>/dev/null && family=deb ;;
	rhel | rocky | almalinux | centos) [ "$major" -ge 8 ] 2>/dev/null && family=rpm ;;
esac
[ -n "$family" ] || die unsupported_os "${PRETTY_NAME:-$ID $VERSION_ID}"
msg detected "${PRETTY_NAME:-$ID}" "$arch"
echo

# ---- Language --------------------------------------------------------------
case "$CHOSEN_LANG" in
	en | zh-CN) ;;
	zh*) CHOSEN_LANG=zh-CN ;;
	"")
		default=1
		[ "$UI_LANG" = zh-CN ] && default=2
		answer=""
		if [ "$ASSUME_YES" -eq 0 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
			{
				echo
				msg choose_lang
				echo
				echo "  1) English"
				echo "  2) 简体中文"
				msg lang_prompt "$default"
			} >/dev/tty
			read -r answer </dev/tty || answer=""
		fi
		case "${answer:-$default}" in 2) CHOSEN_LANG=zh-CN ;; *) CHOSEN_LANG=en ;; esac
		;;
	*) CHOSEN_LANG=en ;;
esac
UI_LANG=$CHOSEN_LANG

# ---- Repository ------------------------------------------------------------
fetch() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die no_download
	fi
}

check_fingerprint() { # keyfile
	case "$KEY_FINGERPRINT" in "" | "@KEY_FINGERPRINT@") return 0 ;; esac
	command -v gpg >/dev/null 2>&1 || return 0
	got=$(gpg --show-keys --with-colons "$1" 2>/dev/null | awk -F: '/^fpr:/ {print $10; exit}')
	[ "$got" = "$KEY_FINGERPRINT" ] || die key_mismatch "${got:-?}" "$KEY_FINGERPRINT"
}

msg adding_repo
echo
if [ "$family" = deb ]; then
	install -d -m 0755 /etc/apt/keyrings
	fetch "$REPO_URL/cairn-archive-keyring.pgp" /etc/apt/keyrings/cairn.pgp.new
	check_fingerprint /etc/apt/keyrings/cairn.pgp.new
	chmod 0644 /etc/apt/keyrings/cairn.pgp.new
	mv /etc/apt/keyrings/cairn.pgp.new /etc/apt/keyrings/cairn.pgp
	cat >/etc/apt/sources.list.d/cairn.sources <<EOF
Types: deb
URIs: $REPO_URL/apt
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/cairn.pgp
EOF
	export DEBIAN_FRONTEND=noninteractive
	apt-get update
	msg installing
	echo
	CAIRN_LANG=$CHOSEN_LANG apt-get install -y cairn
else
	# Fetch and check the key ourselves instead of letting dnf download it.
	install -d -m 0755 /etc/pki/rpm-gpg
	fetch "$REPO_URL/cairn-archive-keyring.asc" /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
	check_fingerprint /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
	chmod 0644 /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
	mv /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn
	cat >/etc/yum.repos.d/cairn.repo <<EOF
[cairn]
name=Cairn
baseurl=$REPO_URL/rpm/\$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-cairn
EOF
	msg installing
	echo
	CAIRN_LANG=$CHOSEN_LANG dnf install -y cairn
fi

echo
msg done
echo
