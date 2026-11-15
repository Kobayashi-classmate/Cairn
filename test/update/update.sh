#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside a throwaway container of one distribution (see run.sh), with
# the test releases served at http://cairn-rel/releases. Installs 0.1.0 with
# the published install.sh, updates to 0.2.0 through the panel API, then
# tries 0.3.0, whose installation fails, and checks the rollback to 0.2.0.
# There is no systemd here: the helper runs the update in-process and the
# services are restarted by hand.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input: with pipefail, grep -q
# stopping at the first match can kill the writer with SIGPIPE and fail the
# whole pipeline at random.
grep_q() { grep "$@" >/dev/null; }

. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	for f in /tmp/install.log /tmp/helper.log /tmp/web.log /var/lib/cairn-helper/update-status.json; do
		[ -f "$f" ] && { echo "--- $f" >&2; tail -30 "$f" >&2; }
	done
	exit 1
}
echo "== ${PRETTY_NAME}"

if command -v apt-get >/dev/null; then
	family=deb
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq >/dev/null
	apt-get install -y -qq curl procps >/dev/null
else
	family=rpm
	command -v curl >/dev/null || dnf install -y -q curl >/dev/null
	command -v ps >/dev/null || dnf install -y -q procps-ng >/dev/null
	command -v runuser >/dev/null || dnf install -y -q util-linux >/dev/null
fi

# is_version V: exactly version V of cairn is installed and fully configured.
is_version() {
	if [ "$family" = deb ]; then
		# Package release "1" (packaging/nfpm.yaml).
		[ "$(dpkg-query -W -f '${Version} ${db:Status-Abbrev}' cairn 2>/dev/null)" = "$1-1 ii " ]
	else
		[ "$(rpm -q --qf '%{VERSION}\n' cairn 2>/dev/null)" = "$1" ]
	fi
}
updates=/var/lib/cairn-helper/updates
current_is() { case "$(cat $updates/current)" in *"$1"*) [ -f "$updates/$(cat $updates/current)" ] ;; *) return 1 ;; esac; }

curl -fsSL http://cairn-rel/releases/latest/download/install.sh -o /tmp/install.sh
sh /tmp/install.sh --mirror http://cairn-rel/releases --lang en --yes --no-systemd-check >/tmp/install.log 2>&1 ||
	fail "install.sh"
is_version 0.1.0 || fail "0.1.0 is not installed"
grep -q "Signature verified: SHA256SUMS" /tmp/install.log || fail "signature check not reported"
[ ! -e /etc/apt/sources.list.d/cairn.sources ] && [ ! -e /etc/yum.repos.d/cairn.repo ] || fail "a package repository was added"
[ "$(stat -c '%U %a' /var/lib/cairn-helper)" = "root 700" ] || fail "helper state dir ownership/mode"
current_is 0.1.0 || fail "installed package not kept for rollback"
[ "$(stat -c '%a' "$updates/$(cat $updates/current)")" = 600 ] || fail "kept package mode"
step "install.sh: 0.1.0 from the signed release, no repository added, package kept for rollback"

cat >>/etc/cairn/config.toml <<EOF

[cloud]
enabled = true
base_url = "http://cairn-rel/releases/download/feed"
channel = "stable"
trusted_keys = ["$TRUSTED_KEY"]
EOF

port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
start() {
	/usr/lib/cairn/cairn-helper >>/tmp/helper.log 2>&1 &
	for _ in $(seq 50); do [ -S /run/cairn/helper.sock ] && break; sleep 0.1; done
	runuser -u cairn -- /usr/bin/cairn-web >>/tmp/web.log 2>&1 &
	for _ in $(seq 100); do curl -sk -o /dev/null "$base/api/v1/meta" && return 0; sleep 0.1; done
	fail "services did not start"
}
stop() {
	pkill -f /usr/bin/cairn-web || true
	pkill -f /usr/lib/cairn/cairn-helper || true
	for _ in $(seq 50); do pgrep -f 'cairn-(web|helper)' >/dev/null || break; sleep 0.1; done
	rm -f /run/cairn/helper.sock
}
jar=/tmp/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
pw=update-test-password-1
login() {
	rm -f "$jar"
	api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | grep_q '"username":"admin"' || fail "login"
	api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
}
# wait_job: prints the final state of the update job.
wait_job() {
	for _ in $(seq 300); do
		s=$(api "$base/api/v1/updates" | sed -n 's/.*"job":{"state":"\([a-z]*\)".*/\1/p')
		case "$s" in succeeded | failed) echo "$s"; return ;; esac
		sleep 1
	done
	echo timeout
}
apply() { api -o /tmp/apply.json -w '%{http_code}' -X POST "$base/api/v1/updates/apply" -d "{\"version\":\"$1\"}"; }

start
token=$(grep -o 'token=[A-Za-z0-9_-]*' /tmp/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" |
	grep -q '"username":"admin"' || fail "setup"
login

r=$(api -X POST "$base/api/v1/updates/check")
echo "$r" | grep_q '"status":"available"' || fail "no update offered: $r"
echo "$r" | grep_q '"current":"0.1.0"' && echo "$r" | grep_q '"latest":{"version":"0.3.0"' || fail "update check: $r"
step "update check: running 0.1.0, latest is 0.3.0 (signed index verified)"

code=$(apply 0.2.0)
[ "$code" = 202 ] || fail "apply 0.2.0 returned $code: $(cat /tmp/apply.json)"
[ "$(wait_job)" = succeeded ] || fail "update to 0.2.0 did not succeed"
is_version 0.2.0 || fail "0.2.0 is not installed"
current_is 0.2.0 || fail "rollback pointer not moved to 0.2.0"
ls $updates | grep_q 0.1.0 || fail "previous package not kept"
step "online update to 0.2.0 installed; 0.1.0 kept as the rollback package"

stop
start
api "$base/api/v1/meta" | grep_q '"version":"0.2.0"' || fail "restarted panel is not 0.2.0"
login
step "restarted: the panel now runs 0.2.0"

code=$(apply 0.3.0)
[ "$code" = 202 ] || fail "apply 0.3.0 returned $code: $(cat /tmp/apply.json)"
[ "$(wait_job)" = failed ] || fail "the uninstallable 0.3.0 did not fail"
api "$base/api/v1/updates" | grep_q 'the previous version was restored' || fail "rollback not reported"
is_version 0.2.0 || fail "0.2.0 not restored after the failed update"
current_is 0.2.0 || fail "rollback pointer moved by a failed update"
step "update to 0.3.0 failed (missing dependency) and 0.2.0 was restored"

audit=$(api "$base/api/v1/audit?action=update.finish")
echo "$audit" | grep_q '"result":"ok"' && echo "$audit" | grep_q '"result":"update.install_failed"' ||
	fail "update results not audited: $audit"
[ "$(api "$base/api/v1/audit?action=update.apply" | grep -o '"action":"update.apply"' | wc -l)" -eq 2 ] || fail "update requests not audited"
step "both requests and both outcomes audited"

stop
echo "== PASS: ${PRETTY_NAME}"
