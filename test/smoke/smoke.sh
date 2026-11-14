#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside a throwaway container of one target distribution (see run.sh).
# Installs the package and exercises the first-run flow end to end without
# systemd as PID 1: the root helper and the web server are started by hand,
# the administrator is created through the setup link printed by the package,
# and the API is driven with curl. Finally the package is removed.
set -euo pipefail

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
grep -q "https://.*/setup?token=" /tmp/install.log || fail "setup link not printed by the package"
token=$(grep -o 'token=[A-Za-z0-9_-]*' /tmp/install.log | head -1 | cut -d= -f2)
step "setup link printed"

[ "$(stat -c '%U:%G %a' /etc/cairn/config.toml)" = "root:cairn 640" ] || fail "config ownership/mode"
[ "$(stat -c '%U:%G %a' /var/lib/cairn)" = "cairn:cairn 700" ] || fail "data dir ownership/mode"
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

api "$base/api/v1/meta" | grep -q '"setupRequired":true' || fail "meta before setup"
out=$(api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"smoke-test-password-1\",\"locale\":\"$INSTALL_LANG\"}")
echo "$out" | grep -q '"username":"admin"' || fail "setup: $out"
grep -q '#HttpOnly_127.0.0.1.*TRUE.*__Host-cairn_session' "$jar" || fail "session cookie must be HttpOnly and Secure"
step "administrator created through the setup link"

api "$base/api/v1/system/overview" | grep -q '"cpuCores":[1-9]' || fail "overview"
headers=$(curl -sk -D - -o /dev/null "$base/")
echo "$headers" | grep -qi '^content-security-policy:' || fail "CSP header missing"
echo "$headers" | grep -qi '^x-frame-options: DENY' || fail "X-Frame-Options missing"
step "overview served, security headers present"

code=$(api -o /dev/null -w '%{http_code}' -X PUT "$base/api/v1/settings/default-locale" -d "{\"locale\":\"$OTHER_LANG\"}")
[ "$code" = 204 ] || fail "default-locale via helper returned $code"
grep -qE "default_locale = ['\"]$OTHER_LANG['\"]" /etc/cairn/config.toml || fail "helper did not write the config"
[ "$(stat -c '%U:%G %a' /etc/cairn/config.toml)" = "root:cairn 640" ] || fail "config ownership changed by helper"
api "$base/api/v1/meta" | grep -q "\"defaultLocale\":\"$OTHER_LANG\"" || fail "meta default locale"
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
api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$newpw\"}" | grep -q '"username":"admin"' || fail "login with reset password"
step "cairn reset-password works, old password rejected"

newop='{"username":"ops","password":"smoke-operator-password","role":"operator"}'
api -X POST "$base/api/v1/users" -d "$newop" | grep -q '"auth.step_up_required"' || fail "creating a user without step-up"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$newpw\"}" | grep -q '"elevatedUntil"' || fail "elevate"
api -X POST "$base/api/v1/users" -d "$newop" | grep -q '"role":"operator"' || fail "create operator"
opjar=/tmp/opjar
curl -sk -c "$opjar" -b "$opjar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' -X POST "$base/api/v1/auth/login" \
	-d '{"username":"ops","password":"smoke-operator-password"}' | grep -q '"system.read"' || fail "operator login"
curl -sk -b "$opjar" "$base/api/v1/users" | grep -q '"auth.forbidden"' || fail "operator reached user management"
api "$base/api/v1/audit?action=users.create" | grep -q '"username":"ops"' || fail "audit entry for users.create"
step "roles enforced: step-up for user management, operator denied, change audited"

tok=$(api -X POST "$base/api/v1/me/tokens" -d '{"name":"smoke","permissions":["system.read"],"expiresInDays":1}' | sed -n 's/.*"token":"\(cairn_[A-Za-z0-9_-]*\)".*/\1/p')
[ -n "$tok" ] || fail "create API token"
curl -sk -H "Authorization: Bearer $tok" "$base/api/v1/system/overview" | grep -q '"cpuCores"' || fail "token within scope"
curl -sk -H "Authorization: Bearer $tok" "$base/api/v1/users" | grep -q '"auth.forbidden"' || fail "token outside scope"
api "$base/api/v1/openapi.json" | grep -q '"openapi":"3.1.0"' || fail "openapi document"
step "API token scoped to system.read works; OpenAPI document served"

cairn reset-2fa --username admin --lang en | grep -q "turned off" || fail "cairn reset-2fa"
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
	units="/usr/lib/systemd/system/cairn-helper.socket /usr/lib/systemd/system/cairn-helper.service /usr/lib/systemd/system/cairn-web.service"
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

if [ "$family" = deb ]; then
	apt-get purge -y cairn >/dev/null 2>&1 || fail "apt purge"
	[ ! -e /usr/bin/cairn-web ] && [ ! -e /etc/cairn ] && [ ! -e /var/lib/cairn ] || fail "purge left files behind"
	step "purged: binaries, configuration and data removed"
else
	dnf remove -y cairn >/dev/null 2>&1 || fail "dnf remove"
	[ ! -e /usr/bin/cairn-web ] || fail "remove left binaries behind"
	step "removed: binaries gone (RPM keeps /etc/cairn and /var/lib/cairn by design)"
fi
echo "== PASS: ${PRETTY_NAME}"
