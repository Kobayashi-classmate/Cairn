#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# starts the helper and the web process by hand (no systemd), then probes the
# download sources of every software@line[#source] of $CASES, installs it
# through the API (from the given source, for runtimes) and checks the exact
# installed version; the last entry is removed again. "ext:php@8.4:a,b"
# installs PHP extensions and removes the first again; "cfg:sw@line" edits
# every configuration file of a line through the API.
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
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
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
# run_api_job METHOD PATH BODY: starts a job and follows its log to the end;
# sets job_state and job_error (the error code, "" when none).
run_api_job() {
	local r id offset=0
	r=$(api -X "$1" "$base$2" -d "$3")
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$2 not started: $(printf '%s' "$r" | head -c 600)"
	: >/tmp/job.log
	for _ in $(seq 400); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/tmp/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		job_state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$job_state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	job_error=$(printf '%s' "$r" | json '(d["job"].get("error") or {}).get("code","")')
}
# follow_job PATH BODY: a POST job that must succeed.
follow_job() {
	run_api_job POST "$1" "$2"
	[ "$job_state" = succeeded ] || fail "$1 ended $job_state: $job_error"
}
# check_plan: the probe's install plan names the exact versions of the
# packages of lines whose repository is ready (a module stream that is not
# enabled yet hides them).
check_plan() {
	python3 - /tmp/probe.json <<'PY' || fail "plan: $(head -c 800 /tmp/probe.json)"
import json, sys
d = json.load(open(sys.argv[1]))
for p in d["plan"]:
    if p["method"] == "standalone" or not p["repoReady"] or p.get("stream"):
        continue
    for pkg in p["packages"]:
        v = pkg["version"].split(":", 1)[-1]
        if not v.startswith(p["line"] + ".") and not v.startswith(p["line"] + "-"):
            sys.exit(f'{p["software"]} {p["line"]}: {pkg["name"]} planned as {pkg["version"]!r}')
PY
}

# ext_case SW LINE A,B: installs the extensions, checks that PHP loads them,
# removes the first again and checks it is gone.
ext_status() {
	api "$base/api/v1/software/$1/lines/$2/extensions" >/tmp/ext.json
	python3 - /tmp/ext.json "$3" "$4" <<'PY' || fail "extensions $1 $2 ($3 $4): $(head -c 800 /tmp/ext.json)"
import json, sys
d = json.load(open(sys.argv[1]))
want, ids = sys.argv[2], sys.argv[3].split(",")
by = {e["id"]: e for e in d["extensions"]}
for i in ids:
    e = by[i]
    if want == "available" and (not e["available"] or e["builtin"]):
        sys.exit(f"{i} not installable: {e}")
    if want == "loaded" and not (e["installed"] and e["loaded"]):
        sys.exit(f"{i} not installed and loaded: {e}")
    if want == "gone" and (e["installed"] or e["loaded"]):
        sys.exit(f"{i} still there: {e}")
PY
}
ext_case() {
	local sw=$1 line=$2 exts=$3 first=${3%%,*} builtin
	ext_status "$sw" "$line" available "$exts"
	follow_job "/api/v1/software/$sw/lines/$line/extensions" "$(python3 -c 'import json,sys; print(json.dumps({"install": sys.argv[1].split(",")}))' "$exts")"
	ext_status "$sw" "$line" loaded "$exts"
	follow_job "/api/v1/software/$sw/lines/$line/extensions" "{\"remove\":[\"$first\"]}"
	ext_status "$sw" "$line" gone "$first"
	builtin=$(python3 -c 'import json; print(",".join(e["id"] for e in json.load(open("/tmp/ext.json"))["extensions"] if e["builtin"]))')
	step "$sw $line extensions: $exts installed and loaded, $first removed again (built in: ${builtin:-none})"
}

# A line that breaks each kind of file for its check.
bad_line() {
	case $1 in
	nginx.conf) printf 'bogus_directive on;\n' ;;
	php.ini | php-cli.ini) printf 'cairn_test = = broken\n' ;;
	php-fpm.conf) printf '[global]\nbogus_entry = 1\n' ;;
	*.cnf) printf '[mysqld]\nbogus_option = 1\n' ;;
	postgresql.conf) printf 'bogus_param = 1\n' ;;
	esac
}
# cfg_case SW LINE: every configuration file of the line takes a valid
# change; one that fails its check is refused and the file put back; then
# the original content comes back.
cfg_case() {
	local sw=$1 line=$2 id path sha check url
	api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
	api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
o = [o for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["line"] == sys.argv[2]][0]
for c in o.get("configs", []):
    print(c["id"], str(c["check"]).lower())' "$sw" "$line" >/tmp/configs.txt
	[ -s /tmp/configs.txt ] || fail "$sw $line has no configuration files"
	while read -r id check; do
		url="$base/api/v1/software/$sw/lines/$line/configs/$id"
		api "$url" >/tmp/cfg.json
		path=$(json 'd["path"]' </tmp/cfg.json) || fail "read $id: $(head -c 400 /tmp/cfg.json)"
		sha=$(json 'd["sha256"]' </tmp/cfg.json)
		cp "$path" /tmp/cfg.orig
		# PHP's INI files only take ";" comments.
		mark='#'
		case $id in php*) mark=';' ;; esac
		{ cat /tmp/cfg.orig; printf '\n%s cairn software test\n' "$mark"; } >/tmp/cfg.new
		body() { python3 -c 'import json,sys; print(json.dumps({"content": open(sys.argv[1]).read(), "base": sys.argv[2]}))' "$1" "$2"; }
		run_api_job PUT "/api/v1/software/$sw/lines/$line/configs/$id" "$(body /tmp/cfg.new "$sha")"
		[ "$job_state" = succeeded ] || fail "$sw $line $id: valid change ended $job_state $job_error"
		cmp -s /tmp/cfg.new "$path" || fail "$sw $line $id: not saved"
		local result="saved"
		if [ "$check" = true ]; then
			grep -m1 '^\$ ' /tmp/job.log | sed 's/^\$ //' >/tmp/check.txt
			sha=$(api "$url" | json 'd["sha256"]')
			{ cat /tmp/cfg.new; bad_line "$id"; } >/tmp/cfg.bad
			run_api_job PUT "/api/v1/software/$sw/lines/$line/configs/$id" "$(body /tmp/cfg.bad "$sha")"
			[ "$job_state" = failed ] && [ "$job_error" = pkg.config_invalid ] || fail "$sw $line $id: bad change ended $job_state $job_error"
			cmp -s /tmp/cfg.new "$path" || fail "$sw $line $id: not put back after the failed check"
			result="saved after $(head -c 60 /tmp/check.txt), broken change refused and put back"
		fi
		api "$url" >/tmp/cfg.json
		[ "$(json 'len(d["versions"])' </tmp/cfg.json)" -ge 1 ] || fail "$sw $line $id: no version kept"
		run_api_job PUT "/api/v1/software/$sw/lines/$line/configs/$id" "$(body /tmp/cfg.orig "$(json 'd["sha256"]' </tmp/cfg.json)")"
		[ "$job_state" = succeeded ] && cmp -s /tmp/cfg.orig "$path" || fail "$sw $line $id: original not restored"
		step "$sw $line $id ($path): $result"
	done </tmp/configs.txt
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
	case $c in
	ext:*)
		# ext:php@8.4:intl,redis
		IFS=: read -r _ spec exts <<<"$c"
		ext_case "${spec%@*}" "${spec#*@}" "$exts"
		continue
		;;
	cfg:*)
		spec=${c#cfg:}
		cfg_case "${spec%@*}" "${spec#*@}"
		continue
		;;
	esac
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
	check_plan
	run_job install "$sw" "$line" "$src"
	check_line "$sw" "$line" "$src" "$(($(date +%s) - t0))"
done
sw=${last%@*} line=${last#*@}
run_job remove "$sw" "$line"
[ "$(offer "$sw" "$line" installed)" = False ] || fail "$sw $line still reported installed"
[ ! -e "/opt/cairn/runtimes/$sw/$line" ] || fail "$sw $line: runtime files left"
step "$sw $line removed (configuration kept)"
echo "== PASS: ${PRETTY_NAME}"
