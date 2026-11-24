#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the test VM (see run.sh): sites, apps, a four-layer
# proxy and databases on any supported family, with the real systemd and
# (on EL) SELinux enforcing. It checks what PHP-FPM may write, the SELinux
# types of the site directories and ports, and that nothing the panel set
# up was denied by SELinux.
#
#   SCRIPT=sites.sh bash test/vm/run.sh rocky-9
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
R=http://10.0.2.2:8080/releases
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
family=debian
case " $ID ${ID_LIKE:-} " in *" rhel "* | *" fedora "* | *" rocky "* | *" almalinux "*) family=el ;; esac
selinux=no
if command -v getenforce >/dev/null && [ "$(getenforce)" = Enforcing ]; then selinux=yes; fi
# AVC denials of this boot. Cloud images may run without auditd: then the
# kernel's audit messages are only in the journal.
avcs() {
	{
		ausearch -m AVC,USER_AVC -ts boot 2>/dev/null
		journalctl -b _TRANSPORT=audit --no-pager 2>/dev/null
		journalctl -b -k --no-pager 2>/dev/null
	} | grep -E 'avc: +denied' | sort -u || true
}
denials() {
	[ "$selinux" = yes ] || return 0
	avcs | grep -E 'comm="(nginx|php-fpm|mariadbd|mysqld|redis-server|valkey-server|node|sh|cairn-helper)"' || true
}
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	tail -40 /root/job.log >&2 2>/dev/null || true
	if [ "$selinux" = yes ]; then
		echo "--- AVC denials:" >&2
		denials | tail -30 >&2
	fi
	journalctl -u cairn-helper -u nginx --no-pager -n 40 >&2 || true
	exit 1
}
export DEBIAN_FRONTEND=noninteractive
echo "== ${PRETTY_NAME} (VM, sites and databases; SELinux enforcing: $selinux)"

curl -fsSL "$R/latest/download/install.sh" -o /root/install.sh
sh /root/install.sh --mirror "$R" --lang en --yes >/root/install.log 2>&1 || fail "install.sh"
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
for _ in $(seq 120); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 1; done
jar=/root/jar elevated=/root/elevated-at
req() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
elevate() {
	req -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
	date +%s >"$elevated"
}
# A step-up lasts 10 minutes: every call elevates again first once the
# last step-up is 8 minutes old (kept in a file: calls run in command
# substitutions).
api() {
	if [ -s "$elevated" ] && [ $(($(date +%s) - $(cat "$elevated"))) -ge 480 ]; then elevate; fi
	req "$@"
}
pw=vm-test-password-1
token=$(grep -o 'token=[A-Za-z0-9_-]*' /root/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep_q '"username":"admin"' || fail "setup"
login() {
	rm -f "$jar" "$elevated"
	req -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | grep_q '"username":"admin"' || fail "login"
	elevate
}
login
follow() {
	local r id state offset=0
	r=$(api -X "$1" "$base$2" ${3:+-d "$3"})
	id=$(printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("job",{}).get("id",""))')
	[ -n "$id" ] || fail "$1 $2 not started: $r"
	: >/root/job.log
	for _ in $(seq 300); do
		r=$(api "$base/api/v1/jobs/$id/log?offset=$offset&wait=20")
		printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["log"], end="")' >>/root/job.log
		offset=$(printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["offset"])')
		state=$(printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["job"]["state"])')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	if [ "$state" != succeeded ]; then
		api "$base/api/v1/jobs/$id/log?offset=0" | python3 -c 'import json,sys; print(json.load(sys.stdin)["log"])' | tail -40 >&2 || true
		fail "$1 $2 ended $state: $(api "$base/api/v1/jobs/$id")"
	fi
}
recommended() { # software -> its recommended line ("" when not offered here)
	api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
r = [o["line"] for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["recommended"]]
print(r[0] if r else "")' "$1"
}
setype() { ls -Zd "$1" | awk '{print $1}' | cut -d: -f3; }
port_type() { semanage port -l | awk -v p="$2" -v pr="$1" '$2 == pr { for (i = 3; i <= NF; i++) { gsub(",", "", $i); if ($i == p) print $1 } }' | head -1; }

# PHP: the runner may write tmp/ and the writable directory, nothing else.
php=$(recommended php)
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"nginx\",\"line\":\"$(recommended nginx)\"},{\"software\":\"php\",\"line\":\"$php\"}]}"
systemctl is-active --quiet nginx.service || fail "nginx not running"
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"php.test\"],\"php\":\"$php\",\"writable\":[\"uploads\"]}"
runuser -u cw-php-test -- tee /srv/www/php-test/app/index.php >/dev/null <<'PHP'
<?php
$up = @file_put_contents(__DIR__ . '/uploads/x.txt', 'x') !== false ? 'yes' : 'no';
$code = @file_put_contents(__DIR__ . '/hack.php', 'x') !== false ? 'yes' : 'no';
$tmp = @file_put_contents(sys_get_temp_dir() . '/t.txt', 'x') !== false ? 'yes' : 'no';
session_start(); $_SESSION['n'] = 1; session_write_close();
echo "uploads=$up code=$code tmp=$tmp session=" . (session_status() === PHP_SESSION_NONE ? 'ok' : 'open');
PHP
out=$(curl -s -H 'Host: php.test' http://127.0.0.1/)
[ "$out" = "uploads=yes code=no tmp=yes session=ok" ] || fail "PHP site: $out"
[ "$(stat -c %U /srv/www/php-test/app/uploads/x.txt)" = cw-php-test-run ] || fail "upload owner"
if [ "$selinux" = yes ]; then
	[ "$(setype /srv/www/php-test/app)" = httpd_sys_content_t ] || fail "app dir type $(setype /srv/www/php-test/app)"
	[ "$(setype /srv/www/php-test/app/uploads)" = httpd_sys_rw_content_t ] || fail "uploads type $(setype /srv/www/php-test/app/uploads)"
	[ "$(setype /srv/www/php-test/tmp)" = httpd_sys_rw_content_t ] || fail "tmp type"
	[ "$(setype /srv/www/php-test/app/uploads/x.txt)" = httpd_sys_rw_content_t ] || fail "upload type"
fi
follow DELETE "/api/v1/sites/php-test?deleteFiles=true"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: php.test' http://127.0.0.1/index.php)" != 200 ] || fail "deleted site still served"
if [ "$selinux" = yes ]; then ! semanage fcontext -l -C | grep_q php-test || fail "the site's file rules were kept"; fi
step "PHP $php site: uploads/, tmp/ and sessions writable by cw-php-test-run, the code not$( [ "$selinux" = yes ] && echo '; httpd_sys_content_t, writable parts httpd_sys_rw_content_t'); deleted with its rules"

# An app behind nginx and a four-layer proxy in front of it.
node=$(recommended nodejs)
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"nodejs\",\"line\":\"$node\"}]}"
follow POST /api/v1/sites "{\"type\":\"app\",\"domains\":[\"app.test\"],\"writable\":[\"data\"],\"app\":{\"runtime\":\"nodejs\",\"line\":\"$node\",\"command\":\"node server.js\",\"port\":0}}"
runuser -u cw-app-test -- tee /srv/www/app-test/app/server.js >/dev/null <<'JS'
const fs = require('fs');
require('http').createServer((req, res) => {
  let data = 'refused';
  try { fs.writeFileSync(__dirname + '/data/d.txt', 'x'); data = 'written'; } catch (e) {}
  res.end('user=' + require('os').userInfo().username + ' data=' + data + '\n');
}).listen(process.env.PORT, '127.0.0.1');
JS
follow POST /api/v1/sites/app-test/restart
for _ in $(seq 60); do curl -sf -o /dev/null -H 'Host: app.test' http://127.0.0.1/ && break; sleep 1; done
out=$(curl -s -H 'Host: app.test' http://127.0.0.1/)
aport=$(api "$base/api/v1/sites/app-test" | python3 -c 'import json,sys; print(json.load(sys.stdin)["app"]["port"])')
if [ "$out" != "user=cw-app-test-run data=written" ]; then
	{
		echo "--- app port $aport, direct: $(curl -s -m 5 "http://127.0.0.1:$aport/" || echo 'no answer')"
		systemctl status cairn-app-app-test.service --no-pager -l | head -20
		journalctl -u cairn-app-app-test.service --no-pager -n 30
		ss -ltnp | grep -E ":$aport|nginx" || true
		ps -eZ | grep -E 'node|nginx|sh ' || true
		[ "$selinux" = yes ] && semanage port -l -C
		grep -rh proxy_pass /etc/nginx/cairn/sites/ || true
		ls /var/log/nginx/cairn/ || true
		tail -5 /var/log/nginx/cairn/*error* /var/log/nginx/error.log 2>/dev/null || true
		if [ "$selinux" = yes ]; then
			getsebool -a | grep -E 'httpd_(can_network|graceful|can_connect)' || true
			echo "--- with dontaudit rules off:"
			semodule -DB
			curl -s -o /dev/null -H 'Host: app.test' http://127.0.0.1/ || true
			sleep 2
			avcs | tail -10
			semodule -B
		fi
	} >&2
	fail "app through nginx: $out"
fi
appdomain=""
if [ "$selinux" = yes ]; then
	[ "$(port_type tcp "$aport")" = http_port_t ] || fail "app port $aport type: $(port_type tcp "$aport")"
	appdomain=$(ps -eZ | grep -w node | awk '{print $1}' | cut -d: -f3 | head -1)
fi
follow POST /api/v1/streams "{\"protocol\":\"tcp\",\"port\":9002,\"upstreams\":[\"127.0.0.1:$aport\"]}"
for _ in $(seq 20); do curl -sf -o /dev/null http://127.0.0.1:9002/ && break; sleep 1; done
curl -s http://127.0.0.1:9002/ | grep_q "user=cw-app-test-run" || fail "stream to the app"
if [ "$selinux" = yes ]; then [ "$(port_type tcp 9002)" = http_port_t ] || fail "stream port type"; fi
follow DELETE /api/v1/streams/tcp-9002
follow DELETE "/api/v1/sites/app-test?deleteFiles=true"
if [ "$selinux" = yes ]; then
	! semanage port -l -C | grep -wE "($aport|9002)" | grep_q tcp || fail "port labels kept: $(semanage port -l -C)"
fi
step "Node.js $node app behind nginx (data/ writable), stream tcp 9002 in front$( [ "$selinux" = yes ] && echo "; ports $aport and 9002 labeled http_port_t and released; the app ran as $appdomain"); deleted"

# Databases.
login
redis_sw=redis
[ -n "$(recommended redis)" ] || redis_sw=valkey
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"mariadb\",\"line\":\"$(recommended mariadb)\"},{\"software\":\"$redis_sw\",\"line\":\"$(recommended $redis_sw)\"}]}"
dbjson() { api "$base/api/v1/databases" | python3 -c "import json,sys; d=json.load(sys.stdin); e={x['id']:x for x in d['engines']}; print($1)"; }
[ "$(dbjson "e['mysql']['running'], e['redis']['running']")" = "True True" ] || fail "engines: $(api "$base/api/v1/databases")"
follow POST /api/v1/databases/mysql/dbs '{"name":"vmdb"}'
follow POST /api/v1/databases/mysql/users '{"name":"far","password":"far-password-1","hosts":["%"],"grants":[{"database":"vmdb","level":"all"}]}'
addr=$(hostname -I | awk '{print $1}')
follow PUT /api/v1/databases/mysql/remote '{"enabled":true}'
ss -ltn | grep_q '0.0.0.0:3306' || fail "MariaDB not listening on every address"
mariadb -ufar -pfar-password-1 -h "$addr" vmdb -e "SELECT 1" >/dev/null || fail "remote login to MariaDB"
follow PUT /api/v1/databases/mysql/remote '{"enabled":false}'
ss -ltn | grep_q '127.0.0.1:3306' || fail "MariaDB not back on 127.0.0.1"
follow POST /api/v1/databases/mysql/dumps '{"name":"vmdb"}'
follow POST /api/v1/databases/mysql/service '{"action":"restart"}'
follow PUT /api/v1/databases/redis/config '{"generatePassword":true}'
follow PUT /api/v1/databases/redis/remote '{"enabled":true}'
ss -ltn | grep_q '0.0.0.0:6379' || fail "Redis not listening on every address"
follow PUT /api/v1/databases/redis/remote '{"enabled":false}'
follow DELETE "/api/v1/databases/mysql/dbs/vmdb?dropOwner=true"
step "MariaDB: database, user, remote access on and off, dump, restart; $redis_sw: password, remote access on and off"

if [ "$selinux" = yes ]; then
	d=$(denials)
	[ -z "$d" ] || { printf '%s\n' "$d" | tail -30 >&2; fail "SELinux denied something"; }
	step "SELinux: no denials for nginx, php-fpm, mariadb, $redis_sw, the app or the helper"
fi
if [ "$family" = debian ]; then apt-get purge -y cairn >/root/purge.log 2>&1 || fail "purge"; else dnf remove -y cairn >/root/purge.log 2>&1 || fail "remove"; fi
echo "== PASS: ${PRETTY_NAME} (VM, sites and databases)"
