#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Cairn online installer.
#
#   curl -fsSL https://github.com/Kobayashi-classmate/Cairn/releases/latest/download/install.sh | sudo sh
#   curl -fsSL .../install.sh | sudo sh -s -- --lang zh-CN --version 0.2.0 --yes
#
# It detects the system, asks for the panel's default language, downloads the
# Cairn package of the release, checks it against the release's signed
# checksum list and installs it with apt-get or dnf. No package repository is
# added: dependencies come from the system's own sources, and later versions
# are installed through the panel's online update. Nothing else changes:
# system mirrors, /bin/sh, /etc/hosts and other global settings are left
# alone. Every step can be reproduced by hand; see the README.
#
# Options:
#   --lang en|zh-CN   default language of the panel (skips the question)
#   --version X.Y.Z   install exactly this version (default: the latest release)
#   --mirror URL      download the release files from a mirror of
#                     https://github.com/Kobayashi-classmate/Cairn/releases
#                     with the same layout; signatures are checked as usual
#   --with-repo       instead, add the signed Cairn package repository and
#                     install from it (the repository installer's default)
#   --repo URL        package repository for --with-repo
#   --yes             do not ask questions; use --lang or the system locale
#   --no-systemd-check  install even if systemd is not running (containers,
#                     image builds, tests); the services are then not started
set -eu

# Filled in when the installer is published: by tools/release (release files,
# ed25519 keys) and by scripts/build-repo.sh (repository mode, OpenPGP key).
TRUSTED_KEYS=""
MODE="release"
REPO_URL_DEFAULT="@REPO_URL@"
KEY_FINGERPRINT="@KEY_FINGERPRINT@"

RELEASES_URL=${CAIRN_RELEASES_URL:-https://github.com/Kobayashi-classmate/Cairn/releases}
REPO_URL=${CAIRN_REPO_URL:-$REPO_URL_DEFAULT}
WANT_VERSION=""
CHOSEN_LANG=""
ASSUME_YES=0
CHECK_SYSTEMD=1

while [ $# -gt 0 ]; do
	case "$1" in
		--no-systemd-check) CHECK_SYSTEMD=0; shift ;;
		--lang) CHOSEN_LANG=${2:-}; shift 2 ;;
		--lang=*) CHOSEN_LANG=${1#*=}; shift ;;
		--version) WANT_VERSION=${2:-}; shift 2 ;;
		--version=*) WANT_VERSION=${1#*=}; shift ;;
		--mirror) RELEASES_URL=${2:-}; shift 2 ;;
		--mirror=*) RELEASES_URL=${1#*=}; shift ;;
		--with-repo) MODE=repo; shift ;;
		--repo) MODE=repo; REPO_URL=${2:-}; shift 2 ;;
		--repo=*) MODE=repo; REPO_URL=${1#*=}; shift ;;
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
			bad_version) text='版本号格式不对：%s（应为 X.Y.Z）。' ;;
			no_keys) text='此安装程序没有内置签名公钥，请从 Cairn 的正式发布页面下载安装程序。' ;;
			no_repo) text='没有设置软件源地址，请使用 --repo 指定。' ;;
			http_warning) text='警告：下载地址使用 HTTP。文件仍会校验签名，但建议使用 HTTPS。' ;;
			no_download) text='需要 curl 或 wget 来下载 Cairn。' ;;
			download_failed) text='下载失败：%s' ;;
			installing_openssl) text='正在安装 openssl，用于校验发布签名……' ;;
			no_verifier) text='无法校验发布签名：需要 OpenSSL 3（EL 8 可从 EPEL 安装 openssl3），或者改用 --with-repo。' ;;
			bad_signature) text='发布文件的签名无效，已中止，没有安装任何东西。' ;;
			no_package) text='该版本没有适用于本系统的软件包（%s，%s）。' ;;
			bad_checksum) text='下载的软件包与签名的校验值不一致，已中止，没有安装任何东西。' ;;
			key_mismatch) text='签名密钥的指纹不符（得到 %s，期望 %s），已中止安装。' ;;
			detected) text='检测到系统：%s（%s）' ;;
			choose_lang) text='请选择面板默认语言（以后可以在面板里随时修改）：' ;;
			lang_prompt) text='输入序号 [%s]：' ;;
			fetching) text='正在获取发布信息……' ;;
			verified) text='签名校验通过：%s' ;;
			adding_repo) text='正在添加 Cairn 软件源……' ;;
			installing) text='正在安装 Cairn %s……' ;;
			done) text='安装完成。请打开上面显示的初始化链接，创建管理员账号。' ;;
		esac
	else
		case "$key" in
			need_root) text='Please run this installer as root (for example with sudo).' ;;
			need_systemd) text='Cairn needs systemd, but this system was not booted with systemd.' ;;
			unsupported_os) text='Unsupported system: %s. Supported: Debian 12+, Ubuntu 22.04+, RHEL/Rocky/AlmaLinux 8+.' ;;
			unsupported_arch) text='Unsupported CPU architecture: %s. Supported: x86_64 and aarch64.' ;;
			bad_version) text='Invalid version: %s (expected X.Y.Z).' ;;
			no_keys) text='This installer has no signing key built in; download it from the official Cairn release page.' ;;
			no_repo) text='No package repository is configured; pass --repo.' ;;
			http_warning) text='Warning: downloads use HTTP. Files are still signature-checked, but HTTPS is recommended.' ;;
			no_download) text='curl or wget is needed to download Cairn.' ;;
			download_failed) text='Download failed: %s' ;;
			installing_openssl) text='Installing openssl to check the release signature...' ;;
			no_verifier) text='Cannot check the release signature: OpenSSL 3 is needed (on EL 8, install openssl3 from EPEL), or use --with-repo.' ;;
			bad_signature) text='The release files are not signed by a trusted key; aborting. Nothing was installed.' ;;
			no_package) text='The release has no package for this system (%s, %s).' ;;
			bad_checksum) text='The downloaded package does not match the signed checksum; aborting. Nothing was installed.' ;;
			key_mismatch) text='Signing key fingerprint mismatch (got %s, expected %s); aborting.' ;;
			detected) text='Detected system: %s (%s)' ;;
			choose_lang) text='Choose the panel default language (you can change it in the panel at any time):' ;;
			lang_prompt) text='Enter a number [%s]: ' ;;
			fetching) text='Fetching release information...' ;;
			verified) text='Signature verified: %s' ;;
			adding_repo) text='Adding the Cairn package repository...' ;;
			installing) text='Installing Cairn %s...' ;;
			done) text='Done. Open the setup link shown above to create the administrator account.' ;;
		esac
	fi
	# shellcheck disable=SC2059 # the message is the format string
	printf "$text" "$@"
}

say() {
	msg "$@"
	echo
}

die() {
	msg "$@" >&2
	echo >&2
	exit 1
}

# ---- Checks ----------------------------------------------------------------
[ "$(id -u)" -eq 0 ] || die need_root
[ "$CHECK_SYSTEMD" -eq 0 ] || [ -d /run/systemd/system ] || die need_systemd

if [ "$MODE" = repo ]; then
	[ -n "$REPO_URL" ] && [ "$REPO_URL" != "@REPO_URL@" ] || die no_repo
	BASE_URL=${REPO_URL%/}
else
	[ -n "$TRUSTED_KEYS" ] || die no_keys
	WANT_VERSION=${WANT_VERSION#v}
	if [ -n "$WANT_VERSION" ] &&
		! printf '%s\n' "$WANT_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
		die bad_version "$WANT_VERSION"
	fi
	BASE_URL=${RELEASES_URL%/}
fi
case "$BASE_URL" in http://*) say http_warning ;; esac

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
say detected "${PRETTY_NAME:-$ID}" "$arch"

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

# ---- Helpers ---------------------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# apt-get reads local packages as its unprivileged _apt user.
chmod 0755 "$tmp"

fetch() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 "$1" -o "$2" || die download_failed "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1" || die download_failed "$1"
	else
		die no_download
	fi
}

# ---- Release mode ----------------------------------------------------------

# An OpenSSL that can check ed25519 signatures (3.0 and later).
find_openssl() {
	for o in openssl openssl3; do
		if command -v "$o" >/dev/null 2>&1 && "$o" pkeyutl -help 2>&1 | grep -q -- '-rawin'; then
			echo "$o"
			return 0
		fi
	done
	return 1
}

# verify_signature FILE: FILE.sig is "<key-id> <base64 signature>"; the key ID
# is the first 8 bytes of SHA-256 over the raw public key, in hex.
verify_signature() {
	read -r sig_id sig_b64 <"$1.sig" || return 1
	for k in $TRUSTED_KEYS; do
		id=$(printf '%s' "$k" | base64 -d | sha256sum | cut -c1-16)
		[ "$id" = "$sig_id" ] || continue
		# SubjectPublicKeyInfo of an ed25519 key: a fixed 12-byte DER prefix
		# (base64 "MCowBQYDK2VwAyEA") followed by the 32-byte key.
		printf '%s\n' '-----BEGIN PUBLIC KEY-----' "MCowBQYDK2VwAyEA$k" '-----END PUBLIC KEY-----' >"$tmp/key.pem"
		printf '%s' "$sig_b64" | base64 -d >"$tmp/sig.bin" 2>/dev/null || return 1
		out=$("$OPENSSL" pkeyutl -verify -pubin -inkey "$tmp/key.pem" -rawin -in "$1" -sigfile "$tmp/sig.bin" 2>&1) || return 1
		case "$out" in *"Signature Verified Successfully"*) return 0 ;; esac
		return 1
	done
	return 1
}

install_from_release() {
	if ! OPENSSL=$(find_openssl); then
		if ! command -v openssl >/dev/null 2>&1; then
			say installing_openssl
			if [ "$family" = deb ]; then
				DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null
				DEBIAN_FRONTEND=noninteractive apt-get install -y -qq openssl >/dev/null
			else
				dnf install -y -q openssl >/dev/null
			fi
		fi
		OPENSSL=$(find_openssl) || die no_verifier
	fi

	if [ -n "$WANT_VERSION" ]; then
		rel_url="$BASE_URL/download/v$WANT_VERSION"
	else
		rel_url="$BASE_URL/latest/download"
	fi
	say fetching
	fetch "$rel_url/SHA256SUMS" "$tmp/SHA256SUMS"
	fetch "$rel_url/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig"
	verify_signature "$tmp/SHA256SUMS" || die bad_signature
	say verified SHA256SUMS

	if [ "$family" = deb ]; then
		case "$arch" in x86_64) suffix='_amd64\.deb' ;; *) suffix='_arm64\.deb' ;; esac
	else
		case "$arch" in x86_64) suffix='\.x86_64\.rpm' ;; *) suffix='\.aarch64\.rpm' ;; esac
	fi
	line=$(grep -E "^[0-9a-f]{64}  cairn[-_][A-Za-z0-9.+~_-]*$suffix\$" "$tmp/SHA256SUMS" || true)
	[ -n "$line" ] && [ "$(printf '%s\n' "$line" | wc -l)" -eq 1 ] || die no_package "$family" "$arch"
	sum=${line%% *}
	name=${line##* }
	version=$(printf '%s\n' "$name" | sed -E 's/^cairn[-_]//; s/[-_](1\.)?(amd64|arm64|x86_64|aarch64)\.(deb|rpm)$//; s/-1$//')
	# The signed list could belong to another release (a mirror serving the
	# wrong directory): the package must be the version that was asked for.
	if [ -n "$WANT_VERSION" ] && [ "$version" != "$WANT_VERSION" ] &&
		[ "$version" != "$(printf '%s' "$WANT_VERSION" | tr - '~')" ]; then
		die no_package "$family" "$arch"
	fi

	fetch "$rel_url/$name" "$tmp/$name"
	printf '%s  %s\n' "$sum" "$tmp/$name" | sha256sum -c --status || die bad_checksum
	chmod 0644 "$tmp/$name"

	say installing "$version"
	if [ "$family" = deb ]; then
		CAIRN_LANG=$CHOSEN_LANG DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$tmp/$name"
	else
		CAIRN_LANG=$CHOSEN_LANG dnf install -y "$tmp/$name"
	fi

	# Keep the package so that a failed online update can roll back to it.
	updates=/var/lib/cairn-helper/updates
	install -d -m 0700 /var/lib/cairn-helper "$updates"
	old=$(cat "$updates/current" 2>/dev/null || true)
	install -m 0600 "$tmp/$name" "$updates/$name"
	printf '%s\n' "$name" >"$updates/current"
	for f in "$updates"/*.deb "$updates"/*.rpm; do
		[ -e "$f" ] || continue
		b=${f##*/}
		[ "$b" = "$name" ] || [ "$b" = "$old" ] || rm -f "$f"
	done
}

# ---- Repository mode -------------------------------------------------------

check_fingerprint() { # keyfile
	case "$KEY_FINGERPRINT" in "" | "@KEY_FINGERPRINT@") return 0 ;; esac
	command -v gpg >/dev/null 2>&1 || return 0
	got=$(gpg --show-keys --with-colons "$1" 2>/dev/null | awk -F: '/^fpr:/ {print $10; exit}')
	[ "$got" = "$KEY_FINGERPRINT" ] || die key_mismatch "${got:-?}" "$KEY_FINGERPRINT"
}

install_from_repo() {
	say adding_repo
	if [ "$family" = deb ]; then
		install -d -m 0755 /etc/apt/keyrings
		fetch "$BASE_URL/cairn-archive-keyring.pgp" /etc/apt/keyrings/cairn.pgp.new
		check_fingerprint /etc/apt/keyrings/cairn.pgp.new
		chmod 0644 /etc/apt/keyrings/cairn.pgp.new
		mv /etc/apt/keyrings/cairn.pgp.new /etc/apt/keyrings/cairn.pgp
		cat >/etc/apt/sources.list.d/cairn.sources <<EOF
Types: deb
URIs: $BASE_URL/apt
Suites: stable
Components: main
Signed-By: /etc/apt/keyrings/cairn.pgp
EOF
		export DEBIAN_FRONTEND=noninteractive
		apt-get update
		say installing "(repository)"
		CAIRN_LANG=$CHOSEN_LANG apt-get install -y cairn
	else
		# Fetch and check the key ourselves instead of letting dnf download it.
		install -d -m 0755 /etc/pki/rpm-gpg
		fetch "$BASE_URL/cairn-archive-keyring.asc" /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
		check_fingerprint /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
		chmod 0644 /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new
		mv /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn.new /etc/pki/rpm-gpg/RPM-GPG-KEY-cairn
		cat >/etc/yum.repos.d/cairn.repo <<EOF
[cairn]
name=Cairn
baseurl=$BASE_URL/rpm/\$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-cairn
EOF
		say installing "(repository)"
		CAIRN_LANG=$CHOSEN_LANG dnf install -y cairn
	fi
}

if [ "$MODE" = repo ]; then
	install_from_repo
else
	install_from_release
fi

echo
say done
