#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the test VM (see run.sh), with systemd as PID 1. The
# test releases are served at http://10.0.2.2:8080/releases. Covers what the
# container tests cannot: the systemd units and their hardening, socket
# activation of the helper, a package job run by the helper, the online
# update in its own transient unit with the services restarted by the
# package, the rollback of a failed update, and a clean purge.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input: with pipefail, grep -q
# stopping at the first match can kill the writer with SIGPIPE and fail the
# whole pipeline at random.
grep_q() { grep "$@" >/dev/null; }

R=http://10.0.2.2:8080/releases
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
# journal_has PATTERN: searched in a saved copy, because with pipefail
# "journalctl | grep -q" fails when grep stops early and journalctl gets
# SIGPIPE.
journal_has() {
	journalctl --no-pager >/root/journal.txt 2>/dev/null || true
	grep -q "$1" /root/journal.txt
}
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	tail -20 /root/install.log 2>/dev/null >&2 || true
	journalctl -u cairn-web -u cairn-helper -u 'cairn-update-*' --no-pager -n 60 >&2 || true
	exit 1
}
echo "== ${PRETTY_NAME} (VM, systemd $(systemctl --version | awk 'NR==1 {print $2}'))"
export DEBIAN_FRONTEND=noninteractive

is_version() { [ "$(dpkg-query -W -f '${Version} ${db:Status-Abbrev}' cairn 2>/dev/null)" = "$1-1 ii " ]; }
updates=/var/lib/cairn-helper/updates

command -v curl >/dev/null || { apt-get update -qq && apt-get install -y -qq curl >/dev/null; }
curl -fsSL "$R/latest/download/install.sh" -o /root/install.sh
sh /root/install.sh --mirror "$R" --lang zh-CN --yes >/root/install.log 2>&1 || fail "install.sh"
is_version 0.1.0 || fail "0.1.0 not installed"
[ ! -e /etc/apt/sources.list.d/cairn.sources ] || fail "a package repository was added"
systemctl is-active --quiet cairn-web.service || fail "cairn-web is not running"
systemctl is-active --quiet cairn-helper.socket || fail "helper socket is not listening"
step "install.sh (real systemd): 0.1.0 installed, cairn-web running, helper socket listening"

[ "$(systemctl show -p User --value cairn-web.service)" = cairn ] || fail "cairn-web does not run as cairn"
[ "$(systemctl show -p NoNewPrivileges --value cairn-web.service)" = yes ] || fail "NoNewPrivileges not set"
[ "$(stat -c '%U:%G %a' /run/cairn/helper.sock)" = "root:cairn 660" ] || fail "helper socket ownership/mode"
web_exposure=$(systemd-analyze security cairn-web.service 2>/dev/null | tail -1 | sed 's/.*: //')
helper_exposure=$(systemd-analyze security cairn-helper.service 2>/dev/null | tail -1 | sed 's/.*: //')
step "hardening: cairn-web as cairn with NoNewPrivileges; exposure web ${web_exposure}, helper ${helper_exposure}"

cat >>/etc/cairn/config.toml <<EOF

[cloud]
enabled = true
base_url = "$R/download/feed"
channel = "stable"
trusted_keys = ["$TRUSTED_KEY"]
EOF
systemctl restart cairn-web.service
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
wait_web() {
	for _ in $(seq 120); do curl -sk -o /dev/null "$base/api/v1/meta" && return 0; sleep 1; done
	fail "web did not answer"
}
wait_web
jar=/root/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
pw=vm-test-password-1
token=$(grep -o 'token=[A-Za-z0-9_-]*' /root/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"zh-CN\"}" |
	grep -q '"username":"admin"' || fail "setup"
login() {
	rm -f "$jar"
	api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | grep_q '"username":"admin"' || fail "login"
	api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
}
login
systemctl is-active --quiet cairn-helper.service || fail "helper was not started through its socket"
api "$base/api/v1/ui/nav" | grep_q '"key":"software"' || fail "menu"
step "first-run setup through the API; helper started on demand by socket activation"

# A package job: the web process starts an operation in the root helper,
# which runs the real apt-get update; the job follows its log.
job=$(api -X POST "$base/api/v1/software/refresh" | sed -n 's/.*"job":{"id":"\([0-9a-f]*\)".*/\1/p')
[ -n "$job" ] || fail "refresh job not started"
state="" offset=0
: >/root/job.log
for _ in $(seq 120); do
	r=$(api "$base/api/v1/jobs/$job/log?offset=$offset&wait=10")
	printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["log"], end="")' >>/root/job.log
	offset=$(printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["offset"])')
	state=$(printf '%s' "$r" | python3 -c 'import json,sys; print(json.load(sys.stdin)["job"]["state"])')
	case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
done
[ "$state" = succeeded ] || { cat /root/job.log; fail "refresh job ended $state"; }
grep -q '^\$ apt-get update' /root/job.log && grep -qE '^(Hit|Get):' /root/job.log || { cat /root/job.log; fail "job log"; }
journal_has 'operation finished.*kind=pkg.refresh.*state=succeeded' || fail "helper did not log the operation"
step "software refresh job: real apt-get update run by the helper, live log followed to the end"

# A PHP site with the real systemd: the panel installs nginx and PHP (which
# systemd starts), the site's pool and server block are checked and the
# services reloaded through systemctl.
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
	[ "$state" = succeeded ] || { tail -40 /root/job.log; fail "$1 $2 ended $state"; }
}
recommended() {
	api "$base/api/v1/software" | python3 -c '
import json, sys
d = json.load(sys.stdin)
print([o["line"] for s in d["software"] if s["id"] == sys.argv[1] for o in s["offers"] if o["recommended"]][0])' "$1"
}
php=$(recommended php)
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"nginx\",\"line\":\"$(recommended nginx)\"},{\"software\":\"php\",\"line\":\"$php\"}]}"
systemctl is-active --quiet nginx.service && systemctl is-active --quiet "php$php-fpm.service" || fail "nginx or php-fpm not running"
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"php.test\"],\"php\":\"$php\",\"writable\":[\"uploads\"]}"
grep_q "^\$ systemctl reload php$php-fpm" /root/job.log && grep_q '^\$ systemctl reload nginx' /root/job.log || { cat /root/job.log; fail "services not reloaded through systemd"; }
runuser -u cw-php-test -- tee /srv/www/php-test/app/index.php >/dev/null <<'PHP'
<?php echo 'user=' . posix_getpwuid(posix_geteuid())['name'];
PHP
out=$(curl -s -H 'Host: php.test' http://127.0.0.1/)
[ "$out" = "user=cw-php-test-run" ] || fail "PHP site served: $out"
follow DELETE "/api/v1/sites/php-test?deleteFiles=true"
[ "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: php.test' http://127.0.0.1/index.php)" != 200 ] || fail "deleted site still served"
step "PHP site with systemd: pool and server block checked, php$php-fpm and nginx reloaded by systemctl, PHP runs as cw-php-test-run; deleted"

# An app: Node.js from the panel, run by systemd as the site's runner in a
# locked-down unit, restarted when it dies, a four-layer proxy in front.
node=$(recommended nodejs)
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"nodejs\",\"line\":\"$node\"}]}"
follow POST /api/v1/sites "{\"type\":\"app\",\"domains\":[\"app.test\"],\"writable\":[\"data\"],\"app\":{\"runtime\":\"nodejs\",\"line\":\"$node\",\"command\":\"node server.js\",\"port\":0,\"env\":[{\"name\":\"GREETING\",\"value\":\"hello \\\"world\\\" \$HOME 100%\"}]}}"
runuser -u cw-app-test -- tee /srv/www/app-test/app/server.js >/dev/null <<'JS'
const fs = require('fs');
require('http').createServer((req, res) => {
  const tryWrite = (p) => { try { fs.writeFileSync(p, 'x'); return 'written'; } catch (e) { return 'refused'; } };
  res.end(['user=' + require('os').userInfo().username, 'node=' + process.version,
    'code=' + tryWrite(__dirname + '/hacked.js'), 'data=' + tryWrite(__dirname + '/data/d.txt'),
    'greeting=' + process.env.GREETING].join('\n') + '\n');
}).listen(process.env.PORT, '127.0.0.1', () => console.log('listening on ' + process.env.PORT));
JS
follow POST /api/v1/sites/app-test/restart
for _ in $(seq 30); do curl -s -o /dev/null -H 'Host: app.test' http://127.0.0.1/ && break; sleep 1; done
out=$(curl -s -H 'Host: app.test' http://127.0.0.1/)
for want in "user=cw-app-test-run" "node=v$node." "code=refused" "data=written" 'greeting=hello "world" $HOME 100%'; do
	printf '%s\n' "$out" | grep_q -F -- "$want" || fail "app: want $want, got: $out"
done
[ "$(systemctl show -p ProtectSystem --value cairn-app-app-test.service)" = strict ] || fail "unit not locked down"
[ "$(stat -c %a /etc/cairn/apps/app-test.env)" = 600 ] || fail "env file readable by others"
pid=$(systemctl show -p MainPID --value cairn-app-app-test.service)
kill -9 "$pid"
for _ in $(seq 30); do
	new=$(systemctl show -p MainPID --value cairn-app-app-test.service)
	[ "$new" != 0 ] && [ "$new" != "$pid" ] && break
	sleep 1
done
[ "$new" != 0 ] && [ "$new" != "$pid" ] || fail "systemd did not restart the app"
api "$base/api/v1/sites/app-test/app" | python3 -c 'import json,sys; d=json.load(sys.stdin); sys.exit(0 if d["activeState"]=="active" and d["restarts"]>=1 else 1)' || fail "app status: $(api "$base/api/v1/sites/app-test/app")"
api "$base/api/v1/sites/app-test/app/log" | grep_q 'listening on' || fail "app log: $(api "$base/api/v1/sites/app-test/app/log")"
port=$(api "$base/api/v1/sites/app-test" | python3 -c 'import json,sys; print(json.load(sys.stdin)["app"]["port"])')
follow POST /api/v1/streams "{\"protocol\":\"tcp\",\"port\":9002,\"upstreams\":[\"127.0.0.1:$port\"]}"
for _ in $(seq 10); do curl -s -o /dev/null http://127.0.0.1:9002/ && break; sleep 1; done
curl -s http://127.0.0.1:9002/ | grep_q "user=cw-app-test-run" || fail "stream to the app"
follow DELETE /api/v1/streams/tcp-9002
follow DELETE "/api/v1/sites/app-test?deleteFiles=true"
[ "$(systemctl show -p LoadState --value cairn-app-app-test.service)" = not-found ] || fail "app unit left"
step "app with systemd: Node.js $node as cw-app-test-run, code read-only, data/ writable, variables quoted, restarted after kill -9, log read; stream tcp 9002 in front; deleted"

# Databases with systemd: the packages start their services; remote access
# changes the listening address and restarts them through systemctl.
login
pg=$(recommended postgresql)
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"mariadb\",\"line\":\"$(recommended mariadb)\"},{\"software\":\"postgresql\",\"line\":\"$pg\"},{\"software\":\"redis\",\"line\":\"$(recommended redis)\"}]}"
systemctl is-active --quiet mariadb.service && systemctl is-active --quiet redis-server.service || fail "database services not running"
dbjson() { api "$base/api/v1/databases" | python3 -c "import json,sys; d=json.load(sys.stdin); e={x['id']:x for x in d['engines']}; print($1)"; }
[ "$(dbjson "e['mysql']['running'], e['postgresql-$pg']['running'], e['redis']['running']")" = "True True True" ] || fail "engines: $(api "$base/api/v1/databases")"
follow POST /api/v1/databases/mysql/dbs '{"name":"vmdb"}'
follow POST /api/v1/databases/mysql/users '{"name":"far","password":"far-password-1","hosts":["%"],"grants":[{"database":"vmdb","level":"all"}]}'
addr=$(hostname -I | awk '{print $1}')
follow PUT /api/v1/databases/mysql/remote '{"enabled":true}'
ss -ltn | grep_q '0.0.0.0:3306' || fail "MariaDB not listening on every address"
mariadb -ufar -pfar-password-1 -h "$addr" vmdb -e "SELECT 1" >/dev/null || fail "remote login to MariaDB"
follow PUT /api/v1/databases/mysql/remote '{"enabled":false}'
ss -ltn | grep_q '127.0.0.1:3306' || fail "MariaDB not back on 127.0.0.1"
follow POST /api/v1/databases/mysql/service '{"action":"restart"}'
systemctl is-active --quiet mariadb.service || fail "MariaDB not running after restart"
follow PUT "/api/v1/databases/postgresql-$pg/remote" '{"enabled":true}'
ss -ltn | grep_q '0.0.0.0:5432' || fail "PostgreSQL not listening on every address"
systemctl is-active --quiet "postgresql@$pg-main.service" || fail "PostgreSQL cluster unit"
follow PUT "/api/v1/databases/postgresql-$pg/remote" '{"enabled":false}'
follow PUT /api/v1/databases/redis/config '{"generatePassword":true}'
follow PUT /api/v1/databases/redis/remote '{"enabled":true}'
ss -ltn | grep_q '0.0.0.0:6379' || fail "Redis not listening on every address"
follow PUT /api/v1/databases/redis/remote '{"enabled":false}'
follow DELETE "/api/v1/databases/mysql/dbs/vmdb?dropOwner=true"
step "databases with systemd: MariaDB remote on and off (restarted by systemctl, remote login works), service restart; PostgreSQL $pg remote through its cluster unit; Redis password and remote access"

# Online update in a transient unit: the package restarts both services.
api -X POST "$base/api/v1/updates/check" | grep_q '"status":"available"' || fail "no update offered"
helper_pid=$(systemctl show -p MainPID --value cairn-helper.service)
code=$(api -o /root/apply.json -w '%{http_code}' -X POST "$base/api/v1/updates/apply" -d '{"version":"0.2.0"}')
[ "$code" = 202 ] || fail "apply 0.2.0: $code $(cat /root/apply.json)"
# wait_update: the web process restarts in the middle, so requests may fail.
wait_update() {
	for _ in $(seq 300); do
		s=$(api "$base/api/v1/updates" 2>/dev/null | sed -n 's/.*"job":{"state":"\([a-z]*\)".*/\1/p' || true)
		case "$s" in succeeded | failed) echo "$s"; return ;; esac
		sleep 2
	done
	echo timeout
}
[ "$(wait_update)" = succeeded ] || fail "update to 0.2.0"
is_version 0.2.0 || fail "0.2.0 not installed"
journal_has 'Started cairn-update-[0-9]*.service' || fail "no transient update unit"
wait_web
api "$base/api/v1/meta" | grep_q '"version":"0.2.0"' || fail "cairn-web was not restarted into 0.2.0"
[ "$(systemctl show -p MainPID --value cairn-helper.service)" != "$helper_pid" ] || fail "helper was not restarted"
grep -q 0.2.0 "$updates/current" || fail "rollback pointer"
step "online update to 0.2.0 in a transient unit; both services restarted into 0.2.0"

login
code=$(api -o /root/apply.json -w '%{http_code}' -X POST "$base/api/v1/updates/apply" -d '{"version":"0.3.0"}')
[ "$code" = 202 ] || fail "apply 0.3.0: $code $(cat /root/apply.json)"
[ "$(wait_update)" = failed ] || fail "the uninstallable 0.3.0 did not fail"
api "$base/api/v1/updates" | grep_q 'the previous version was restored' || fail "rollback not reported"
is_version 0.2.0 || fail "0.2.0 not restored"
wait_web
systemctl is-active --quiet cairn-web.service || fail "cairn-web not running after the rollback"
step "update to 0.3.0 failed and 0.2.0 was restored; panel running"

apt-get purge -y cairn >/root/purge.log 2>&1 || fail "purge"
for u in cairn-web.service cairn-helper.service cairn-helper.socket; do
	[ "$(systemctl show -p LoadState --value "$u")" = not-found ] || fail "$u left behind"
done
[ ! -e /etc/cairn ] && [ ! -e /var/lib/cairn ] && [ ! -e /var/lib/cairn-helper ] && [ ! -e /run/cairn/helper.sock ] || fail "purge left files behind"
! pgrep -f 'cairn-(web|helper)' >/dev/null || fail "processes left running"
step "purged: units, files, socket and processes gone"
echo "== PASS: ${PRETTY_NAME} (VM)"
