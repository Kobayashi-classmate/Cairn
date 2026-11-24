#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# installs MariaDB or MySQL, PostgreSQL and Redis or Valkey through it,
# starts them by hand (no systemd), then manages databases, users,
# access, dumps and the Redis instance through the API and checks the
# result with the servers' own clients.
#
# DB_MYSQL=mariadb|mysql and DB_REDIS=redis|valkey choose the software.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input (pipefail and SIGPIPE).
grep_q() { grep "$@" >/dev/null; }
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	[ -f /tmp/job.log ] && { echo "--- job log" >&2; tail -40 /tmp/job.log >&2; }
	tail -5 /tmp/helper.log >&2 || true
	exit 1
}
trap 'fail "line $LINENO: $BASH_COMMAND"' ERR
mysql_sw=${DB_MYSQL:-mariadb}
redis_sw=${DB_REDIS:-redis}
echo "== ${PRETTY_NAME} ($mysql_sw, postgresql, $redis_sw)"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null
apt-get install -y -qq curl procps python3 >/dev/null
apt-get install -y /pkgs/cairn_*_amd64.deb >/tmp/install.log 2>&1 || fail "install cairn"
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
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=databases-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | keep_key | grep_q '"username":"admin"' || fail "setup"
# Second verification lasts ten minutes; sections renew it.
elevate() { api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"; }

# start METHOD PATH [BODY]: starts a job and waits; prints its final state
# and error code.
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
# follow METHOD PATH [BODY]: the job must succeed.
follow() {
	local out
	out=$(start "$@")
	[ "${out%% *}" = succeeded ] || fail "$1 $2 ended $out"
}
# refused CODE METHOD PATH [BODY]: the job must fail with CODE.
refused() {
	local want=$1 out
	shift
	out=$(start "$@")
	[ "$out" = "failed $want" ] || fail "$1 $2: want failed $want, got $out"
}
engine() { api "$base/api/v1/databases" | json "[e for e in d['engines'] if e['id']=='$1'][0]$2"; }
password() { api "$base/api/v1/databases/$1/users/$2/password" | json 'd["password"]'; }
upload() {
	curl -sk -c "$jar" -b "$jar" -H "X-Cairn-Session: $(skey)" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/octet-stream' -o /dev/null -w '%{http_code}' \
		-X PUT --data-binary "@$3" "$base/api/v1/databases/$1/dumps/$2"
}
dump() { # dump ENGINE NAME: dumps and prints the newest dump's file name
	follow POST "/api/v1/databases/$1/dumps" "{\"name\":\"$2\"}"
	api "$base/api/v1/databases/$1/dumps" | json 'd["dumps"][0]["file"]'
}

rec() { api "$base/api/v1/software" | json "[o['line'] for s in d['software'] if s['id']=='$1' for o in s['offers'] if o['recommended']][0]"; }
my_line=$(rec "$mysql_sw")
pg_line=$(rec postgresql)
redis_line=$(rec "$redis_sw")
r=$(api "$base/api/v1/databases")
[ "$(printf '%s' "$r" | json 'len(d["engines"])')" = 0 ] || fail "engines before installing: $r"
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"$mysql_sw\",\"line\":\"$my_line\"},{\"software\":\"postgresql\",\"line\":\"$pg_line\"},{\"software\":\"$redis_sw\",\"line\":\"$redis_line\"}]}"
pg=postgresql-$pg_line

# No systemd: the servers start by hand.
mkdir -p /run/mysqld && chown mysql:mysql /run/mysqld
mysqld=$(command -v mariadbd || command -v mysqld)
"$mysqld" --user=mysql >/tmp/mysqld.log 2>&1 &
# A package script may have started a server already.
pg_ctlcluster "$pg_line" main start 2>/dev/null || true
rdir=/run/$redis_sw
[ "$redis_sw" = redis ] && conf=/etc/redis/redis.conf || conf=/etc/valkey/valkey.conf
mkdir -p "$rdir" && chown "$redis_sw:$redis_sw" "$rdir"
runuser -u "$redis_sw" -- "$redis_sw-server" "$conf" --daemonize yes || true
for _ in $(seq 100); do [ -S /run/mysqld/mysqld.sock ] && break; sleep 0.2; done
myc=$(command -v mariadb || command -v mysql)
[ "$(engine mysql '["running"]')" = True ] || fail "MySQL not running: $(engine mysql '')"
[ "$(engine "$pg" '["running"]')" = True ] || fail "PostgreSQL not running: $(engine "$pg" '')"
[ "$(engine redis '["running"]')" = True ] || fail "Redis not running: $(engine redis '')"
step "$mysql_sw $my_line, PostgreSQL $pg_line and $redis_sw $redis_line installed through the panel, started by hand"

# --- MariaDB / MySQL -------------------------------------------------------
elevate
follow POST /api/v1/databases/mysql/dbs '{"name":"shop"}'
shop_pw=$(password mysql shop)
[ ${#shop_pw} = 20 ] || fail "generated password: $shop_pw"
"$myc" -ushop -p"$shop_pw" -h127.0.0.1 shop -e "CREATE TABLE t (id int); INSERT INTO t VALUES (1);"
"$myc" -ushop -p"$shop_pw" shop -e "SELECT 1" >/dev/null
step "database shop with user shop (generated password): TCP and socket logins work"

follow POST /api/v1/databases/mysql/users '{"name":"report","password":"report-pass-1","grants":[{"database":"shop","level":"readonly"}]}'
[ "$("$myc" -ureport -preport-pass-1 -h127.0.0.1 shop -N -e "SELECT count(*) FROM t")" = 1 ] || fail "read-only user cannot read"
if "$myc" -ureport -preport-pass-1 -h127.0.0.1 shop -e "INSERT INTO t VALUES (9)" 2>/dev/null; then fail "read-only user can write"; fi
"$myc" -e "CREATE USER legacy@localhost IDENTIFIED BY 'legacy-pass-1'"
refused db.user_exists POST /api/v1/databases/mysql/dbs '{"name":"legacy"}'
"$myc" -ulegacy -plegacy-pass-1 -e "SELECT 1" >/dev/null || fail "an existing user's password changed"
step "read-only user report reads shop and cannot write; an existing user of the same name is not taken over"

# _ is a wildcard in GRANT: the owner of my_app must not reach myxapp.
follow POST /api/v1/databases/mysql/dbs '{"name":"my_app"}'
follow POST /api/v1/databases/mysql/dbs '{"name":"myxapp","noUser":true}'
app_pw=$(password mysql my_app)
"$myc" -umy_app -p"$app_pw" -h127.0.0.1 my_app -e "SELECT 1" >/dev/null
if "$myc" -umy_app -p"$app_pw" -h127.0.0.1 myxapp -e "SELECT 1" 2>/dev/null; then fail "my_app reaches myxapp"; fi
step "the owner of my_app cannot reach myxapp (GRANT wildcards escaped)"

# Dump, download, restore.
file=$(dump mysql shop)
case "$file" in shop_*.sql.gz) ;; *) fail "dump name $file" ;; esac
curl -sk -b "$jar" -H "X-Cairn-Session: $(skey)" -o /tmp/shop.sql.gz "$base/api/v1/databases/mysql/dumps/$file"
gunzip -c /tmp/shop.sql.gz | grep_q 'CREATE TABLE `t`' || fail "downloaded dump"
[ "$(stat -c '%U %a' "/var/lib/cairn/backups/db/mysql/$file")" = "cairn 600" ] || fail "dump owner $(stat -c '%U %a' "/var/lib/cairn/backups/db/mysql/$file")"
"$myc" shop -e "DROP TABLE t"
follow POST /api/v1/databases/mysql/dbs/shop/restore "{\"file\":\"$file\"}"
[ "$("$myc" shop -N -e "SELECT count(*) FROM t")" = 1 ] || fail "restored rows"
printf 'INSERT INTO t VALUES (2);\n' >/tmp/more.sql
[ "$(upload mysql shop_more.sql /tmp/more.sql)" = 201 ] || fail "upload"
follow POST /api/v1/databases/mysql/dbs/shop/restore '{"file":"shop_more.sql"}'
[ "$("$myc" shop -N -e "SELECT count(*) FROM t")" = 2 ] || fail "uploaded dump not restored"
step "dump downloaded (owned by cairn, 0600), table restored; an uploaded dump restored"

# A dump runs with the owner's rights only.
printf 'CREATE DATABASE evil;\nGRANT ALL PRIVILEGES ON *.* TO shop@localhost;\n' >/tmp/evil.sql
[ "$(upload mysql shop_evil.sql /tmp/evil.sql)" = 201 ] || fail "upload evil"
refused db.query_failed POST /api/v1/databases/mysql/dbs/shop/restore '{"file":"shop_evil.sql"}'
[ -z "$("$myc" -N -e "SHOW DATABASES LIKE 'evil'")" ] || fail "a restored dump created a database"
step "a dump that reaches beyond its database fails: it runs as the owner, not as root"

refused db.systemd_required PUT /api/v1/databases/mysql/remote '{"enabled":true}'
follow DELETE "/api/v1/databases/mysql/dbs/shop?dropOwner=true"
[ -z "$("$myc" -N -e "SELECT User FROM mysql.user WHERE User = 'shop'")" ] || fail "owner kept"
[ -z "$("$myc" -N -e "SHOW DATABASES LIKE 'shop'")" ] || fail "database kept"
api "$base/api/v1/databases" | json "[u['name'] for e in d['engines'] if e['id']=='mysql' for u in e['users']]" | grep_q report || fail "report lost"
step "remote access needs systemd here (db.systemd_required); shop dropped with its owner"

# --- PostgreSQL ------------------------------------------------------------
elevate
P=/api/v1/databases/$pg
pgport=$(engine "$pg" '["port"]')
psql_as() { PGPASSWORD=$2 psql -X -q -At -h 127.0.0.1 -p "$pgport" -U "$1" -d "$3" -c "$4"; }
follow POST "$P/dbs" '{"name":"blog"}'
blog_pw=$(password "$pg" blog)
psql_as blog "$blog_pw" blog "CREATE TABLE posts (id int); INSERT INTO posts VALUES (1);"
follow POST "$P/users" '{"name":"reader","password":"reader-pass-1","hosts":["localhost","%"],"grants":[{"database":"blog","level":"readonly"}]}'
[ "$(psql_as reader reader-pass-1 blog "SELECT count(*) FROM posts")" = 1 ] || fail "reader cannot read"
if psql_as reader reader-pass-1 blog "INSERT INTO posts VALUES (2)" 2>/dev/null; then fail "reader can write"; fi
psql_as blog "$blog_pw" blog "CREATE TABLE later (id int);"
psql_as reader reader-pass-1 blog "SELECT count(*) FROM later" >/dev/null || fail "default privileges"
grep_q -P '^host\tall\treader\t0\.0\.0\.0/0\tscram-sha-256$' "/etc/postgresql/$pg_line/main/pg_hba.conf" || fail "pg_hba.conf"
follow POST "$P/users" '{"name":"stranger","password":"stranger-pass-1"}'
if psql_as stranger stranger-pass-1 blog "SELECT 1" 2>/dev/null; then fail "a user without access connects to blog"; fi
step "blog with owner blog; reader reads (also later tables), cannot write; others cannot connect; remote hosts in pg_hba.conf"

file=$(dump "$pg" blog)
case "$file" in blog_*.dump) ;; *) fail "dump name $file" ;; esac
psql_as blog "$blog_pw" blog "DROP TABLE posts;"
follow POST "$P/dbs/blog/restore" "{\"file\":\"$file\"}"
[ "$(psql_as blog "$blog_pw" blog "SELECT count(*) FROM posts")" = 1 ] || fail "restored rows"
printf 'ALTER ROLE blog SUPERUSER;\n' >/tmp/evil.sql
[ "$(upload "$pg" blog_evil.sql /tmp/evil.sql)" = 201 ] || fail "upload evil"
refused db.query_failed POST "$P/dbs/blog/restore" '{"file":"blog_evil.sql"}'
[ "$(runuser -u postgres -- psql -X -At -p "$pgport" -c "SELECT rolsuper FROM pg_roles WHERE rolname = 'blog'")" = f ] || fail "blog became superuser"
step "dump restored as the owner; a dump cannot make its owner a superuser"

# PostgreSQL restarts without systemd (pg_ctlcluster).
follow PUT "$P/remote" '{"enabled":true}'
[ "$(engine "$pg" '["remote"]')" = True ] || fail "remote not on: $(engine "$pg" '')"
follow PUT "$P/remote" '{"enabled":false}'
follow POST "$P/service" '{"action":"stop"}'
[ "$(engine "$pg" '["running"]')" = False ] || fail "still running"
follow POST "$P/service" '{"action":"start"}'
[ "$(engine "$pg" '["running"]')" = True ] || fail "not started"
refused db.user_owns_database DELETE "$P/users/blog"
follow DELETE "$P/users/reader"
if grep_q reader "/etc/postgresql/$pg_line/main/pg_hba.conf"; then fail "reader kept in pg_hba.conf"; fi
follow DELETE "$P/dbs/blog?dropOwner=true"
step "remote on and off, stop and start; owners stay until their database goes; users and databases dropped"

# --- Redis / Valkey --------------------------------------------------------
elevate
cli="$redis_sw-cli"
refused redis.password_required PUT /api/v1/databases/redis/remote '{"enabled":true}'
follow PUT /api/v1/databases/redis/config '{"generatePassword":true,"maxMemoryMb":64,"maxMemoryPolicy":"allkeys-lru","persistence":"aof"}'
rpw=$(api "$base/api/v1/databases/redis/password" | json 'd["password"]')
[ "$($cli --no-auth-warning -a "$rpw" CONFIG GET maxmemory | tail -1)" = 67108864 ] || fail "maxmemory"
[ "$($cli --no-auth-warning -a "$rpw" CONFIG GET appendonly | tail -1)" = yes ] || fail "appendonly"
grep_q "^requirepass" "$conf" || fail "password not in $conf"
[ "$(engine redis '["redis"]["passwordSet"]')" = True ] || fail "status"
$cli --no-auth-warning -a "$rpw" SET user:1 alice >/dev/null
$cli --no-auth-warning -a "$rpw" HSET session:1 who alice >/dev/null
r=$(api "$base/api/v1/databases/redis/keys?pattern=user:*")
[ "$(printf '%s' "$r" | json '[k["key"] for k in d["keys"]]')" = "['user:1']" ] || fail "scan: $r"
[ "$(api "$base/api/v1/databases/redis/value?key=session:1" | json 'd["items"]')" = "['who', 'alice']" ] || fail "hash value"
api -X PUT "$base/api/v1/databases/redis/ttl" -d '{"db":0,"key":"user:1","seconds":300}' >/dev/null
[ "$($cli --no-auth-warning -a "$rpw" TTL user:1)" -gt 0 ] || fail "ttl"
api -X POST "$base/api/v1/databases/redis/delete" -d '{"db":0,"keys":["user:1"]}' | grep_q '"deleted":1' || fail "delete"
step "password generated (kept, shown again), memory limit and AOF set; key browser lists, shows, expires and deletes"

out=$(start PUT /api/v1/databases/redis/remote '{"enabled":true}')
case "$out" in
"succeeded "*) [ "$(engine redis '["remote"]')" = True ] || fail "remote not on"
	follow PUT /api/v1/databases/redis/remote '{"enabled":false}'
	step "remote access on and off at run time" ;;
"failed db.systemd_required") step "this $redis_sw changes bind only with a restart: needs systemd here" ;;
*) fail "redis remote: $out" ;;
esac
file=$(follow POST /api/v1/databases/redis/dumps '{}' && api "$base/api/v1/databases/redis/dumps" | json 'd["dumps"][0]["file"]')
case "$file" in redis_*.rdb) ;; *) fail "snapshot $file" ;; esac
head -c 5 "/var/lib/cairn/backups/db/redis/$file" | grep_q -E '^(REDIS|VALKE)' || fail "snapshot content"
step "snapshot $file"
echo "OK"
