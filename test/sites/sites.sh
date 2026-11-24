#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# installs nginx and the system PHP through the panel, starts both by hand
# (no systemd), then creates a static, a PHP and a reverse proxy site
# through the API and checks what each serves, under which user PHP runs,
# what it may write and read, stopping, logs, changes and deletion.
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
echo "== ${PRETTY_NAME}"

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
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=sites-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep_q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"

# follow METHOD PATH [BODY]: starts a job and waits for it to succeed.
follow() {
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
	[ "$state" = succeeded ] || fail "$1 $2 ended $state: $(printf '%s' "$r" | json 'd["job"].get("error")')"
}
# get HOST PATH: the body served for a host on port 80.
get() { curl -s -H "Host: $1" "http://127.0.0.1$2"; }
code() { curl -s -o /dev/null -w '%{http_code}' -H "Host: $1" "http://127.0.0.1$2"; }

# nginx and the recommended PHP line, through the panel.
php=$(api "$base/api/v1/software" | json '[o["line"] for s in d["software"] if s["id"]=="php" for o in s["offers"] if o["recommended"]][0]')
nginx=$(api "$base/api/v1/software" | json '[o["line"] for s in d["software"] if s["id"]=="nginx" for o in s["offers"] if o["recommended"]][0]')
r=$(api "$base/api/v1/sites")
[ "$(printf '%s' "$r" | json 'd["nginx"]["installed"]')" = False ] || fail "nginx reported before it is installed"
follow POST /api/v1/software/install "{\"items\":[{\"software\":\"nginx\",\"line\":\"$nginx\"},{\"software\":\"php\",\"line\":\"$php\"}]}"
mkdir -p /run/php
nginx
"/usr/sbin/php-fpm$php" --daemonize
step "nginx $nginx and PHP $php installed through the panel, started by hand"
r=$(api "$base/api/v1/sites")
[ "$(printf '%s' "$r" | json 'd["nginx"]["running"]')" = True ] || fail "nginx not reported running: $r"
printf '%s' "$r" | json 'd["php"]' | grep_q "$php" || fail "PHP $php not offered to sites: $r"

# Static site.
follow POST /api/v1/sites '{"type":"static","domains":["static.test","www.static.test"]}'
get static.test / | grep_q "static.test is ready" || fail "static placeholder: $(get static.test /)"
[ "$(code www.static.test /.git/config)" = 403 ] || fail "hidden files are served"
step "static site static.test: placeholder page, hidden files refused"

# PHP site with the WordPress rule and one writable directory.
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"php.test\"],\"php\":\"$php\",\"rewrite\":\"wordpress\",\"writable\":[\"uploads\"]}"
app=/srv/www/php-test/app
[ "$(stat -c '%U:%G %a' /srv/www/php-test)" = "root:cw-php-test 750" ] || fail "site dir: $(stat -c '%U:%G %a' /srv/www/php-test)"
[ "$(stat -c '%U:%G %a' $app)" = "cw-php-test:cw-php-test 2750" ] || fail "app dir: $(stat -c '%U:%G %a' $app)"
[ "$(stat -c '%U:%G %a' $app/uploads)" = "cw-php-test-run:cw-php-test 2770" ] || fail "uploads: $(stat -c '%U:%G %a' $app/uploads)"
# The owner deploys the code.
runuser -u cw-php-test -- tee $app/index.php >/dev/null <<'PHP'
<?php
header('Content-Type: text/plain');
echo 'user=' . posix_getpwuid(posix_geteuid())['name'] . "\n";
echo 'path=' . $_SERVER['REQUEST_URI'] . "\n";
echo 'upload=' . (@file_put_contents(__DIR__ . '/uploads/u.txt', 'ok') ? 'ok' : 'refused') . "\n";
echo 'code=' . (@file_put_contents(__DIR__ . '/hacked.php', 'x') ? 'written' : 'refused') . "\n";
echo 'passwd=' . (@file_get_contents('/etc/passwd') === false ? 'refused' : 'read') . "\n";
PHP
out=$(get php.test /hello/world)
for want in "user=cw-php-test-run" "path=/hello/world" "upload=ok" "code=refused" "passwd=refused"; do
	printf '%s' "$out" | grep_q -x "$want" || fail "PHP site: want $want, got: $out"
done
[ ! -e $app/hacked.php ] && [ "$(stat -c %U $app/uploads/u.txt)" = cw-php-test-run ] || fail "writes"
step "PHP site php.test: runs as cw-php-test-run, writes uploads/ only, open_basedir holds, WordPress rule routes /hello/world"

# A second PHP site on the same line (one FPM master, one opcache), laid
# out like Laravel: web root public/, public/storage linking into the
# runner's storage, links allowed between the site's own users.
lara_spec="{\"type\":\"php\",\"domains\":[\"lara.test\"],\"php\":\"$php\",\"root\":\"public\",\"writable\":[\"storage/app/public\"],\"allowSymlinks\":true}"
follow POST /api/v1/sites "$lara_spec"
lara=/srv/www/lara-test/app
runuser -u cw-lara-test -- mkdir $lara/public
runuser -u cw-lara-test -- tee $lara/config.php >/dev/null <<'PHP'
<?php define('LARA_DB_PASSWORD', 'lara-secret-value');
PHP
runuser -u cw-lara-test -- tee $lara/public/index.php >/dev/null <<'PHP'
<?php require __DIR__ . '/../config.php'; echo "lara-ok\n";
PHP
# opcache leaves out files changed in the last 2 seconds.
touch -d '1 hour ago' $lara/config.php
runuser -u cw-php-test -- tee $app/probe.php >/dev/null <<'PHP'
<?php
@include '/srv/www/lara-test/app/config.php';
echo defined('LARA_DB_PASSWORD') ? "included " . LARA_DB_PASSWORD . "\n" : "blocked\n";
echo is_array(@opcache_get_status()) ? "status read\n" : "status refused\n";
PHP
for _ in 1 2 3; do
	get lara.test / | grep_q -x lara-ok || fail "lara.test: $(get lara.test /)"
	out=$(get php.test /probe.php)
	printf '%s' "$out" | grep_q -x blocked || fail "a site read another site's script from the shared opcache: $out"
done
printf '%s' "$out" | grep_q -x "status refused" || fail "a site may use the opcache API: $out"
step "same PHP line: php.test cannot include lara.test's cached config or use the opcache API"

# Links: the site's own public/storage is served once the site is saved;
# a link to another site's files never is, nor a web root that is a link.
echo avatar-marker | runuser -u cw-lara-test-run -- tee $lara/storage/app/public/avatar.txt >/dev/null
runuser -u cw-lara-test -- ln -s $lara/storage/app/public $lara/public/storage
follow PUT /api/v1/sites/lara-test "$lara_spec"
[ "$(stat -c %U $lara/public/storage)" = cw-lara-test-run ] || fail "public/storage not aligned: $(stat -c %U $lara/public/storage)"
get lara.test /storage/avatar.txt | grep_q -x avatar-marker || fail "public/storage: $(code lara.test /storage/avatar.txt)"
runuser -u cw-lara-test-run -- ln -s /srv/www/php-test/app/index.php $lara/storage/app/public/leak.txt
runuser -u cw-lara-test -- ln -s /srv/www/php-test/app/uploads/u.txt $lara/public/leak2.txt
ln -s /srv/www/php-test/app/uploads/u.txt $lara/public/leak3.txt
follow PUT /api/v1/sites/lara-test "$lara_spec"
for p in /storage/leak.txt /leak2.txt /leak3.txt; do
	case "$(code lara.test $p)" in 403 | 404) ;; *) fail "lara.test$p serves another site's file: $(get lara.test $p)" ;; esac
done
mv $lara/public $lara/public.real
runuser -u cw-lara-test -- ln -s /srv/www/php-test/app $lara/public
case "$(code lara.test /uploads/u.txt)" in 403 | 404) ;; *) fail "a web root linked to another site serves it: $(get lara.test /uploads/u.txt)" ;; esac
rm $lara/public && mv $lara/public.real $lara/public
get lara.test /storage/avatar.txt | grep_q -x avatar-marker || fail "public/storage after the checks"
grep_q 'disable_symlinks if_not_owner from=/srv/www/lara-test/app;' /etc/nginx/cairn/sites/lara-test.conf || fail "lara-test.conf"
step "lara.test: own public/storage served after saving; links to php.test's files and a linked web root refused"

# Reverse proxy with WebSocket upgrades.
mkdir -p /tmp/upstream && echo "upstream marker" >/tmp/upstream/index.html
(cd /tmp/upstream && python3 -m http.server 8081 --bind 127.0.0.1 >/tmp/upstream.log 2>&1 &)
for _ in $(seq 50); do curl -s -o /dev/null http://127.0.0.1:8081/ && break; sleep 0.1; done
follow POST /api/v1/sites '{"type":"proxy","domains":["proxy.test"],"proxy":{"url":"http://127.0.0.1:8081","webSocket":true}}'
get proxy.test / | grep_q "upstream marker" || fail "proxy: $(get proxy.test /)"
grep_q 'proxy_set_header Upgrade' /etc/nginx/cairn/sites/proxy-test.conf || fail "WebSocket headers missing"
step "reverse proxy proxy.test -> 127.0.0.1:8081 (WebSocket headers set)"

# A four-layer proxy to the same upstream; stopped, it does not listen.
follow POST /api/v1/streams '{"protocol":"tcp","port":9001,"upstreams":["127.0.0.1:8081"]}'
curl -s http://127.0.0.1:9001/ | grep_q "upstream marker" || fail "stream: $(curl -s http://127.0.0.1:9001/)"
follow POST /api/v1/streams/tcp-9001/stop
if curl -s --max-time 3 -o /dev/null http://127.0.0.1:9001/; then fail "a stopped stream still answers"; fi
follow DELETE /api/v1/streams/tcp-9001
[ ! -e /etc/nginx/cairn/stream/tcp-9001.conf ] || fail "stream file left"
step "four-layer proxy tcp 9001 -> 127.0.0.1:8081; stopped, it no longer listens; deleted"

# Apps are systemd units: without systemd they are refused clearly.
r=$(api -X POST "$base/api/v1/sites" -d '{"type":"app","domains":["app.test"],"app":{"command":"python3 -m http.server $PORT","port":0}}')
id=$(printf '%s' "$r" | json 'd["job"]["id"]')
for _ in $(seq 60); do
	state=$(api "$base/api/v1/jobs/$id" | json 'd["job"]["state"]')
	case "$state" in succeeded | failed) break ;; esac
	sleep 0.5
done
[ "$(api "$base/api/v1/jobs/$id" | json 'd["job"]["error"]["code"]')" = app.systemd_required ] || fail "app without systemd: $(api "$base/api/v1/jobs/$id")"
step "an app site without systemd is refused (app.systemd_required)"

# Stop and start; logs.
follow POST /api/v1/sites/static-test/stop
[ "$(code static.test /)" = 503 ] || fail "stopped site answers $(code static.test /)"
follow POST /api/v1/sites/static-test/start
[ "$(code static.test /)" = 200 ] || fail "started site answers $(code static.test /)"
sleep 1
api "$base/api/v1/sites/static-test/log?kind=access" | json 'd["data"]' | grep_q '"status":503' || fail "access log: $(api "$base/api/v1/sites/static-test/log")"
step "stop answers 503, start serves again; the access log shows both"

# A change of domains; a broken one leaves everything as it was.
follow PUT /api/v1/sites/php-test "{\"type\":\"php\",\"domains\":[\"php2.test\"],\"php\":\"$php\",\"rewrite\":\"wordpress\",\"writable\":[\"uploads\"]}"
get php2.test / | grep_q "user=cw-php-test-run" || fail "new domain"
get php.test / | grep_q "user=" && fail "old domain still served"
r=$(api -X PUT "$base/api/v1/sites/proxy-test" -d '{"type":"proxy","domains":["proxy.test"],"proxy":{"url":"http://no-such-host.invalid:81"}}')
id=$(printf '%s' "$r" | json 'd["job"]["id"]')
for _ in $(seq 60); do
	state=$(api "$base/api/v1/jobs/$id" | json 'd["job"]["state"]')
	case "$state" in succeeded | failed) break ;; esac
	sleep 1
done
[ "$state" = failed ] || fail "an unresolvable upstream was accepted"
api "$base/api/v1/sites/proxy-test" | json 'd["errorCode"]' | grep_q site.config_invalid || fail "error code: $(api "$base/api/v1/sites/proxy-test")"
get proxy.test / | grep_q "upstream marker" || fail "the failed change broke the site"
step "domain change applied; a change nginx -t refuses is rolled back (site keeps serving)"

# Delete, with files.
follow DELETE "/api/v1/sites/php-test?deleteFiles=true"
[ ! -e /srv/www/php-test ] && ! getent passwd cw-php-test >/dev/null && ! getent group cw-php-test >/dev/null || fail "files or users left"
[ ! -e "/etc/php/$php/fpm/pool.d/cairn-php-test.conf" ] || fail "pool left"
nginx -t 2>/dev/null || fail "nginx configuration broken after delete"
step "deleted php-test with its files, users and pool; nginx -t passes"
echo "== PASS: ${PRETTY_NAME}"
