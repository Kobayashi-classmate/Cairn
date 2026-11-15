#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# starts the helper and the web process by hand (no systemd), then installs
# every software@line of $CASES through the API and checks the exact
# installed version; the last entry is removed again.
set -euo pipefail
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	[ -f /tmp/job.log ] && { echo "--- job log" >&2; tail -40 /tmp/job.log >&2; }
	tail -5 /tmp/helper.log >&2 || true
	exit 1
}
echo "== ${PRETTY_NAME}: ${CASES}"

if command -v apt-get >/dev/null; then
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq >/dev/null
	apt-get install -y -qq curl procps python3 >/dev/null
	apt-get install -y /pkgs/cairn_*_amd64.deb >/tmp/install.log 2>&1 || fail "install cairn"
	version_of() { dpkg-query -W -f '${Version}' "$1" 2>/dev/null; }
else
	command -v curl >/dev/null || dnf install -y -q curl >/dev/null
	dnf install -y -q procps-ng util-linux python3 >/dev/null
	dnf install -y /pkgs/cairn-*.x86_64.rpm >/tmp/install.log 2>&1 || fail "install cairn"
	version_of() { rpm -q --qf '%{EVR}' "$1" 2>/dev/null; }
fi
/usr/lib/cairn/cairn-helper >/tmp/helper.log 2>&1 &
for _ in $(seq 50); do [ -S /run/cairn/helper.sock ] && break; sleep 0.1; done
runuser -u cairn -- /usr/bin/cairn-web >/tmp/web.log 2>&1 &
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
for _ in $(seq 100); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 0.1; done
jar=/tmp/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
pw=software-test-password-1
token=$(grep -o 'token=[A-Za-z0-9_-]*' /tmp/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep -q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep -q '"elevatedUntil"' || fail "elevate"

json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# run_job ACTION SOFTWARE LINE: starts the job, follows its log to the end.
run_job() {
	local r id state offset=0
	r=$(api -X POST "$base/api/v1/software/$2/$1" -d "{\"line\":\"$3\"}")
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$1 $2 $3 not started: $r"
	: >/tmp/job.log
	for _ in $(seq 400); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/tmp/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	[ "$state" = succeeded ] || fail "$1 $2 $3 ended $state: $(printf '%s' "$r" | json 'd["job"].get("error")')"
}
# offer SOFTWARE LINE FIELD: a field of the offer (the first item of a list).
offer() {
	api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
o = [o for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["line"] == sys.argv[2]][0]
v = o.get(sys.argv[3])
print(v[0] if isinstance(v, list) else ("" if v is None else v))' "$1" "$2" "$3"
}

entries=($CASES)
last=${entries[-1]}
for c in "${entries[@]::${#entries[@]}-1}"; do
	sw=${c%@*} line=${c#*@}
	t0=$(date +%s)
	run_job install "$sw" "$line"
	[ "$(offer "$sw" "$line" installed)" = True ] || fail "$sw $line not reported installed"
	method=$(offer "$sw" "$line" method)
	if [ "$method" = standalone ]; then
		# The line path points at the exact build; the job already ran it.
		pkg=/opt/cairn/runtimes/$sw/$line
		v=$(readlink "$pkg")
		[ -d "/opt/cairn/runtimes/$sw/$v" ] || fail "$sw $line: $pkg -> $v missing"
	else
		pkg=$(offer "$sw" "$line" packages)
		v=$(version_of "$pkg")
	fi
	case "${v#*:}" in "$line".*) ;; *) fail "$sw $line: $pkg is $v" ;; esac
	repo=$(offer "$sw" "$line" repo)
	keyinfo=""
	grep -q "Key verified" /tmp/job.log && keyinfo=", key verified"
	grep -q "checksum matches" /tmp/job.log && keyinfo=", checksum verified, $(grep -o 'Checked: .*' /tmp/job.log | head -1)"
	step "$sw $line: $pkg $v ($method${repo:+ $repo}$keyinfo) in $(($(date +%s) - t0))s"
done
sw=${last%@*} line=${last#*@}
run_job remove "$sw" "$line"
[ "$(offer "$sw" "$line" installed)" = False ] || fail "$sw $line still reported installed"
[ ! -e "/opt/cairn/runtimes/$sw/$line" ] || fail "$sw $line: runtime files left"
step "$sw $line removed (configuration kept)"
echo "== PASS: ${PRETTY_NAME}"
