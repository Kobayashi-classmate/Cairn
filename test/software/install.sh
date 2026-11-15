#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# starts the helper and the web process by hand (no systemd), then probes the
# download sources of every software@line[#source] of $CASES, installs it
# through the API (from the given source, for runtimes) and checks the exact
# installed version; the last entry is removed again.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input: with pipefail, grep -q
# stopping at the first match can kill the writer with SIGPIPE and fail the
# whole pipeline at random.
grep_q() { grep "$@" >/dev/null; }
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	[ -f /tmp/job.log ] && { echo "--- job log" >&2; tail -40 /tmp/job.log >&2; }
	tail -5 /tmp/helper.log >&2 || true
	exit 1
}
echo "== ${PRETTY_NAME}: ${CASES}"
# A command failing outside fail() still says where and shows the logs.
trap 'fail "line $LINENO: $BASH_COMMAND"; tail -5 /tmp/web.log >&2 || true' ERR

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
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep_q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"

json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# items_json "sw@line[#source] ...": the items of a probe or set install.
items_json() {
	python3 -c '
import json, sys
out = []
for c in sys.argv[1:]:
    c, _, src = c.partition("#")
    sw, _, line = c.partition("@")
    out.append(dict(software=sw, line=line, **({"source": src} if src else {})))
print(json.dumps({"items": out}))' "$@"
}
# probe ENTRY...: checks the download sources of the entries in one probe,
# prints each slot (* marks the source in use) and fails when a runtime
# download has no usable source or a requested one is not among them.
probe() {
	api -X POST "$base/api/v1/software/probe" -d "$(items_json "${@%%#*}")" >/tmp/probe.json
	python3 /t/probe_summary.py /tmp/probe.json "$@" || fail "probe $*: $(head -c 800 /tmp/probe.json)"
}
# follow_job PATH BODY: starts a job, follows its log to the end.
follow_job() {
	local r id state offset=0
	r=$(api -X POST "$base$1" -d "$2")
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$1 not started: $r"
	: >/tmp/job.log
	for _ in $(seq 400); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/tmp/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	[ "$state" = succeeded ] || fail "$1 ended $state: $(printf '%s' "$r" | json 'd["job"].get("error")')"
}
# run_job ACTION SOFTWARE LINE [SOURCE]
run_job() {
	local body="{\"line\":\"$3\"}"
	[ -n "${4:-}" ] && body="{\"line\":\"$3\",\"source\":\"$4\"}"
	follow_job "/api/v1/software/$2/$1" "$body"
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
# check_line SOFTWARE LINE SOURCE SECONDS: the line is installed in its
# exact version (from SOURCE, for a runtime); prints one step.
check_line() {
	local sw=$1 line=$2 src=$3 took=$4 method pkg v used repo keyinfo="" from=""
	[ "$(offer "$sw" "$line" installed)" = True ] || fail "$sw $line not reported installed"
	method=$(offer "$sw" "$line" method)
	if [ "$method" = standalone ]; then
		# The line path points at the exact build; the job already ran it.
		pkg=/opt/cairn/runtimes/$sw/$line
		v=$(readlink "$pkg")
		[ -d "/opt/cairn/runtimes/$sw/$v" ] || fail "$sw $line: $pkg -> $v missing"
		used=$(json 'd.get("source","")' <"/opt/cairn/runtimes/$sw/$v.json")
		[ -z "$src" ] || [ "$used" = "$src" ] || fail "$sw $line: downloaded from $used, not $src"
		from=", from $used"
	else
		pkg=$(offer "$sw" "$line" packages)
		v=$(version_of "$pkg")
	fi
	case "${v#*:}" in "$line".*) ;; *) fail "$sw $line: $pkg is $v" ;; esac
	repo=$(offer "$sw" "$line" repo)
	grep -q "Key verified" /tmp/job.log && keyinfo=", key verified"
	[ "$method" = standalone ] && grep -q "checksum matches" /tmp/job.log && keyinfo=", checksum verified"
	step "$sw $line: $pkg $v ($method${repo:+ $repo}$keyinfo$from)${took:+ in ${took}s}"
}

entries=($CASES)
last=${entries[-1]}
for c in "${entries[@]::${#entries[@]}-1}"; do
	t0=$(date +%s)
	if [[ $c == set:* ]]; then
		# set:a@1,b@2#source: the wizard's way, one probe and one job.
		IFS=, read -ra set <<<"${c#set:}"
		probe "${set[@]}"
		follow_job /api/v1/software/install "$(items_json "${set[@]}")"
		step "set of ${#set[@]} installed in $(($(date +%s) - t0))s ($(grep -c '^\$ apt-get install\|^\$ dnf install' /tmp/job.log) package manager install)"
		for e in "${set[@]}"; do
			src=""
			case $e in *"#"*) src=${e#*#} e=${e%%#*} ;; esac
			check_line "${e%@*}" "${e#*@}" "$src" ""
		done
		continue
	fi
	src=""
	case $c in *"#"*) src=${c#*#} c=${c%%#*} ;; esac
	sw=${c%@*} line=${c#*@}
	probe "$c${src:+#$src}"
	run_job install "$sw" "$line" "$src"
	check_line "$sw" "$line" "$src" "$(($(date +%s) - t0))"
done
sw=${last%@*} line=${last#*@}
run_job remove "$sw" "$line"
[ "$(offer "$sw" "$line" installed)" = False ] || fail "$sw $line still reported installed"
[ ! -e "/opt/cairn/runtimes/$sw/$line" ] || fail "$sw $line: runtime files left"
step "$sw $line removed (configuration kept)"
echo "== PASS: ${PRETTY_NAME}"
