#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# The P1 acceptance, run as root inside the test VM (see run.sh) on every
# tier-1 distribution: deploy a PHP site and a Node.js project, issue their
# certificates (HTTP-01 and DNS-01 from Pebble, Let's Encrypt's test CA),
# set up databases and a backup to an S3-compatible bucket and restore it,
# and change the firewall without losing SSH (run.sh logs in again after
# this script). Pebble, an authoritative DNS server for "test." (TSIG
# dynamic updates) and the bucket run in the QEMU container, 10.0.2.2 for
# the guest.
#
#   SCRIPT=accept.sh bash test/vm/run.sh <target>
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
R=http://10.0.2.2:8080/releases
H=10.0.2.2
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
family=debian
case " $ID ${ID_LIKE:-} " in *" rhel "* | *" fedora "* | *" rocky "* | *" almalinux "*) family=el ;; esac
selinux=no
if command -v getenforce >/dev/null && [ "$(getenforce)" = Enforcing ]; then selinux=yes; fi
avcs() {
	{
		ausearch -m AVC,USER_AVC -ts boot 2>/dev/null
		journalctl -b _TRANSPORT=audit --no-pager 2>/dev/null
		journalctl -b -k --no-pager 2>/dev/null
	} | grep -E 'avc: +denied' | sort -u || true
}
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	tail -40 /root/job.log 2>/dev/null >&2 || true
	[ "$selinux" = yes ] && avcs | tail -20 >&2
	journalctl -u cairn-helper --no-pager -n 30 >&2 || true
	exit 1
}
export DEBIAN_FRONTEND=noninteractive
echo "== ${PRETTY_NAME} (VM, P1 acceptance; SELinux enforcing: $selinux)"

# Pebble's HTTPS names the host "pebble".
grep_q ' pebble$' /etc/hosts || echo "$H pebble" >>/etc/hosts
curl -fsSL "$R/latest/download/install.sh" -o /root/install.sh
sh /root/install.sh --mirror "$R" --lang zh-CN --yes >/root/install.log 2>&1 || fail "install.sh"
# DNS-01 checks the TXT record at the test zone's server.
printf '\n[acme]\nresolvers = ["%s:53"]\n' "$H" >>/etc/cairn/config.toml
systemctl restart cairn-web.service
systemctl stop cairn-helper.service 2>/dev/null || true
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
for _ in $(seq 120); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 1; done
jar=/root/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=vm-test-password-1
token=$(grep -o 'token=[A-Za-z0-9_-]*' /root/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"zh-CN\"}" | grep_q '"username":"admin"' || fail "setup"
login() {
	rm -f "$jar"
	api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | grep_q '"username":"admin"' || fail "login"
	api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
}
login
# The installer switched the firewall on (Chinese install log, so ask the
# panel rather than read it).
fw=$(api "$base/api/v1/firewall")
[ "$(printf '%s' "$fw" | json 'd["enabled"] and d["loaded"]')" = True ] || fail "the installer did not switch the firewall on: $fw"
follow() {
	local r id state offset=0
	r=$(api -X "$1" "$base$2" ${3:+-d "$3"})
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")' 2>/dev/null)
	[ -n "$id" ] || fail "$1 $2 not started: $r"
	: >/root/job.log
	for _ in $(seq 400); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | json 'd["log"]' >>/root/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	if [ "$state" != succeeded ]; then
		api "$base/api/v1/jobs/$id/log?offset=0" | json 'd["log"]' | tail -40 >&2 || true
		fail "$1 $2 ended $state: $(api "$base/api/v1/jobs/$id")"
	fi
}
recommended() {
	api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
r = [o["line"] for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["recommended"]]
print(r[0] if r else "")' "$1"
}
newest_cert() { api "$base/api/v1/certs" | json 'sorted(d["certs"], key=lambda c: c["createdAt"])[-1]["id"]'; }
step "installed (firewall on), first-run setup done, the ACME resolver points at the test zone"

# Software for both sites and the databases, in one go.
php=$(recommended php) node=$(recommended nodejs)
redis_sw=redis
[ -n "$(recommended redis)" ] || redis_sw=valkey
items="{\"software\":\"nginx\",\"line\":\"$(recommended nginx)\"},{\"software\":\"php\",\"line\":\"$php\"},{\"software\":\"nodejs\",\"line\":\"$node\"}"
items="$items,{\"software\":\"mariadb\",\"line\":\"$(recommended mariadb)\"},{\"software\":\"$redis_sw\",\"line\":\"$(recommended $redis_sw)\"}"
pg=""
if [ "$family" = debian ]; then
	pg=$(recommended postgresql)
	items="$items,{\"software\":\"postgresql\",\"line\":\"$pg\"}"
fi
follow POST /api/v1/software/install "{\"items\":[$items]}"
step "installed through the panel: nginx, PHP $php, Node.js $node, MariaDB, $redis_sw${pg:+, PostgreSQL $pg}"

# The custom CA: Pebble, whose HTTPS is signed by its test root.
root=$(python3 -c 'import json; print(json.dumps(open("/tmp/minica.pem").read()))')
api -X PUT "$base/api/v1/certs/settings" -d "{\"email\":\"\",\"defaultCa\":\"custom\",\"cas\":{\"custom\":{\"directory\":\"https://pebble:14000/dir\",\"rootPem\":$root}}}" |
	grep_q '"ready":true' || fail "CA settings"
curl -sk https://pebble:15000/roots/0 >/root/pebble-root.pem
curl -sk https://pebble:15000/intermediates/0 >>/root/pebble-root.pem
acct=$(api -X POST "$base/api/v1/dns/accounts" -d "{\"name\":\"bind\",\"provider\":\"rfc2136\",\"fields\":{\"server\":\"$H:53\",\"key_name\":\"cairn.\",\"key\":\"$TSIG_SECRET\",\"key_alg\":\"hmac-sha256\"}}" | json 'd["id"]')

# A PHP site with HTTP-01 (Pebble connects to port 80 through the
# firewall) and HTTPS.
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"site.test\",\"www.site.test\"],\"php\":\"$php\",\"writable\":[\"uploads\"]}"
runuser -u cw-site-test -- tee /srv/www/site-test/app/index.php >/dev/null <<'PHP'
<?php
$up = @file_put_contents(__DIR__ . '/uploads/x.txt', 'x') !== false ? 'yes' : 'no';
echo "php site, uploads=$up";
PHP
follow POST /api/v1/certs '{"domains":["site.test","www.site.test"],"challenge":"http-01"}'
site_cert=$(newest_cert)
api "$base/api/v1/certs/$site_cert" | json 'd["issuer"]' | grep_q -i pebble || fail "issuer: $(api "$base/api/v1/certs/$site_cert")"
follow PUT /api/v1/sites/site-test "{\"type\":\"php\",\"domains\":[\"site.test\",\"www.site.test\"],\"php\":\"$php\",\"writable\":[\"uploads\"],\"tls\":{\"cert\":\"$site_cert\",\"redirect\":true}}"
out=$(curl -s --cacert /root/pebble-root.pem --resolve site.test:443:127.0.0.1 https://site.test/)
[ "$out" = "php site, uploads=yes" ] || fail "PHP over HTTPS: $out"
step "PHP site site.test: certificate from Pebble over HTTP-01, HTTPS verified against Pebble's root, uploads/ writable"

# A Node.js project with DNS-01 (RFC 2136 to the test zone) and HTTPS.
follow POST /api/v1/sites "{\"type\":\"app\",\"domains\":[\"app.test\"],\"writable\":[\"data\"],\"app\":{\"runtime\":\"nodejs\",\"line\":\"$node\",\"command\":\"node server.js\",\"port\":0}}"
runuser -u cw-app-test -- tee /srv/www/app-test/app/server.js >/dev/null <<'JS'
require('http').createServer((req, res) => res.end('node app, ' + process.version + '\n')).listen(process.env.PORT, '127.0.0.1');
JS
follow POST /api/v1/sites/app-test/restart
follow POST /api/v1/certs "{\"domains\":[\"app.test\"],\"challenge\":\"dns-01\",\"dnsAccount\":\"$acct\"}"
app_cert=$(newest_cert)
follow PUT /api/v1/sites/app-test "{\"type\":\"app\",\"domains\":[\"app.test\"],\"writable\":[\"data\"],\"app\":{\"runtime\":\"nodejs\",\"line\":\"$node\",\"command\":\"node server.js\",\"port\":0},\"tls\":{\"cert\":\"$app_cert\",\"redirect\":true}}"
for _ in $(seq 60); do curl -sf -o /dev/null --cacert /root/pebble-root.pem --resolve app.test:443:127.0.0.1 https://app.test/ && break; sleep 1; done
out=$(curl -s --cacert /root/pebble-root.pem --resolve app.test:443:127.0.0.1 https://app.test/)
printf '%s' "$out" | grep_q "^node app, v$node\." || fail "Node.js over HTTPS: $out"
step "Node.js project app.test: certificate over DNS-01 (TXT through RFC 2136), HTTPS verified"

# Databases.
login
follow POST /api/v1/databases/mysql/dbs '{"name":"shop"}'
follow POST /api/v1/databases/mysql/users '{"name":"shopper","password":"shop-password-1","hosts":["localhost"],"grants":[{"database":"shop","level":"all"}]}'
mariadb -ushopper -pshop-password-1 shop -e "CREATE TABLE t (id int); INSERT INTO t VALUES (1), (2), (3);" || fail "MariaDB as shopper"
if [ -n "$pg" ]; then
	follow POST "/api/v1/databases/postgresql-$pg/dbs" '{"name":"crm"}'
	[ "$(runuser -u postgres -- psql -tAc "SELECT 1 FROM pg_database WHERE datname='crm'")" = 1 ] || fail "PostgreSQL database"
fi
follow PUT /api/v1/databases/redis/config '{"generatePassword":true}'
step "databases: MariaDB shop with its user${pg:+, PostgreSQL crm}, $redis_sw with a password"

# A backup plan to the bucket: the PHP site and the MariaDB database; then
# the site comes back into another directory.
storage="{\"name\":\"bucket\",\"endpoint\":\"$H:8081\",\"bucket\":\"cairn-backups\",\"prefix\":\"$ID-$VERSION_ID\",\"accessKey\":\"TESTKEY\",\"secretKey\":\"TESTSECRET\",\"pathStyle\":true,\"insecure\":true}"
sid=$(api -X POST "$base/api/v1/backups/storages" -d "$storage" | json 'd["id"]') || fail "storage"
plan=$(api -X POST "$base/api/v1/backups/plans" -d "{\"name\":\"nightly\",\"targets\":[{\"kind\":\"site\",\"site\":\"site-test\"},{\"kind\":\"database\",\"engine\":\"mysql\",\"database\":\"shop\"}],\"storageId\":\"$sid\",\"keepLocal\":true,\"retention\":{\"last\":3,\"daily\":0,\"weekly\":0,\"monthly\":0}}" | json 'd["id"]') || fail "plan"
follow POST "/api/v1/backups/plans/$plan/run"
api "$base/api/v1/backups/plans/$plan/files" >/root/files.json
python3 - /root/files.json <<'PY' || fail "backups: $(cat /root/files.json)"
import json, sys
d = json.load(open(sys.argv[1]))
t = {f["target"]: f for f in d["files"]}
assert set(t) == {"site-site-test", "db-mysql-shop"}, t
assert all(f["remote"] and f["local"] for f in t.values()), t
PY
site_file=$(python3 -c 'import json; print([f["name"] for f in json.load(open("/root/files.json"))["files"] if f["target"]=="site-site-test"][0])')
api -X POST "$base/api/v1/files/mkdir" -d '{"root":"sites","dir":"","name":"restored"}' >/dev/null
follow POST "/api/v1/backups/plans/$plan/restore" "{\"name\":\"$site_file\",\"dest\":{\"root\":\"sites\",\"path\":\"restored\"}}"
grep -rq "php site" /srv/www/restored || fail "restored site: $(find /srv/www/restored | head -20)"
step "backup plan: site and database to the bucket and locally; the site restored into /srv/www/restored"

# Installed lines: services, a PHP extension and configuration files, with
# systemd. A restart that fails with a broken file puts the old one back.
api "$base/api/v1/databases" | python3 -c '
import json, sys
d = json.load(sys.stdin)
shop = [x for e in d["engines"] if e["id"] == "mysql" for x in e["databases"] if x["name"] == "shop"][0]
assert d["backups"] and shop.get("backup", {}).get("lastOkAt"), shop' || fail "database backup column: $(api "$base/api/v1/databases")"
nginx_line=$(recommended nginx)
follow POST /api/v1/software/nginx/service "{\"line\":\"$nginx_line\",\"action\":\"restart\"}"
systemctl is-active --quiet nginx || fail "nginx after restart"
follow POST "/api/v1/software/php/lines/$php/extensions" '{"install":["intl"]}'
php_fpm=$(api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
o = [o for s in d["software"] if s["id"] == "php" for o in s["offers"] if o["line"] == sys.argv[1]][0]
print(o["units"][0]["name"], o["units"][0]["state"])' "$php")
[ "${php_fpm#* }" = running ] || fail "PHP-FPM after the extension: $php_fpm"
api "$base/api/v1/software/php/lines/$php/extensions" | json '[e for e in d["extensions"] if e["id"] == "intl"][0]' | grep_q "'loaded': True" || fail "intl not loaded"
grep_q "Reloading\|systemctl reload" /root/job.log || fail "PHP-FPM not reloaded: $(tail -5 /root/job.log)"
save_config() { # software line config content-file -> sets job_state
	local url="/api/v1/software/$1/lines/$2/configs/$3" sha body
	sha=$(api "$base$url" | json 'd["sha256"]') || fail "read $3"
	body=$(python3 -c 'import json,sys; print(json.dumps({"content": open(sys.argv[1]).read(), "base": sys.argv[2]}))' "$4" "$sha")
	follow PUT "$url" "$body"
}
cp /etc/nginx/nginx.conf /root/nginx.conf.new && printf '\n# acceptance\n' >>/root/nginx.conf.new
save_config nginx "$nginx_line" nginx.conf /root/nginx.conf.new
grep_q "systemctl reload nginx" /root/job.log || fail "nginx not reloaded: $(tail -5 /root/job.log)"
out=$(curl -s --cacert /root/pebble-root.pem --resolve site.test:443:127.0.0.1 https://site.test/)
[ "$out" = "php site, uploads=yes" ] || fail "PHP site after the nginx.conf change: $out"
redis_line=$(recommended $redis_sw) redis_conf=/etc/redis/redis.conf redis_unit=$(api "$base/api/v1/databases" | json '[e for e in d["engines"] if e["id"] == "redis"][0]["service"]')
[ "$redis_sw" = valkey ] && redis_conf=/etc/valkey/valkey.conf
cp "$redis_conf" /root/redis.orig && { cat /root/redis.orig; printf 'bogus_directive 1\n'; } >/root/redis.bad
url="/api/v1/software/$redis_sw/lines/$redis_line/configs/$(basename "$redis_conf")"
r=$(api -X PUT "$base$url" -d "$(python3 -c 'import json,sys; print(json.dumps({"content": open(sys.argv[1]).read(), "base": sys.argv[2]}))' /root/redis.bad "$(api "$base$url" | json 'd["sha256"]')")")
id=$(printf '%s' "$r" | json 'd["job"]["id"]') || fail "redis config not started: $r"
for _ in $(seq 120); do
	state=$(api "$base/api/v1/jobs/$id" | json 'd["job"]["state"] if "job" in d else d["state"]')
	case "$state" in succeeded | failed) break ;; esac
	sleep 1
done
[ "$state" = failed ] || fail "a broken $redis_conf was applied: $state"
cmp -s /root/redis.orig "$redis_conf" || fail "$redis_conf not put back"
systemctl is-active --quiet "$redis_unit" || fail "$redis_unit not running after the rollback"
step "installed lines: nginx restarted, PHP $php has intl (PHP-FPM reloaded), nginx.conf saved and reloaded, a broken $(basename "$redis_conf") rolled back with $redis_unit running"

# The firewall: changes confirmed over a new connection; SSH stays
# reachable (run.sh logs in again afterwards).
change() { # method path body -> confirms the pending change
	local r id
	r=$(api -X "$1" "$base$2" -d "$3")
	id=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "firewall $2: $r"
	api -X POST "$base/api/v1/firewall/confirm" -d "{\"id\":\"$id\"}" | grep_q '^{}' || fail "confirm $2"
}
change POST /api/v1/firewall/rules '{"action":"allow","protocol":"tcp","ports":["8081"],"note":"acceptance"}'
change POST /api/v1/firewall/addresses '{"address":"192.0.2.50","action":"deny","note":"acceptance"}'
change PUT /api/v1/firewall/ping '{"block":true}'
api "$base/api/v1/firewall" | json 'd["enabled"] and d["loaded"] and d["pending"] is None' | grep_q True || fail "firewall: $(api "$base/api/v1/firewall")"
step "firewall ($(api "$base/api/v1/firewall" | json 'd["backend"]')): a rule, a blocked address and ping changed and confirmed"

if [ "$selinux" = yes ]; then
	d=$(avcs | grep -E 'comm="(nginx|php-fpm|mariadbd|mysqld|redis-server|valkey-server|node|sh|cairn-helper|cairn-web)"' || true)
	[ -z "$d" ] || { printf '%s\n' "$d" | tail -20 >&2; fail "SELinux denied something"; }
	step "SELinux: no denials"
fi
echo "== PASS: ${PRETTY_NAME} (VM, P1 acceptance)"
