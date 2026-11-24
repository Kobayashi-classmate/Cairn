#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh), next to an S3 server
# reachable as s3:8080: backs up a site, a directory and (with apt) a
# MariaDB database, encrypted for $AGE_RECIPIENT, into the bucket only;
# checks retention, downloads through a presigned link and restores with
# $AGE_IDENTITY. Without systemd, scheduled tasks and the journal are
# refused clearly.
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	[ -f /tmp/job.log ] && { echo "--- job log" >&2; tail -30 /tmp/job.log >&2; }
	tail -5 /tmp/helper.log >&2 || true
	exit 1
}
trap 'fail "line $LINENO: $BASH_COMMAND"' ERR
echo "== ${PRETTY_NAME}"

if command -v apt-get >/dev/null; then
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -qq >/dev/null
	apt-get install -y -qq curl procps python3 >/dev/null
	apt-get install -y /pkgs/cairn_*_amd64.deb >/tmp/install.log 2>&1 || fail "install cairn"
else
	command -v curl >/dev/null || dnf install -y -q curl >/dev/null
	dnf install -y -q procps-ng python3 shadow-utils util-linux diffutils >/dev/null
	dnf install -y /pkgs/cairn-*.x86_64.rpm >/tmp/install.log 2>&1 || fail "install cairn"
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
status() { curl -sk -c "$jar" -b "$jar" -H "X-Cairn-Session: $(skey)" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' -o /tmp/body -w '%{http_code}' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=backups-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | keep_key | grep_q '"username":"admin"' || fail "setup"
elevate() { api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"; }
elevate

start() {
	local r id state offset=0
	r=$(api -X "$1" "$base$2" ${3:+-d "$3"})
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$1 $2 not started: $r"
	: >/tmp/job.log
	for _ in $(seq 200); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/tmp/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	printf '%s %s\n' "$state" "$(printf '%s' "$r" | json '(d["job"].get("error") or {}).get("code","")')"
}
follow() {
	local out
	out=$(start "$@")
	[ "${out%% *}" = succeeded ] || fail "$1 $2 ended $out"
}
refused() {
	local want=$1 out
	shift
	out=$(start "$@")
	[ "$out" = "failed $want" ] || fail "$1 $2: want failed $want, got $out"
}

# A site as the sites module makes it.
groupadd --system cw-demo
useradd --system --gid cw-demo --home-dir /srv/www/demo --no-create-home --shell /usr/sbin/nologin cw-demo
mkdir -p /srv/www/demo/app/data /srv/www/demo/tmp
echo '<?php echo "demo";' >/srv/www/demo/app/index.php
head -c 200000 /dev/urandom >/srv/www/demo/app/data/blob.bin
echo session >/srv/www/demo/tmp/sess_1
chown -R cw-demo:cw-demo /srv/www/demo

# The bucket.
prefix=$(printf '%s' "$ID-$VERSION_ID" | tr -c 'a-z0-9' '-')
storage="{\"name\":\"test\",\"endpoint\":\"s3:8080\",\"bucket\":\"cairn-backups\",\"prefix\":\"$prefix\",\"accessKey\":\"TESTKEY\",\"pathStyle\":true,\"insecure\":true"
[ "$(status -X POST "$base/api/v1/backups/storages/test" -d "$storage,\"secretKey\":\"WRONG\"}")" = 502 ] || fail "a wrong secret passes: $(cat /tmp/body)"
sid=$(api -X POST "$base/api/v1/backups/storages" -d "$storage,\"secretKey\":\"TESTSECRET\"}" | json 'd["id"]')
[ "$(status -X POST "$base/api/v1/backups/storages/$sid/test")" = 204 ] || fail "bucket test: $(cat /tmp/body)"
step "bucket checked (a wrong secret is refused)"

targets='{"kind":"site","site":"demo"},{"kind":"directory","root":"sites","path":"demo/app/data"}'
db=false
if command -v apt-get >/dev/null; then
	line=$(api "$base/api/v1/software" | json "[o['line'] for s in d['software'] if s['id']=='mariadb' for o in s['offers'] if o['recommended']][0]")
	follow POST /api/v1/software/install "{\"items\":[{\"software\":\"mariadb\",\"line\":\"$line\"}]}"
	mkdir -p /run/mysqld && chown mysql:mysql /run/mysqld
	"$(command -v mariadbd || command -v mysqld)" --user=mysql >/tmp/mysqld.log 2>&1 &
	for _ in $(seq 100); do [ -S /run/mysqld/mysqld.sock ] && break; sleep 0.2; done
	follow POST /api/v1/databases/mysql/dbs '{"name":"shop"}'
	myc=$(command -v mariadb || command -v mysql)
	"$myc" shop -e "CREATE TABLE t (id int); INSERT INTO t VALUES (1), (2), (3);"
	targets="$targets"',{"kind":"database","engine":"mysql","database":"shop"}'
	db=true
fi
plan=$(api -X POST "$base/api/v1/backups/plans" -d "{\"name\":\"nightly\",\"targets\":[$targets],\"storageId\":\"$sid\",\"keepLocal\":false,
	\"retention\":{\"last\":2,\"daily\":0,\"weekly\":0,\"monthly\":0},\"recipients\":[\"$AGE_RECIPIENT\"]}" | json 'd["id"]')
for _ in 1 2 3; do
	follow POST "/api/v1/backups/plans/$plan/run"
	sleep 1
done
api "$base/api/v1/backups/plans/$plan/files" >/tmp/files.json
python3 -I - "$db" /tmp/files.json <<'PY' || fail "backups: $(cat /tmp/files.json)"
import collections, json, sys
d = json.load(open(sys.argv[2]))
db = sys.argv[1] == "true"
by = collections.Counter(f["target"] for f in d["files"])
want = {"site-demo": 2, "dir-sites-demo-app-data": 2}
if db:
    want["db-mysql-shop"] = 2
assert dict(by) == want, by
assert all(f["remote"] and not f["local"] and f["encrypted"] for f in d["files"]), d["files"]
PY
[ -z "$(ls /var/lib/cairn/backups/plans/"$plan")" ] || fail "local copies kept"
step "plan run 3 times: encrypted backups in the bucket only, 2 kept per target"

# Restore the site, with the key only.
site=$(python3 -c 'import json; print([f["name"] for f in json.load(open("/tmp/files.json"))["files"] if f["target"]=="site-demo"][0])')
api -X POST "$base/api/v1/files/mkdir" -d '{"root":"sites","dir":"","name":"restored"}' >/dev/null
chown cw-demo:cw-demo /srv/www/restored
elevate
refused backup.identity_required POST "/api/v1/backups/plans/$plan/restore" "{\"name\":\"$site\",\"dest\":{\"root\":\"sites\",\"path\":\"restored\"}}"
follow POST "/api/v1/backups/plans/$plan/restore" "{\"name\":\"$site\",\"identity\":\"$AGE_IDENTITY\",\"dest\":{\"root\":\"sites\",\"path\":\"restored\"}}"
cmp -s /srv/www/demo/app/data/blob.bin /srv/www/restored/app/data/blob.bin || fail "restored content"
[ "$(stat -c %U /srv/www/restored/app/index.php)" = cw-demo ] && [ ! -e /srv/www/restored/tmp/sess_1 ] || fail "restored owner, or tmp/ came along"
step "site restored from the bucket with the private key (refused without it), owned by the site, without tmp/"

# Download through a short-lived link to the bucket.
size=$(python3 -c "import json; print([f['size'] for f in json.load(open('/tmp/files.json'))['files'] if f['name']=='$site'][0])")
code=$(curl -sk -b "$jar" -H "X-Cairn-Session: $(skey)" -o /tmp/dl -w '%{http_code}' "$base/api/v1/backups/plans/$plan/files/$site")
[ "$code" = 302 ] || fail "download answered $code"
curl -sk -b "$jar" -H "X-Cairn-Session: $(skey)" -L -o /tmp/dl "$base/api/v1/backups/plans/$plan/files/$site"
[ "$(wc -c </tmp/dl)" = "$size" ] && head -c 21 /tmp/dl | grep_q age-encryption.org || fail "presigned download"
step "download redirected to a presigned link; the encrypted file arrives whole"

if $db; then
	dbfile=$(python3 -c 'import json; print([f["name"] for f in json.load(open("/tmp/files.json"))["files"] if f["target"]=="db-mysql-shop"][0])')
	follow POST "/api/v1/backups/plans/$plan/restore" "{\"name\":\"$dbfile\",\"identity\":\"$AGE_IDENTITY\",\"dest\":{\"root\":\"sites\",\"path\":\"\"}}"
	dump=$(sed -n 's/.*now the dump \([^ ]*\) of mysql.*/\1/p' /tmp/job.log)
	[ -f "/var/lib/cairn/backups/db/mysql/$dump" ] || fail "no dump from the database backup: $(cat /tmp/job.log)"
	"$myc" shop -e "DROP TABLE t"
	follow POST /api/v1/databases/mysql/dbs/shop/restore "{\"file\":\"$dump\"}"
	[ "$("$myc" shop -N -e "SELECT count(*) FROM t")" = 3 ] || fail "database not restored"
	step "database backup decrypted into a dump and restored as the database's owner"
fi

follow POST "/api/v1/backups/plans/$plan/delete" "{\"names\":[\"$site\"],\"remote\":true}"
api "$base/api/v1/backups/plans/$plan/files" | json '[f["name"] for f in d["files"]]' | grep_q "$site" && fail "deleted backup still listed"
[ "$(status "$base/api/v1/logs/journal")" = 404 ] && grep_q log.no_journal /tmp/body || fail "journal without journalctl"
[ "$(status -X POST "$base/api/v1/cron" -d "{\"name\":\"b\",\"type\":\"backup\",\"schedule\":\"daily\",\"plan\":\"$plan\",\"enabled\":true}")" = 202 ] || fail "task not started"
id=$(json 'd["job"]["id"]' </tmp/body)
for _ in $(seq 60); do
	state=$(api "$base/api/v1/jobs/$id" | json 'd["job"]["state"]')
	case "$state" in succeeded | failed) break ;; esac
	sleep 0.5
done
[ "$(api "$base/api/v1/jobs/$id" | json 'd["job"]["error"]["code"]')" = cron.systemd_required ] || fail "task without systemd"
step "deleted from the bucket; without systemd: no journal (log.no_journal), no tasks (cron.systemd_required)"
echo "OK"
