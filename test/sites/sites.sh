#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel,
# installs nginx and the system PHP through the panel, starts both by hand
# (no systemd), then creates a static, a PHP and a reverse proxy site
# through the API and checks what each serves, under which user PHP runs,
# what it may write and read, a second PHP line side by side, the move of
# the pools to the sockets of this version, stopping, logs and their
# rotation, changes and deletion.
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
apt-get install -y -qq curl procps python3 logrotate >/dev/null
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
pw=sites-test-password-1
token=$(cairn setup-link 2>&1 | grep -o 'token=[A-Za-z0-9_-]*' | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | keep_key | grep_q '"username":"admin"' || fail "setup"
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
# A reload of PHP-FPM lets the requests it serves finish (by default it
# kills them): the checks below count every request that fails.
graceful() { sed -i 's/^;\?process_control_timeout = .*/process_control_timeout = 10s/' "/etc/php/$1/fpm/php-fpm.conf"; }
graceful "$php"
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

# A browser sends the panel's cookies to a site on the panel's host name as
# well (cookies know no ports): nginx takes them out before the site's code,
# and leaves the site's own.
runuser -u cw-php-test -- tee "$app/cookies.php" >/dev/null <<'PHP'
<?php
header('Content-Type: text/plain');
echo 'cookies=[' . ($_SERVER['HTTP_COOKIE'] ?? '') . ']';
PHP
out=$(curl -s -H 'Host: php.test' -H 'Cookie: __Host-cairn_session=x; site=1; __Host-cairn_device=y; theme=dark' http://127.0.0.1/cookies.php)
[ "$out" = 'cookies=[site=1; theme=dark]' ] || fail "cookies the PHP site got: $out"
out=$(curl -s -H 'Host: php.test' -H 'Cookie: __Host-cairn_session=x' http://127.0.0.1/cookies.php)
[ "$out" = 'cookies=[]' ] || fail "cookies the PHP site got from the panel's alone: $out"
rm -f "$app/cookies.php"
step "PHP site php.test: the panel's own cookies do not reach the site's code, the site's do"

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

# A second PHP line next to the first, from the vendor repository: the
# lines share /run/php. A site moves to it and back while a site stays on
# each line; both masters keep running and every site keeps answering.
php2=$(api "$base/api/v1/software" | json '[o["line"] for s in d["software"] if s["id"]=="php" for o in s["offers"] if o["installable"] and not o["recommended"] and not o["endOfLife"] and not o.get("installed")][0]')
# The vendor repository is on the network: a failed download is tried again.
for try in 1 2 3; do
	if (follow POST /api/v1/software/install "{\"items\":[{\"software\":\"php\",\"line\":\"$php2\"}]}"); then break; fi
	[ "$try" != 3 ] || fail "PHP $php2 could not be installed"
	sleep 10
done
graceful "$php2"
# Some vendor builds leave the pid file to systemd; without systemd the
# helper finds the master by it.
"/usr/sbin/php-fpm$php2" --daemonize --pid "/run/php/php$php2-fpm.pid"
running() { kill -0 "$(cat "/run/php/php$1-fpm.pid" 2>/dev/null)" 2>/dev/null; }
serves() { [ "$(get "$1" /)" = "$2" ]; }
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"two.test\"],\"php\":\"$php2\"}"
follow POST /api/v1/sites "{\"type\":\"php\",\"domains\":[\"mover.test\"],\"php\":\"$php\"}"
for c in two-test mover-test; do
	printf '%s\n' '<?php echo PHP_MAJOR_VERSION . "." . PHP_MINOR_VERSION . "\n";' | runuser -u "cw-$c" -- tee "/srv/www/$c/app/index.php" >/dev/null
done
serves two.test "$php2" && serves mover.test "$php" || fail "the sites of two lines: $(get two.test /) $(get mover.test /)"
for to in "$php2" "$php"; do
	follow PUT /api/v1/sites/mover-test "{\"type\":\"php\",\"domains\":[\"mover.test\"],\"php\":\"$to\"}"
	running "$php" && running "$php2" || fail "a PHP-FPM master stopped when mover.test moved to $to"
	serves mover.test "$to" || fail "mover.test on $to answers: $(get mover.test /)"
	serves two.test "$php2" || fail "two.test, on $php2, answers: $(get two.test /)"
	get php.test / | grep_q -x "user=cw-php-test-run" || fail "php.test, on $php, answers: $(get php.test /)"
	[ -S "/run/php/cairn-mover-test-php$to.sock" ] || fail "no socket for mover.test on $to: $(ls /run/php)"
done
step "PHP $php and $php2 side by side: mover.test moved to $php2 and back, both masters running, every site answering"

# The pools of an earlier version listened on /run/php/cairn-<code>.sock.
# The helper's start moves them to the sockets that name their lines and
# no request fails meanwhile: PHP-FPM drops a socket it no longer has as
# soon as it reloads, and nginx uses the old one until it reloads.
for c in "mover-test:$php" "two-test:$php2"; do
	code=${c%%:*} line=${c#*:}
	sed -i "s#/run/php/cairn-$code-php$line.sock#/run/php/cairn-$code.sock#" "/etc/php/$line/fpm/pool.d/cairn-$code.conf" "/etc/nginx/cairn/sites/$code.conf"
done
for line in "$php" "$php2"; do kill -USR2 "$(cat "/run/php/php$line-fpm.pid")"; done
sleep 2
nginx -s reload
sleep 1
[ -S /run/php/cairn-mover-test.sock ] && serves mover.test "$php" && serves two.test "$php2" || fail "the earlier layout: $(ls /run/php)"
rm -f /tmp/stop-loop
(
	while [ ! -e /tmp/stop-loop ]; do
		for h in mover.test two.test; do curl -s -o /dev/null -w "$h %{http_code}\n" -H "Host: $h" http://127.0.0.1/ || true; done
	done
) >/tmp/codes.txt &
loop=$!
pkill -x cairn-helper
for _ in $(seq 50); do pgrep -x cairn-helper >/dev/null || break; sleep 0.1; done
/usr/lib/cairn/cairn-helper >>/tmp/helper.log 2>&1 &
moved() { [ -S "/run/php/cairn-$1-php$2.sock" ] && [ ! -e "/run/php/cairn-$1.sock" ] && ! grep_q "cairn-$1.old" "/etc/php/$2/fpm/pool.d/cairn-$1.conf"; }
for _ in $(seq 120); do
	if moved mover-test "$php" && moved two-test "$php2"; then break; fi
	sleep 0.5
done
sleep 1
touch /tmp/stop-loop
wait "$loop"
moved mover-test "$php" && moved two-test "$php2" || fail "the pools did not move: $(ls /run/php)"
grep_q "fastcgi_pass unix:/run/php/cairn-two-test-php$php2.sock;" /etc/nginx/cairn/sites/two-test.conf || fail "two-test.conf: $(grep fastcgi_pass /etc/nginx/cairn/sites/two-test.conf)"
total=$(wc -l </tmp/codes.txt)
bad=$(grep -vc ' 200$' /tmp/codes.txt || true)
[ "$total" -gt 50 ] && [ "$bad" = 0 ] || fail "while the pools moved, $bad of $total requests failed: $(grep -v ' 200$' /tmp/codes.txt | sort | uniq -c)"
step "the helper's start moved the pools of an earlier version to their lines' sockets: $total requests meanwhile, none failed"

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

# logrotate renames the logs and signals nginx: each worker then opens them
# again itself, which takes a way into their directory (in the group named
# like the workers' user: www-data, or nginx for nginx.org's nginx).
workers=$(awk '$1 == "user" { sub(";", "", $2); print $2; exit }' /etc/nginx/nginx.conf)
[ "$(stat -c '%U:%G %a' /var/log/nginx/cairn)" = "root:${workers:-www-data} 750" ] || fail "log directory: $(stat -c '%U:%G %a' /var/log/nginx/cairn), workers ${workers:-www-data}"
logrotate -f /etc/logrotate.d/cairn-nginx
sleep 1
for _ in 1 2 3; do code static.test / >/dev/null; done
sleep 1
[ -s /var/log/nginx/cairn/static-test.access.log ] || fail "nothing logged after the rotation: $(ls -l /var/log/nginx/cairn)"
if grep 'Permission denied' /var/log/nginx/error.log | grep_q /var/log/nginx/cairn/; then fail "nginx: $(grep 'Permission denied' /var/log/nginx/error.log | tail -2)"; fi
api "$base/api/v1/sites/static-test/log?kind=access" | json 'd["data"]' | grep_q '"status":200' || fail "the panel's log after the rotation"
step "logrotate: nginx's workers open the logs again (directory root:${workers:-www-data} 750) and the panel shows the new requests"

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
