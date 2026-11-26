#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# starts the helper and the web process by hand (no systemd), then probes the
# download sources of every software@line[~origin][#source] of $CASES,
# installs it through the API (from the given origin, else the line's
# default one, and from the given download source, for runtimes) and checks
# the exact installed version and its origin; the last entry is removed
# again, with its repository once no installed line comes from it.
# "pin:sw@line" checks that the package manager's updates keep to the
# installed line of a repository serving several. "ext:php@8.4:a,b"
# installs PHP extensions and removes the first again; "cfg:sw@line" edits
# every configuration file of a line through the API; "src:sw@line"
# installs a system line from an official mirror chosen for that one
# installation and checks the system's sources and indexes are unchanged.
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
	dnf install -y -q procps-ng util-linux python3 diffutils >/dev/null
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
# A browser session is the cookie in the jar and the session key that setup
# and sign-in answer with: keep_key saves it next to the jar, and every
# request sends it (the cookie alone is refused).
skey() { cat "${1:-$jar}.key" 2>/dev/null || true; }
keep_key() (
	out=$(cat)
	printf '%s' "$out" | sed -n 's/.*"sessionKey":"\([A-Za-z0-9_-]*\)".*/\1/p' >"${1:-$jar}.key"
	printf '%s\n' "$out"
)
api() { curl -sk -c "$jar" -b "$jar" -H "X-Cairn-Session: $(skey)" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
pw=software-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | keep_key | grep_q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"

json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
# items_json "sw@line[~origin][#source] ...": the items of a probe or set
# install.
items_json() {
	python3 -c '
import json, sys
out = []
for c in sys.argv[1:]:
    c, _, src = c.partition("#")
    c, _, origin = c.partition("~")
    sw, _, line = c.partition("@")
    item = dict(software=sw, line=line)
    if origin:
        item["origin"] = origin
    if src:
        item["source"] = src
    out.append(item)
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
	# The body goes through stdin: a configuration file (Redis 8's is over
	# 100 KB) does not fit in one command-line argument.
	r=$(printf '%s' "$3" | api -X "$1" "$base$2" --data-binary @-)
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
# system_state: the system's source files and package indexes, byte for
# byte (content, mode, the set of files).
system_state() {
	local dirs
	if command -v apt-get >/dev/null; then
		dirs="/etc/apt /var/lib/apt/lists"
	else
		dirs="/etc/yum.repos.d /var/cache/dnf"
	fi
	# shellcheck disable=SC2086
	find $dirs \( -type f -o -type l \) -printf '%p %m %l\n' | sort
	# shellcheck disable=SC2086
	find $dirs -type f -exec sha256sum {} + | sort
}
# src_case SW LINE: probes the line, picks an eligible official mirror of
# the system's main repositories that is not the one in use (or the
# official address), installs from it and checks that nothing of the
# system's configuration changed.
src_case() {
	local sw=$1 line=$2 pick t0
	t0=$(date +%s)
	probe "$sw@$line"
	check_plan
	pick=$(python3 - /tmp/probe.json <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
s = [s for s in d["slots"] if s["id"] == "system:main"]
if len(d["slots"]) != 1 or not s or not s[0]["selectable"]:
    sys.exit("expected one selectable system:main slot, got %s" % [x["id"] for x in d["slots"]])
# An official mirror, the fastest first; the official address when the
# network offers none this system can use (EL needs HTTPS mirrors).
c = [c for c in s[0]["candidates"] if c["eligible"] and not c["inUse"] and c["kind"] in ("official_mirror", "official")]
c.sort(key=lambda c: (c["kind"] != "official_mirror", -(c.get("speed") or 0)))
print(c[0]["id"] if c else "")
PY
) || fail "probe $sw $line: $(head -c 600 /tmp/probe.json)"
	[ -n "$pick" ] || fail "$sw $line: no eligible official source other than the one in use (see the probe above)"
	system_state >/tmp/state.before
	run_job install "$sw" "$line" "$pick"
	grep_q "for this installation only" /tmp/job.log || fail "$sw $line: the job log does not name the chosen source"
	system_state >/tmp/state.after
	if ! diff /tmp/state.before /tmp/state.after >/tmp/state.diff; then
		head -20 /tmp/state.diff >&2
		fail "$sw $line: the system's sources or indexes changed"
	fi
	if ls -d /var/tmp/cairn-pkgsrc-* >/dev/null 2>&1; then
		fail "$sw $line: the private source configuration was left behind"
	fi
	check_line "$sw" "$line" "" "$(($(date +%s) - t0))"
	step "$sw $line from $(grep -m1 'for this installation only' /tmp/job.log | sed 's/^Package source: //; s/\.$//'); sources and indexes unchanged"
}
# run_job ACTION SOFTWARE LINE [SOURCE [ORIGIN]]
run_job() {
	local body
	body=$(python3 -c '
import json, sys
b = {"line": sys.argv[1]}
if sys.argv[2]:
    b["source"] = sys.argv[2]
if sys.argv[3]:
    b["origin"] = sys.argv[3]
print(json.dumps(b))' "$3" "${4:-}" "${5:-}")
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
# check_line SOFTWARE LINE SOURCE SECONDS [ORIGIN]: the line is installed
# in its exact version (from SOURCE, for a runtime; from ORIGIN when given);
# prints one step.
check_line() {
	local sw=$1 line=$2 src=$3 took=$4 want=${5:-} method pkg v used repo keyinfo="" from="" origin
	[ "$(offer "$sw" "$line" installed)" = True ] || fail "$sw $line not reported installed"
	origin=$(offer "$sw" "$line" origin)
	[ -z "$want" ] || [ "$origin" = "$want" ] || fail "$sw $line: installed from $origin, not $want"
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
# pin_case SOFTWARE LINE: the line comes from a repository serving several
# lines; the package manager's candidates for its packages stay on it.
pin_case() {
	local sw=$1 line=$2 repo pkgs p cand others
	repo=$(offer "$sw" "$line" repo)
	pkgs=$(api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
o = [o for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["line"] == sys.argv[2]][0]
print(" ".join(o.get("packages", [])))' "$sw" "$line")
	# off_line LINE: prints the versions on stdin (one per line) that are not of LINE.
	off_line() { python3 -c 'import re, sys; [print(v.strip()) for v in sys.stdin if v.strip() and not re.match(r"(\d+:)?" + re.escape(sys.argv[1]) + r"\.", v.strip())]' "$1"; }
	if command -v apt-get >/dev/null; then
		apt-get update -qq >/dev/null
		for p in $pkgs; do
			cand=$(apt-cache policy "$p" | sed -n 's/^ *Candidate: //p')
			[ -z "$(printf '%s\n' "$cand" | off_line "$line")" ] || fail "$sw $line: apt's candidate for $p is $cand"
		done
		others=$(apt-cache madison "${pkgs%% *}" | cut -d'|' -f2 | off_line "$line" | wc -l)
		step "$sw $line: apt keeps to the line ($pkgs: $cand; $others versions of other lines offered)"
	else
		# shellcheck disable=SC2086
		cand=$(dnf -q repoquery --qf '%{evr}\n' --repo "cairn-$repo-${line//./}" $pkgs 2>/dev/null | sort -u)
		[ -n "$cand" ] || fail "$sw $line: nothing visible in its repository"
		[ -z "$(printf '%s\n' "$cand" | off_line "$line")" ] || fail "$sw $line: other lines visible in its repository: $cand"
		# shellcheck disable=SC2086
		dnf -q check-update $pkgs >/tmp/upd.txt 2>&1 || true
		[ -z "$(awk 'NF >= 3 {print $2}' /tmp/upd.txt | off_line "$line")" ] || fail "$sw $line: an update leaves the line: $(cat /tmp/upd.txt)"
		step "$sw $line: dnf sees only the line in its repository ($(printf '%s' "$cand" | tr '\n' ' '))"
	fi
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
	src:*)
		spec=${c#src:}
		src_case "${spec%@*}" "${spec#*@}"
		continue
		;;
	pin:*)
		spec=${c#pin:}
		pin_case "${spec%@*}" "${spec#*@}"
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
	src="" origin=""
	case $c in *"#"*) src=${c#*#} c=${c%%#*} ;; esac
	case $c in *"~"*) origin=${c#*~} c=${c%%~*} ;; esac
	sw=${c%@*} line=${c#*@}
	probe "$c${origin:+~$origin}${src:+#$src}"
	check_plan
	run_job install "$sw" "$line" "$src" "$origin"
	check_line "$sw" "$line" "$src" "$(($(date +%s) - t0))" "$origin"
done
last=${last%%~*}
sw=${last%@*} line=${last#*@}
repo=$(offer "$sw" "$line" repo)
run_job remove "$sw" "$line"
[ "$(offer "$sw" "$line" installed)" = False ] || fail "$sw $line still reported installed"
[ ! -e "/opt/cairn/runtimes/$sw/$line" ] || fail "$sw $line: runtime files left"
gone=""
if [ -n "$repo" ]; then
	# The repository goes once no installed line comes from it.
	used=$(api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
print(sum(1 for s in d["software"] for o in s["offers"] if o["installed"] and o.get("repo") == sys.argv[1]))' "$repo")
	left=$(ls /etc/apt/sources.list.d/cairn-"$repo"* /etc/yum.repos.d/cairn-"$repo"* 2>/dev/null || true)
	if [ "$used" = 0 ] && [ -n "$left" ]; then fail "$sw $line: repository files left: $left"; fi
	if [ "$used" != 0 ] && [ -z "$left" ]; then fail "$sw $line: repository removed while $used line(s) still come from it"; fi
	[ "$used" = 0 ] && gone=", its repository $repo with it"
fi
step "$sw $line removed (configuration kept$gone)"
echo "== PASS: ${PRETTY_NAME}"
