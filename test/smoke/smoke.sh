#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside a throwaway container of one target distribution (see run.sh).
# Installs the package and exercises the first-run flow end to end without
# systemd as PID 1: the root helper and the web server are started by hand,
# the administrator is created through a setup link from `cairn setup-link`,
# and the API is driven with curl. Finally the package is removed.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input: with pipefail, grep -q
# stopping at the first match can kill the writer with SIGPIPE and fail the
# whole pipeline at random.
grep_q() { grep "$@" >/dev/null; }

PKGS=/pkgs
INSTALL_LANG=${SMOKE_LANG:-en}
OTHER_LANG=zh-CN
[ "$INSTALL_LANG" = zh-CN ] && OTHER_LANG=en

. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; for f in /tmp/install.log /tmp/helper.log /tmp/web.log; do [ -f "$f" ] && { echo "--- $f" >&2; tail -20 "$f" >&2; }; done; exit 1; }
echo "== ${PRETTY_NAME} (install language: ${INSTALL_LANG})"

if command -v apt-get >/dev/null; then
	family=deb
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq >/dev/null
	apt-get install -y -qq curl procps >/dev/null
	CAIRN_LANG=$INSTALL_LANG apt-get install -y "$PKGS"/cairn_*_amd64.deb >/tmp/install.log 2>&1 || fail "apt install"
else
	family=rpm
	command -v curl >/dev/null || dnf install -y -q curl >/dev/null
	command -v ps >/dev/null || dnf install -y -q procps-ng >/dev/null
	# runuser is only needed by this test to start cairn-web without systemd.
	command -v runuser >/dev/null || dnf install -y -q util-linux >/dev/null
	CAIRN_LANG=$INSTALL_LANG dnf install -y "$PKGS"/cairn-*.x86_64.rpm >/tmp/install.log 2>&1 || fail "dnf install"
fi
step "package installed"

grep -q "useradd: Warning" /tmp/install.log && fail "useradd warned during install"
# The package says how to get the setup link but does not print it (package
# manager logs keep the output); the link comes from the CLI, as install.sh does.
grep -q "token=" /tmp/install.log && fail "the package printed the setup token into the install log"
grep -q "cairn setup-link" /tmp/install.log || fail "the package did not say how to get the setup link"
cairn setup-link >/tmp/setup-link.log 2>&1 || fail "cairn setup-link"
token=$(grep -o 'token=[A-Za-z0-9_-]*' /tmp/setup-link.log | head -1 | cut -d= -f2)
[ -n "$token" ] || fail "no setup link from cairn setup-link"
step "setup hint printed; setup link from the CLI"

[ "$(stat -c '%U:%G %a' /etc/cairn/config.toml)" = "root:cairn 640" ] || fail "config ownership/mode"
[ "$(stat -c '%U:%G %a' /var/lib/cairn)" = "cairn:cairn 700" ] || fail "data dir ownership/mode"
# The account the helper extracts 7z and RAR archives as: no login shell.
getent passwd cairn-unpack | grep -Eq ':(/usr/sbin/nologin|/sbin/nologin|/bin/false)$' || fail "cairn-unpack account"
grep -q "default_locale = \"$INSTALL_LANG\"" /etc/cairn/config.toml || fail "install language not applied"
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
{ [ "$port" -ge 20000 ] && [ "$port" -lt 60000 ]; } || fail "port $port out of range"
step "config written: port $port, ownership and modes correct"

/usr/lib/cairn/cairn-helper >/tmp/helper.log 2>&1 &
for _ in $(seq 50); do [ -S /run/cairn/helper.sock ] && break; sleep 0.1; done
[ "$(stat -c '%U:%G %a' /run/cairn/helper.sock)" = "root:cairn 660" ] || fail "helper socket ownership/mode"
runuser -u cairn -- /usr/bin/cairn-web >/tmp/web.log 2>&1 &
base="https://127.0.0.1:${port}"
for _ in $(seq 100); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 0.1; done
[ "$(ps -o user= -C cairn-web | head -1)" = cairn ] || fail "cairn-web is not running as cairn"
grep -q "root helper reachable" /tmp/web.log || fail "web could not reach the helper"
step "helper (root) and web (cairn) running, socket handshake ok"

jar=/tmp/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }

api "$base/api/v1/meta" | grep_q '"setupRequired":true' || fail "meta before setup"
out=$(api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"smoke-test-password-1\",\"locale\":\"$INSTALL_LANG\"}")
echo "$out" | grep_q '"username":"admin"' || fail "setup: $out"
grep -q '#HttpOnly_127.0.0.1.*TRUE.*__Host-cairn_session' "$jar" || fail "session cookie must be HttpOnly and Secure"
step "administrator created through the setup link"

api "$base/api/v1/system/overview" | grep_q '"cpuCores":[1-9]' || fail "overview"
headers=$(curl -sk -D - -o /dev/null "$base/")
echo "$headers" | grep_q -i '^content-security-policy:' || fail "CSP header missing"
echo "$headers" | grep_q -i '^x-frame-options: DENY' || fail "X-Frame-Options missing"
step "overview served, security headers present"

code=$(api -o /dev/null -w '%{http_code}' -X PUT "$base/api/v1/settings/default-locale" -d "{\"locale\":\"$OTHER_LANG\"}")
[ "$code" = 204 ] || fail "default-locale via helper returned $code"
grep -qE "default_locale = ['\"]$OTHER_LANG['\"]" /etc/cairn/config.toml || fail "helper did not write the config"
[ "$(stat -c '%U:%G %a' /etc/cairn/config.toml)" = "root:cairn 640" ] || fail "config ownership changed by helper"
api "$base/api/v1/meta" | grep_q "\"defaultLocale\":\"$OTHER_LANG\"" || fail "meta default locale"
grep -q 'action=config.set_default_locale result=ok user=admin' /tmp/helper.log || fail "helper audit line"
step "default language changed through the root helper (audited)"

code=$(curl -sk -c "$jar" -b "$jar" -H 'Content-Type: application/json' -o /dev/null -w '%{http_code}' -X PUT "$base/api/v1/me/locale" -d '{"locale":"en"}')
[ "$code" = 403 ] || fail "request without CSRF header returned $code"
step "CSRF header enforced"

newpw=$(cairn reset-password --lang en | sed -n 's/.*New password: //p')
[ -n "$newpw" ] || fail "reset-password printed no password"
rm -f "$jar"
code=$(api -o /dev/null -w '%{http_code}' -X POST "$base/api/v1/auth/login" -d '{"username":"admin","password":"smoke-test-password-1"}')
[ "$code" = 401 ] || fail "old password still works ($code)"
api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$newpw\"}" | grep_q '"username":"admin"' || fail "login with reset password"
step "cairn reset-password works, old password rejected"

newop='{"username":"ops","password":"smoke-operator-password","role":"operator"}'
api -X POST "$base/api/v1/users" -d "$newop" | grep_q '"auth.step_up_required"' || fail "creating a user without step-up"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$newpw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
api -X POST "$base/api/v1/users" -d "$newop" | grep_q '"role":"operator"' || fail "create operator"
opjar=/tmp/opjar
curl -sk -c "$opjar" -b "$opjar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' -X POST "$base/api/v1/auth/login" \
	-d '{"username":"ops","password":"smoke-operator-password"}' | grep_q '"system.read"' || fail "operator login"
curl -sk -b "$opjar" "$base/api/v1/users" | grep_q '"auth.forbidden"' || fail "operator reached user management"
api "$base/api/v1/audit?action=users.create" | grep_q '"username":"ops"' || fail "audit entry for users.create"
step "roles enforced: step-up for user management, operator denied, change audited"

tok=$(api -X POST "$base/api/v1/me/tokens" -d '{"name":"smoke","permissions":["system.read"],"expiresInDays":1}' | sed -n 's/.*"token":"\(cairn_[A-Za-z0-9_-]*\)".*/\1/p')
[ -n "$tok" ] || fail "create API token"
curl -sk -H "Authorization: Bearer $tok" "$base/api/v1/system/overview" | grep_q '"cpuCores"' || fail "token within scope"
curl -sk -H "Authorization: Bearer $tok" "$base/api/v1/users" | grep_q '"auth.forbidden"' || fail "token outside scope"
api "$base/api/v1/openapi.json" | grep_q '"openapi":"3.1.0"' || fail "openapi document"
step "API token scoped to system.read works; OpenAPI document served"

api "$base/api/v1/ui/nav" | grep_q '"key":"software"' || fail "menu from the modules"
api "$base/api/v1/software" | grep_q '"supported":true' || fail "software catalog not resolved for this system"
job=$(api -X POST "$base/api/v1/software/refresh" | sed -n 's/.*"job":{"id":"\([0-9a-f]*\)".*/\1/p')
[ -n "$job" ] || fail "refresh job not started"
state=""
for _ in $(seq 150); do
	state=$(api "$base/api/v1/jobs/$job" | grep -o '"state":"[a-z]*"' | head -1 | cut -d'"' -f4)
	case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	sleep 2
done
[ "$state" = succeeded ] || fail "package index refresh job ended '$state'"
api "$base/api/v1/jobs/$job/log" | grep_q -E '\$ (apt-get update|dnf makecache)' || fail "job log"
step "menu from the enabled modules; catalog resolved; package index refresh ran as a helper operation"

# Something in the backups root, made through the helper, for the purge
# below to keep. Changes outside the sites root take a recent step-up, and
# the package index refresh above may have outlasted the last one.
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$newpw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
api -X POST "$base/api/v1/files/touch" -d '{"root":"backups","dir":"","name":"note.txt"}' | grep_q '"name":"note.txt"' || fail "file in the backups root"
step "file made in the backups root through the helper"

cairn reset-2fa --username admin --lang en | grep_q "turned off" || fail "cairn reset-2fa"
[ "$(stat -c '%U:%G %a' /var/lib/cairn/master.key)" = "cairn:cairn 600" ] || fail "master key ownership/mode"
step "cairn reset-2fa works; master key is cairn-only (0600)"

pkill -f /usr/bin/cairn-web || true
pkill -f /usr/lib/cairn/cairn-helper || true
sleep 0.5

if [ "${SMOKE_SYSTEMD_VERIFY:-0}" = 1 ]; then
	if ! command -v systemd-analyze >/dev/null; then
		if [ "$family" = deb ]; then
			apt-get install -y -qq systemd >/tmp/systemd-install.log 2>&1 || fail "installing systemd for verification"
		else
			dnf install -y -q systemd >/tmp/systemd-install.log 2>&1 || fail "installing systemd for verification"
		fi
	fi
	# The nftables units (firewall, port forwards on ufw systems) name
	# /usr/sbin/nft, which verify wants to exist.
	if ! command -v nft >/dev/null; then
		if [ "$family" = deb ]; then
			apt-get install -y -qq nftables >/tmp/nft-install.log 2>&1 || fail "installing nftables for verification"
		else
			dnf install -y -q nftables >/tmp/nft-install.log 2>&1 || fail "installing nftables for verification"
		fi
	fi
	units="/usr/lib/systemd/system/cairn-helper.socket /usr/lib/systemd/system/cairn-helper.service /usr/lib/systemd/system/cairn-web.service"
	units="$units /usr/lib/systemd/system/cairn-firewall.service /usr/lib/systemd/system/cairn-forward.service"
	# No output means no findings; a container without systemd as PID 1 adds
	# harmless working-directory noise, which is filtered out.
	# shellcheck disable=SC2086
	systemd-analyze verify $units >/tmp/verify.raw 2>&1 || true
	grep -v 'Failed to setup working directory' /tmp/verify.raw >/tmp/verify.log || true
	if [ -s /tmp/verify.log ]; then
		cat /tmp/verify.log >&2
		fail "systemd-analyze verify reported problems"
	fi
	exposure=$(systemd-analyze security --offline=yes /usr/lib/systemd/system/cairn-web.service 2>/dev/null | tail -1 || true)
	step "systemd units verified (systemd $(systemd-analyze --version | head -1 | awk '{print $2}')); cairn-web ${exposure#→ }"
fi

# Backups as the helper leaves them: a database dump, a backup plan's
# local copy, and empty directories made on first use.
b=/var/lib/cairn/backups
runuser -u cairn -- mkdir -p $b/db/mysql $b/db/redis $b/plans/p1 $b/plans/p2
runuser -u cairn -- sh -c "echo dump >$b/db/mysql/shop.sql.gz && echo plan >$b/plans/p1/dir_x.tar.zst"

# What remove and purge leave is listed in README.md ("Uninstall").
if [ "$family" = deb ]; then
	apt-get remove -y cairn >/tmp/remove.log 2>&1 || fail "apt remove"
	[ ! -e /usr/bin/cairn-web ] && [ -f /etc/cairn/config.toml ] && [ -f /var/lib/cairn/cairn.db ] && [ -d /var/lib/cairn-helper ] || fail "remove did not keep the data"
	grep_q 'configuration and data were kept' /tmp/remove.log || fail "remove did not say what it kept"
	step "removed: binaries gone, configuration and data kept and reported"

	apt-get purge -y cairn >/tmp/purge.log 2>&1 || fail "apt purge"
	[ ! -e /etc/cairn ] && [ ! -e /var/lib/cairn-helper ] && [ ! -e /var/lib/cairn-unpack ] || fail "purge left files behind"
	[ "$(ls -A /var/lib/cairn)" = backups ] || fail "purge kept more than the backups: $(ls -A /var/lib/cairn)"
	[ "$(cd $b && find . -mindepth 1 | sort | tr '\n' ' ')" = "./db ./db/mysql ./db/mysql/shop.sql.gz ./note.txt ./plans ./plans/p1 ./plans/p1/dir_x.tar.zst " ] ||
		fail "purge did not keep exactly the backups: $(cd $b && find . -mindepth 1 | sort | tr '\n' ' ')"
	grep_q "kept in $b" /tmp/purge.log || fail "purge did not say where the backups are"
	step "purged: binaries, configuration and data removed; backups kept and reported"

	# Installed again with a new cairn account (another uid): the kept
	# backups change hands, links in them are not followed, and files of
	# other owners stay theirs.
	old_uid=$(id -u cairn)
	userdel cairn
	if getent group cairn >/dev/null; then groupdel cairn; fi
	useradd --system --no-create-home --uid "$old_uid" cairn-old
	echo secret >/root/target
	ln -s /root/target $b/db/link
	chown -h cairn-old: $b/db/link
	touch $b/rootfile
	CAIRN_LANG=$INSTALL_LANG apt-get install -y "$PKGS"/cairn_*_amd64.deb >/tmp/reinstall.log 2>&1 || fail "apt install after purge"
	[ "$(id -u cairn)" != "$old_uid" ] || fail "the test did not change the cairn uid"
	[ -z "$(find /var/lib/cairn -user "$old_uid")" ] || fail "files left to the old uid: $(find /var/lib/cairn -user "$old_uid")"
	[ "$(stat -c %U /root/target)" = root ] && [ "$(stat -c %U $b/rootfile)" = root ] || fail "ownership changed outside the old account's files"
	runuser -u cairn -- cat $b/db/mysql/shop.sql.gz | grep_q dump || fail "the panel cannot read a kept dump"
	runuser -u cairn -- ls $b/plans/p1 | grep_q dir_x.tar.zst || fail "the panel cannot read a kept backup"
	step "installed again with another cairn uid: kept backups taken over, links not followed"

	CAIRN_PURGE_BACKUPS=1 apt-get purge -y cairn >/tmp/purge2.log 2>&1 || fail "apt purge with CAIRN_PURGE_BACKUPS=1"
	[ ! -e /var/lib/cairn ] && [ ! -e /etc/cairn ] && [ -f /root/target ] || fail "CAIRN_PURGE_BACKUPS=1 did not delete the backups"
	grep_q 'were deleted (CAIRN_PURGE_BACKUPS=1)' /tmp/purge2.log || fail "purge did not say the backups were deleted"
	step "purged with CAIRN_PURGE_BACKUPS=1: backups deleted too"
else
	dnf remove -y cairn >/tmp/remove.log 2>&1 || fail "dnf remove"
	[ ! -e /usr/bin/cairn-web ] || fail "remove left binaries behind"
	[ -f /etc/cairn/config.toml ] && [ -f /var/lib/cairn/cairn.db ] && [ -f $b/db/mysql/shop.sql.gz ] || fail "remove did not keep the data"
	grep_q 'configuration and data were kept' /tmp/remove.log || fail "remove did not say what it kept"
	step "removed: binaries gone, configuration and data kept and reported (RPM has no purge)"
fi
echo "== PASS: ${PRETTY_NAME}"
