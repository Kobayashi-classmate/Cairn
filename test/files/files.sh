#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs inside one throwaway container (see run.sh): installs the panel and
# a site directory owned like a real site's, then uses the file manager
# and the terminal through the API and checks the results on disk:
# ownership, uploads (tus), downloads, archives (also hostile ones, and 7z
# through unar where the system has it), the trash, permissions, search,
# fetching a URL, full-disk access, and terminals as the site owner and as
# root.
set -euo pipefail
# grep_q is "grep -q" that reads all of its input (pipefail and SIGPIPE).
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
start_helper() { /usr/lib/cairn/cairn-helper >/tmp/helper.log 2>&1 & for _ in $(seq 50); do [ -S /run/cairn/helper.sock ] && break; sleep 0.1; done; }
start_helper
runuser -u cairn -- /usr/bin/cairn-web >/tmp/web.log 2>&1 &
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
for _ in $(seq 100); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 0.1; done
jar=/tmp/jar
api() { curl -sk -c "$jar" -b "$jar" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
pw=files-test-password-1
token=$(grep -o 'token=[A-Za-z0-9_-]*' /tmp/install.log | head -1 | cut -d= -f2)
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | grep_q '"username":"admin"' || fail "setup"
api -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"

# start METHOD PATH [BODY]: starts a job, waits; prints "state code".
start() {
	local r id state offset=0
	r=$(api -X "$1" "$base$2" ${3:+-d "$3"})
	id=$(printf '%s' "$r" | json 'd.get("job",{}).get("id","")')
	[ -n "$id" ] || fail "$1 $2 not started: $r"
	follow_id "$id"
}
follow_id() {
	local r state offset=0
	: >/tmp/job.log
	for _ in $(seq 200); do
		r=$(api "$base/api/v1/jobs/$1/log?offset=$offset&wait=20")
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
owner() { stat -c '%U:%G' "$1"; }

# A site directory as the sites module makes it.
groupadd --system cw-demo
useradd --system --gid cw-demo --home-dir /srv/www/demo --no-create-home --shell /usr/sbin/nologin cw-demo
mkdir -p /srv/www/demo/app
chown -R cw-demo:cw-demo /srv/www/demo
chmod 2750 /srv/www/demo/app
S=/srv/www/demo/app

r=$(api "$base/api/v1/files/roots")
[ "$(printf '%s' "$r" | json '[x["available"] for x in d["roots"]]')" = "[True, True, False]" ] || fail "roots: $r"
r=$(api -X PUT "$base/api/v1/files/content" -d '{"root":"sites","path":"demo/app/index.html","content":"<h1>demo</h1>\n"}')
[ "$(owner $S/index.html)" = cw-demo:cw-demo ] || fail "new file owner $(owner $S/index.html): $r"
etag=$(api "$base/api/v1/files/content?root=sites&path=demo/app/index.html" | json 'd["etag"]')
api -X PUT "$base/api/v1/files/content" -d "{\"root\":\"sites\",\"path\":\"demo/app/index.html\",\"content\":\"<h1>v2</h1>\\n\",\"etag\":\"$etag\"}" >/dev/null
[ "$(api -X PUT "$base/api/v1/files/content" -d "{\"root\":\"sites\",\"path\":\"demo/app/index.html\",\"content\":\"x\",\"etag\":\"$etag\"}" | json 'd["error"]["code"]')" = file.changed ] || fail "stale save"
[ "$(cat $S/index.html)" = "<h1>v2</h1>" ] || fail "saved content"
api -X POST "$base/api/v1/files/mkdir" -d '{"root":"sites","dir":"demo/app","name":"img"}' >/dev/null
[ "$(owner $S/img)" = cw-demo:cw-demo ] && [ "$(stat -c %a $S/img)" = 2755 ] || fail "new directory $(owner $S/img) $(stat -c %a $S/img)"
step "editor: new files and directories belong to the site owner; saves detect changes made meanwhile"

# tus upload in two parts.
head -c 300000 /dev/urandom >/tmp/up.bin
meta="root $(printf sites | base64),dir $(printf demo/app/img | base64),filename $(printf up.bin | base64)"
loc=$(curl -sk -b "$jar" -D - -o /dev/null -X POST -H 'X-Cairn-Request: 1' -H 'Tus-Resumable: 1.0.0' -H 'Upload-Length: 300000' \
	-H "Upload-Metadata: $meta" "$base/api/v1/files/uploads" | tr -d '\r' | sed -n 's/^Location: //Ip')
[ -n "$loc" ] || fail "upload not created"
patch() { curl -sk -b "$jar" -D - -o /dev/null -X PATCH -H 'X-Cairn-Request: 1' -H 'Tus-Resumable: 1.0.0' \
	-H 'Content-Type: application/offset+octet-stream' -H "Upload-Offset: $1" --data-binary @"$2" "$base$loc" | tr -d '\r'; }
head -c 100000 /tmp/up.bin >/tmp/p1
tail -c 200000 /tmp/up.bin >/tmp/p2
patch 0 /tmp/p1 | grep_q -i '^Upload-Offset: 100000' || fail "first part"
jid=$(patch 100000 /tmp/p2 | sed -n 's/^Cairn-Job: //Ip')
[ -n "$jid" ] || fail "no job after the last part"
[ "$(follow_id "$jid")" = "succeeded " ] || fail "placing the upload"
cmp -s /tmp/up.bin $S/img/up.bin || fail "uploaded content: $(wc -c <$S/img/up.bin) bytes, $(cmp /tmp/up.bin $S/img/up.bin 2>&1 | head -1)"
[ "$(owner $S/img/up.bin)" = cw-demo:cw-demo ] || fail "uploaded owner"
[ -z "$(ls /var/lib/cairn/uploads)" ] || fail "upload leftovers: $(ls /var/lib/cairn/uploads)"
curl -sk -b "$jar" -o /tmp/down.bin "$base/api/v1/files/download?root=sites&path=demo/app/img/up.bin"
cmp -s /tmp/up.bin /tmp/down.bin || fail "download"
step "resumable upload in two parts, placed for the site owner; downloaded back intact"

# Archives both ways, then a hostile one.
follow POST /api/v1/files/compress '{"root":"sites","dir":"demo/app","names":["index.html","img"],"format":"tar.zst","name":"site.tar.zst"}'
follow POST /api/v1/files/compress '{"root":"sites","dir":"demo/app","names":["index.html","img"],"format":"zip","name":"site.zip"}'
api -X POST "$base/api/v1/files/mkdir" -d '{"root":"sites","dir":"demo/app","name":"restored"}' >/dev/null
follow POST /api/v1/files/extract '{"root":"sites","path":"demo/app/site.tar.zst","dest":"demo/app/restored"}'
cmp -s /tmp/up.bin $S/restored/img/up.bin || fail "tar.zst round trip"
follow POST /api/v1/files/extract '{"root":"sites","path":"demo/app/site.zip","dest":"demo/app/restored","overwrite":true}'
[ "$(owner $S/restored/img/up.bin)" = cw-demo:cw-demo ] || fail "extracted owner"
python3 -I - "$S/evil.tar" <<'PY'
import io, sys, tarfile
with tarfile.open(sys.argv[1], "w") as t:
    def add(name, data=b"x", **kw):
        i = tarfile.TarInfo(name); i.size = len(data)
        for k, v in kw.items(): setattr(i, k, v)
        t.addfile(i, io.BytesIO(data) if i.isreg() else None)
    add("../../../../tmp/escaped")
    add("root-link", type=tarfile.SYMTYPE, linkname="/")
    add("root-link/tmp/through-link")
    add("suid", mode=0o4755)
PY
chown cw-demo:cw-demo $S/evil.tar
follow POST /api/v1/files/extract '{"root":"sites","path":"demo/app/evil.tar","dest":"demo/app"}'
[ ! -e /tmp/escaped ] && [ ! -e /tmp/through-link ] && [ ! -L $S/root-link ] || fail "hostile archive escaped"
[ "$(stat -c %a $S/suid)" = 755 ] || fail "setuid kept"
step "zip and tar.zst round trips; a hostile tar stays inside, without links out or setuid"

if [ "$ID" != rocky ] && [ "$ID" != almalinux ]; then
	line=$(api "$base/api/v1/software" | json '[o["line"] for s in d["software"] if s["id"]=="unar" for o in s["offers"] if o["recommended"]][0]')
	follow POST /api/v1/software/install "{\"items\":[{\"software\":\"unar\",\"line\":\"$line\"}]}"
	[ "$(api "$base/api/v1/files/roots" | json 'd["archivers"]["sevenZip"]')" = True ] || fail "unar not seen"
	apt-get install -y -qq p7zip-full >/dev/null 2>&1 || apt-get install -y -qq 7zip >/dev/null
	(cd /tmp && mkdir -p seven/inner && echo seven >seven/inner/s.txt && (7z a -bd -y s.7z seven >/dev/null 2>&1 || 7zz a -bd -y s.7z seven >/dev/null))
	cp /tmp/s.7z $S/ && chown cw-demo:cw-demo $S/s.7z
	follow POST /api/v1/files/extract '{"root":"sites","path":"demo/app/s.7z","dest":"demo/app"}'
	[ "$(cat $S/seven/inner/s.txt)" = seven ] && [ "$(owner $S/seven/inner/s.txt)" = cw-demo:cw-demo ] || fail "7z extract"
	step "unar $line installed through the panel; a 7z archive extracted for the site owner"
fi

# Trash, permissions, search.
follow POST /api/v1/files/delete '{"root":"sites","paths":["demo/app/restored"]}'
[ ! -e $S/restored ] && [ -d /srv/www/.cairn-trash ] || fail "trash"
tid=$(api "$base/api/v1/files/trash?root=sites" | json 'd["entries"][0]["id"]')
follow POST /api/v1/files/trash/restore "{\"root\":\"sites\",\"ids\":[\"$tid\"]}"
[ -f $S/restored/index.html ] || fail "restore"
follow POST /api/v1/files/delete '{"root":"sites","paths":["demo/app/restored"],"permanent":true}'
[ ! -e $S/restored ] || fail "permanent delete"
[ "$(api -X POST "$base/api/v1/files/chmod" -d '{"root":"sites","paths":["demo/app/index.html"],"mode":2541}' | json 'd["error"]["code"]')" = validation.invalid ] || fail "setuid accepted"
follow POST /api/v1/files/chmod '{"root":"sites","paths":["demo/app/img"],"mode":416,"dirMode":1512,"recursive":true}'
[ "$(stat -c %a $S/img)" = 2750 ] && [ "$(stat -c %a $S/img/up.bin)" = 640 ] || fail "chmod $(stat -c %a $S/img) $(stat -c %a $S/img/up.bin)"
follow POST /api/v1/files/chown '{"root":"sites","paths":["demo/app/img/up.bin"],"user":"root","group":"root"}'
[ "$(owner $S/img/up.bin)" = root:root ] || fail "chown"
r=$(api "$base/api/v1/files/search?root=sites&path=demo&q=V2&content=true")
[ "$(printf '%s' "$r" | json '[(m["path"], m["line"]) for m in d["matches"]]')" = "[('demo/app/index.html', 1)]" ] || fail "search: $r"
step "trash: delete, restore, delete for good; chmod -R (setgid dirs, setuid refused); chown; content search"

# Fetching a URL, as the panel's own user.
mkdir -p /tmp/www && echo fetched >/tmp/www/remote.txt
(cd /tmp/www && python3 -m http.server 8765 --bind 127.0.0.1 >/dev/null 2>&1 &)
sleep 1
follow POST /api/v1/files/fetch '{"root":"sites","dir":"demo/app","url":"http://127.0.0.1:8765/remote.txt"}'
[ "$(cat $S/remote.txt)" = fetched ] && [ "$(owner $S/remote.txt)" = cw-demo:cw-demo ] || fail "fetch"
step "a URL fetched into the site directory"

# Full-disk access.
[ "$(api "$base/api/v1/files/list?root=system&path=etc" | json 'd["error"]["code"]')" = file.root_unavailable ] || fail "system root before"
api -X PUT "$base/api/v1/files/settings" -d '{"fullDisk":true}' | grep_q '"fullDisk":true' || fail "full disk on"
api "$base/api/v1/files/list?root=system&path=etc" | json '[e["name"] for e in d["entries"]]' | grep_q "'os-release'" || fail "system root listing"
out=$(start POST /api/v1/files/delete '{"root":"system","paths":["etc"]}')
[ "${out%% *}" = failed ] && [ -f /etc/os-release ] || fail "a top-level system directory was deleted: $out"
api -X PUT "$base/api/v1/files/settings" -d '{"fullDisk":false}' >/dev/null
step "full-disk access on (system root listed) and off"

# Terminals: as the site owner (no systemd here: started by the helper).
ws() { python3 -I /t/ws.py "wss://127.0.0.1:${port}/api/v1/terminal/sessions/$1/ws" "$jar" "$2" "$3" ${4:-}; }
api "$base/api/v1/terminal/users" | json '[u["name"] for u in d["users"]]' | grep_q "'cw-demo'" || fail "terminal users"
sid=$(api -X POST "$base/api/v1/terminal/sessions" -d '{"user":"cw-demo","cols":100,"rows":30}' | json 'd["id"]')
ws "$sid" 'id -un; pwd; stty size\n' '100' >/tmp/term.out || fail "terminal: $(cat /tmp/term.out)"
grep_q cw-demo /tmp/term.out && grep_q /srv/www/demo /tmp/term.out || fail "terminal identity: $(cat /tmp/term.out)"
ws "$sid" 'exit\n' '' --exit >/dev/null || fail "terminal exit"
[ "$(api -X POST "$base/api/v1/terminal/sessions" -d '{"user":"root","cols":80,"rows":24}' | json 'd["error"]["code"]')" = term.root_disabled ] || fail "root while off"
api -X PUT "$base/api/v1/terminal/settings" -d '{"rootTerminal":true}' | grep_q '"rootTerminal":true' || fail "root terminal on"
sid=$(api -X POST "$base/api/v1/terminal/sessions" -d '{"user":"root","cols":80,"rows":24}' | json 'd["id"]')
ws "$sid" 'echo uid=$(id -u)\n' 'uid=0' >/dev/null || fail "root terminal"
api -X DELETE "$base/api/v1/terminal/sessions/$sid" >/dev/null
step "terminal as cw-demo (in its site, sized), exit ends it; root only once switched on"

# The server's configuration can forbid root access whatever the panel says.
printf '\n[security]\nroot_access = "never"\n' >>/etc/cairn/config.toml
pkill -f /usr/lib/cairn/cairn-helper
sleep 1
rm -f /run/cairn/helper.sock
start_helper
[ "$(api -X PUT "$base/api/v1/files/settings" -d '{"fullDisk":true}' | json 'd["error"]["code"]')" = access.locked ] || fail "locked"
[ "$(api "$base/api/v1/files/roots" | json 'd["settings"]["locked"]')" = True ] || fail "locked not reported"
step "root_access = \"never\" in the configuration locks full-disk access and root terminals"
echo "OK"
