#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the test VM (see run.sh) to check P1 batch 9 with the
# real systemd, firewall and sshd: the firewall switched on by install.sh,
# changes confirmed over a new connection, one rolled back by its timer;
# the SSH port moved in two steps (and back), keys, the login log; host
# name, time zone, swap, updates, automatic updates and a switch of the
# system source with its restore.
#
#   security.sh          the checks; the panel stays installed, with two
#                        listeners (8081 allowed, 8083 not) for run.sh to
#                        probe from outside
#   security.sh purge    removes the panel afterwards
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
R=http://10.0.2.2:8080/releases
. /etc/os-release
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	if command -v getenforce >/dev/null; then
		echo "--- SELinux $(getenforce); contexts:" >&2
		ps -eZ | grep -E 'cairn|firewalld' >&2 || true
		ls -Z /usr/lib/cairn/ /usr/bin/cairn* >&2 || true
		d=$({ ausearch -m AVC,USER_AVC,SELINUX_ERR -ts recent 2>/dev/null; journalctl -b _TRANSPORT=audit --no-pager 2>/dev/null; } | grep -E 'denied|SELINUX_ERR' | tail -30 || true)
		printf -- '--- recent AVC denials:\n%s\n' "${d:-(none)}" >&2
	fi
	tail -30 /root/install.log >&2 2>/dev/null || true
	journalctl -u cairn-web -u cairn-helper -u 'cairn-fw-*' -u 'cairn-ssh-*' --no-pager -n 80 >&2 || true
	exit 1
}
family=debian
case " $ID ${ID_LIKE:-} " in *" rhel "* | *" fedora "* | *" rocky "* | *" almalinux "*) family=el ;; esac
export DEBIAN_FRONTEND=noninteractive

if [ "${1:-}" = purge ]; then
	pkill -f 'http.server 808' || true
	if [ "$family" = debian ]; then apt-get purge -y cairn >/root/purge.log 2>&1 || fail "purge"; else dnf remove -y cairn >/root/purge.log 2>&1 || fail "remove"; fi
	! nft list table inet cairn >/dev/null 2>&1 || fail "the nftables table survived the purge"
	step "panel removed"
	exit 0
fi

echo "== ${PRETTY_NAME} (VM, batch 9)"
command -v curl >/dev/null || fail "no curl"
curl -fsSL "$R/latest/download/install.sh" -o /root/install.sh
sh /root/install.sh --mirror "$R" --lang en --yes >/root/install.log 2>&1 || fail "install.sh"
backend=$(sed -n 's/.*The firewall (\([a-z]*\)) will be switched on.*/\1/p' /root/install.log)
if ! grep_q 'The firewall is on.' /root/install.log; then
	echo "--- install.log ($(wc -c </root/install.log) bytes):" >&2
	cat /root/install.log >&2
	fail "the installer did not switch the firewall on"
fi
case "$ID" in
	debian) want=nftables ;;
	ubuntu) want=ufw ;;
	*) want=firewalld ;;
esac
[ "$backend" = "$want" ] || fail "firewall backend $backend, want $want"
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
case "$backend" in
	nftables)
		nft list table inet cairn | grep_q "tcp dport { 80, 443 } accept" || fail "nftables: web ports"
		nft list table inet cairn | grep_q "tcp dport $port accept" || fail "nftables: panel port"
		[ "$(systemctl is-enabled cairn-firewall.service)" = enabled ] || fail "cairn-firewall.service not enabled"
		# nftables.service flushes every table; ours follows it.
		systemctl restart nftables.service
		sleep 1
		nft list table inet cairn >/dev/null 2>&1 || fail "the table did not come back after nftables restarted"
		;;
	ufw)
		ufw status | grep_q "Status: active" || fail "ufw not active"
		ufw status | grep_q "$port/tcp" || fail "ufw: panel port"
		;;
	firewalld)
		firewall-cmd --state | grep_q running || fail "firewalld not running"
		firewall-cmd --info-policy=cairn | grep_q "ports: 22/tcp $port/tcp 80/tcp 443/tcp" || fail "firewalld: policy ports"
		;;
esac
if [ "$family" = el ]; then
	ps -eZ | grep cairn-helper | grep_q unconfined_service_t || fail "the helper does not run as unconfined_service_t: $(ps -eZ | grep cairn-helper)"
fi
step "install.sh switched the firewall on ($backend): SSH, the panel port $port, 80 and 443 open"

base="https://127.0.0.1:${port}"
for _ in $(seq 120); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 1; done
jar=/root/jar elevated=/root/elevated-at
# A browser session is the cookie in the jar and the session key that setup
# and sign-in answer with: keep_key saves it next to the jar, and every
# request sends it (the cookie alone is refused).
skey() { cat "${1:-$jar}.key" 2>/dev/null || true; }
keep_key() (
	out=$(cat)
	printf '%s' "$out" | sed -n 's/.*"sessionKey":"\([A-Za-z0-9_-]*\)".*/\1/p' >"${1:-$jar}.key"
	printf '%s\n' "$out"
)
req() { curl -sk -c "$jar" -b "$jar" -H "X-Cairn-Session: $(skey)" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
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
api -X POST "$base/api/v1/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"password\":\"$pw\",\"locale\":\"en\"}" | keep_key | grep_q '"username":"admin"' || fail "setup"
rm -f "$elevated"
api -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | keep_key | grep_q '"username":"admin"' || fail "login"
elevate
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
wait_job() { # job id -> final state, log in /root/job.log
	local state="" offset=0 r
	: >/root/job.log
	for _ in $(seq 240); do
		r=$(api "$base/api/v1/jobs/$1/log?offset=$offset&wait=10")
		printf '%s' "$r" | json 'd["log"]' >>/root/job.log
		offset=$(printf '%s' "$r" | json 'd["offset"]')
		state=$(printf '%s' "$r" | json 'd["job"]["state"]')
		case "$state" in succeeded | failed | canceled | interrupted) break ;; esac
	done
	echo "$state"
}
run_job() { # method path body what
	local r id state
	r=$(api -X "$1" "$base$2" -d "$3")
	id=$(printf '%s' "$r" | json 'd["job"]["id"]' 2>/dev/null) || fail "$4: $r"
	state=$(wait_job "$id")
	[ "$state" = succeeded ] || { cat /root/job.log; api "$base/api/v1/jobs/$id" >&2; fail "$4: job $state"; }
}
rules() {
	case "$backend" in
		nftables) nft list table inet cairn ;;
		ufw) ufw status ;;
		firewalld) firewall-cmd --info-policy=cairn ;;
	esac
}

# Firewall: a change is kept when confirmed over a new connection, and
# rolled back by its timer when not. Only a connection opened from the
# second after the change counts: confirm as the page does, a second
# after the change, then again while the server asks to retry (every curl
# run opens a new connection).
r=$(api -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8081"],"note":"vm"}')
pending=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "rule: $r"
systemctl list-timers --all | grep_q "cairn-fw-rollback-$pending" || fail "no rollback timer"
sleep 1
for tries in $(seq 20); do
	r=$(api -X POST "$base/api/v1/firewall/confirm" -d "{\"id\":\"$pending\"}")
	printf '%s' "$r" | grep_q '^{}' && break
	printf '%s' "$r" | grep_q '"firewall.confirm_retry"' || fail "confirm: $r"
	[ "$tries" -lt 20 ] || fail "confirm: still asked to retry after $tries tries: $r"
	sleep 1.5
done
rules | grep_q 8081 || fail "rule 8081 missing"
! systemctl list-timers --all | grep_q "cairn-fw-rollback-$pending" || fail "rollback timer left after the confirmation"
r=$(api -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8083"]}')
printf '%s' "$r" | json 'd["pending"]["id"]' >/dev/null || fail "rule 8083: $r"
rules | grep_q 8083 || fail "rule 8083 not applied"
sleep 70
api "$base/api/v1/firewall" | json 'd["pending"] is None and d["lastRollback"]["kind"] == "rule.add"' | grep_q True || fail "not rolled back: $(api "$base/api/v1/firewall")"
! rules | grep_q 8083 || fail "rule 8083 still there after the rollback"
api -X POST "$base/api/v1/firewall/addresses" -d '{"address":"127.0.0.1","action":"deny"}' | grep_q firewall.self_block || fail "own address blocked"
step "firewall: rule 8081 confirmed over a new connection; rule 8083 rolled back by its timer after 60 s; own address refused"

# SSH: a second port next to 22, then back; then moved and back again.
ssh_ports() { ss -Htln | awk '{print $4}' | grep -oE ':(22|2222)$' | sort -u | tr '\n' ' '; }
run_job POST /api/v1/ssh/port '{"port":2222,"closeAfterMinutes":5}' "ssh port 2222"
[ "$(ssh_ports)" = ":22 :2222 " ] || fail "sshd ports: $(ssh_ports)"
rules | grep_q 2222 || fail "firewall does not allow 2222"
api "$base/api/v1/ssh" | json 'd["pending"]["newPort"]' | grep_q 2222 || fail "no pending port change"
systemctl list-timers --all | grep_q cairn-ssh-port-close || fail "no timer to close the old port"
if [ "$family" = el ]; then semanage port -l | grep -E '^ssh_port_t' | grep_q 2222 || fail "SELinux label for 2222"; fi
run_job POST /api/v1/ssh/port/cancel '' "ssh port cancel"
[ "$(ssh_ports)" = ":22 " ] || fail "after cancel: $(ssh_ports)"
! rules | grep_q 2222 || fail "2222 still open after cancel"
run_job POST /api/v1/ssh/port '{"port":2222,"closeAfterMinutes":5}' "ssh port 2222 again"
# The timer's own service closes the old port, as it would after 5 minutes.
systemctl start cairn-ssh-port-close.service
for _ in $(seq 30); do [ "$(ssh_ports)" = ":2222 " ] && break; sleep 1; done
[ "$(ssh_ports)" = ":2222 " ] || { journalctl -u cairn-ssh-port-close.service --no-pager >&2; fail "after the timer: $(ssh_ports)"; }
run_job POST /api/v1/ssh/port '{"port":22,"closeAfterMinutes":5}' "ssh port back to 22"
run_job POST /api/v1/ssh/port/finish '' "ssh port finish"
[ "$(ssh_ports)" = ":22 " ] || fail "back on 22: $(ssh_ports)"
sshd -T | grep_q '^port 22$' || fail "sshd -T"
step "SSH: 2222 added next to 22 (firewall$( [ "$family" = el ] && echo ', SELinux')), cancelled; moved to 2222 by the timer's service; moved back to 22"

run_job PUT /api/v1/ssh/settings '{"passwordAuthentication":false,"permitRootLogin":"no"}' "ssh settings"
sshd -T | grep_q '^passwordauthentication no$' || fail "password login still on"
sshd -T | grep_q '^permitrootlogin no$' || fail "root login still on"
key="$(cat /home/tester/.ssh/authorized_keys | head -1)"
rm -f /root/vmkey /root/vmkey.pub
ssh-keygen -q -t ed25519 -N '' -C vm-test -f /root/vmkey
k=$(cat /root/vmkey.pub)
fp=$(api -X POST "$base/api/v1/ssh/keys" -d "{\"user\":\"tester\",\"key\":\"$k\"}" | json 'd["key"]["fingerprint"]') || fail "add key"
api "$base/api/v1/ssh/keys?user=tester" | grep_q "$fp" || fail "key not listed"
[ "$(stat -c '%U %a' /home/tester/.ssh/authorized_keys)" = "tester 600" ] || fail "authorized_keys owner/mode"
api -X DELETE "$base/api/v1/ssh/keys?user=tester&fingerprint=$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$fp")" | grep_q '^{}' || fail "delete key"
head -1 /home/tester/.ssh/authorized_keys | grep -qF "$key" || fail "the original key was lost"
api "$base/api/v1/ssh/logins?limit=50" | json '[e for e in d["entries"] if e["user"] == "tester" and e["result"] == "accepted" and e["method"] == "publickey"][0]["ip"]' |
	grep_q . || fail "login log: $(api "$base/api/v1/ssh/logins?limit=50")"
run_job PUT /api/v1/ssh/settings '{"passwordAuthentication":true,"permitRootLogin":"prohibit-password"}' "ssh settings back"
step "SSH: password and root login off and on again (sshd -T agrees); a key added and removed for tester; tester's publickey login in the log"

# Host.
api -X PUT "$base/api/v1/host/hostname" -d '{"hostname":"cairn-vm"}' | grep_q '^{}' || fail "hostname"
[ "$(hostname)" = cairn-vm ] || fail "hostname is $(hostname)"
api -X PUT "$base/api/v1/host/timezone" -d '{"timezone":"Asia/Shanghai"}' | grep_q '^{}' || fail "timezone"
[ "$(timedatectl show -p Timezone --value)" = Asia/Shanghai ] || fail "timezone is $(timedatectl show -p Timezone --value)"
run_job PUT /api/v1/host/swap '{"sizeMb":256}' "swap"
grep_q '^/swapfile ' /proc/swaps || fail "swap not on"
[ "$(stat -c '%a' /swapfile)" = 600 ] || fail "swap file mode"
grep_q '^/swapfile none swap' /etc/fstab || fail "fstab"
api -X PUT "$base/api/v1/host/swappiness" -d '{"value":10}' | grep_q '^{}' || fail "swappiness"
[ "$(cat /proc/sys/vm/swappiness)" = 10 ] || fail "swappiness value"
run_job PUT /api/v1/host/swap '{"sizeMb":0}' "swap off"
! grep_q '^/swapfile ' /proc/swaps || fail "swap still on"
[ ! -e /swapfile ] && ! grep_q swapfile /etc/fstab || fail "swap file left"
step "host: host name, time zone, a 256 MiB swap file (0600, in fstab) and swappiness; swap removed"

# The package manager runs in a unit of its own (cairn-pkg.service), not in
# the helper's sandbox, which hides the kernel modules: there a kernel
# package cannot be installed and a rebuilt initramfs has no modules. An apt
# hook tells what apt-get itself sees (Debian, Ubuntu).
probe=/root/pkg-probe
: >/root/pkg-mark
if [ "$family" = debian ]; then
	cat >/etc/apt/apt.conf.d/99cairn-vm-probe <<EOF
APT::Update::Pre-Invoke { "if touch /usr/lib/modules/.cairn-vm-probe; then echo visible; else echo hidden; fi >$probe 2>&1; rm -f /usr/lib/modules/.cairn-vm-probe"; };
EOF
fi
run_job POST /api/v1/host/updates/check '' "updates check"
if [ "$family" = debian ]; then
	rm -f /etc/apt/apt.conf.d/99cairn-vm-probe
	[ "$(cat "$probe" 2>/dev/null)" = visible ] || fail "apt-get ran where /usr/lib/modules is hidden: $(cat "$probe" 2>&1)"
fi
[ -n "$(find /var/lib/cairn-helper/pkg -name '*.result.json' -newer /root/pkg-mark 2>/dev/null)" ] ||
	fail "the package manager did not run in a unit of its own"
api "$base/api/v1/host" | json 'd["updates"]["checkedAt"] > 0' | grep_q True || fail "no update list"
n=$(api "$base/api/v1/host" | json 'len(d["updates"]["packages"])')
run_job PUT /api/v1/host/updates/auto '{"enabled":true}' "auto updates"
api "$base/api/v1/host" | json 'd["autoUpdates"]["enabled"]' | grep_q True || fail "automatic updates not on"
step "updates: checked ($n pending), automatic security updates on ($(api "$base/api/v1/host" | json 'd["autoUpdates"]["tool"]'))"

# System sources: switch the main one to another eligible candidate, then
# restore the backup.
api "$base/api/v1/host/sources" >/root/sources.json
cand=$(python3 - <<'EOF'
import json
d = json.load(open("/root/sources.json"))
for s in d["slots"]:
    if s["role"] != "main":
        continue
    for c in s["candidates"]:
        if c["eligible"] and not c["inUse"] and (c["url"].startswith("https://") or s.get("suites")):
            print(c["id"]); raise SystemExit
EOF
) || true
if [ -n "$cand" ]; then
	if [ "$family" = debian ]; then srcfiles=$(ls /etc/apt/sources.list /etc/apt/sources.list.d/* 2>/dev/null); else srcfiles=$(ls /etc/yum.repos.d/*.repo); fi
	# shellcheck disable=SC2086 # a list of files
	before=$(cat $srcfiles | md5sum)
	run_job POST /api/v1/host/sources/switch "{\"role\":\"main\",\"candidate\":\"$cand\"}" "switch source to $cand"
	id=$(api "$base/api/v1/host/sources/backups" | json 'd["backups"][0]["id"]')
	to=$(api "$base/api/v1/host/sources/backups" | json 'd["backups"][0]["to"]')
	# shellcheck disable=SC2086
	[ "$(cat $srcfiles | md5sum)" != "$before" ] || fail "sources not switched"
	run_job POST /api/v1/host/sources/restore "{\"id\":\"$id\"}" "restore sources"
	# shellcheck disable=SC2086
	[ "$(cat $srcfiles | md5sum)" = "$before" ] || fail "sources not restored"
	step "system sources: main switched to $to (package indexes read from it), backup $id restored byte for byte"
else
	step "system sources: probed; no other eligible candidate from this network ($(python3 -c 'import json; print(len(json.load(open("/root/sources.json"))["slots"]))') slots)"
fi

# Listeners for run.sh: 8081 is allowed, 8083 is not.
nohup python3 -m http.server 8081 >/dev/null 2>&1 &
nohup python3 -m http.server 8083 >/dev/null 2>&1 &
sleep 1
echo "== PASS: ${PRETTY_NAME} (VM, batch 9)"
