#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the test VM (see run.sh) to check P1 batch 9 with the
# real systemd, firewall and sshd: the firewall switched on by install.sh
# (run over SSH with nobody at a terminal: the connection it runs in stays
# open), changes confirmed over a new connection, one rolled back by its
# timer, one whose timer was lost taken over the way a boot does, a rollback
# that fails and is tried again; the SSH port moved in two steps (and back;
# the old port only closes by itself once the new one was reached from
# outside), a port set on ssh.socket, keys, the login log; host
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
# This script runs in an SSH session (through sudo, which drops
# SSH_CONNECTION): the helper finds the session among its ancestors, and its
# port is among the ones that stay open.
plan=$(/usr/lib/cairn/cairn-helper firewall plan) || fail "firewall plan"
printf '%s\n' "$plan" | grep_q '^session=22$' || fail "the helper did not find the SSH session this runs in: $plan"
printf '%s\n' "$plan" | grep -E '^ssh=(.* )?22( .*)?$' >/dev/null || fail "port 22 is not kept open for SSH: $plan"
printf '%s\n' "$plan" | grep_q '^unattended=yes$' || fail "the plan: $plan"
! grep_q 'NOT switched on' /root/install.log || fail "the installer left the firewall off"
[ "$(systemctl is-enabled cairn-firewall-recover.service)" = static ] || fail "cairn-firewall-recover.service: $(systemctl is-enabled cairn-firewall-recover.service)"
systemctl list-dependencies multi-user.target | grep_q cairn-firewall-recover.service || fail "cairn-firewall-recover.service is not started at boot"
step "install.sh switched the firewall on ($backend): SSH, the panel port $port, 80 and 443 open; the SSH session it ran in was found and kept open"

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

# A pending change that lost its timer (what a reboot leaves behind: the
# timer is a transient unit, the rules are on disk) is taken over by the
# unit that runs at boot: with time left it gets its timer back, past its
# time it is rolled back at once.
r=$(api -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8084"]}')
pending=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "rule 8084: $r"
systemctl stop "cairn-fw-rollback-$pending.timer"
systemctl start cairn-firewall-recover.service
for _ in $(seq 20); do systemctl list-timers --all | grep_q "cairn-fw-rollback-$pending" && break; sleep 1; done
systemctl list-timers --all | grep_q "cairn-fw-rollback-$pending" || fail "the pending change did not get its timer back"
systemctl stop "cairn-fw-rollback-$pending.timer"
sleep 62
rules | grep_q 8084 || fail "rule 8084 went away although its timer was stopped"
api -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8087"]}' | grep_q '"firewall.pending"' || fail "a change was accepted while another is pending"
systemctl start cairn-firewall-recover.service
for _ in $(seq 60); do
	api "$base/api/v1/firewall" | json 'd["pending"] is None' | grep_q True && break
	sleep 1
done
api "$base/api/v1/firewall" | json "d[\"pending\"] is None and d[\"lastRollback\"][\"id\"] == \"$pending\"" | grep_q True ||
	fail "the expired change was not taken over: $(api "$base/api/v1/firewall")"
! rules | grep_q 8084 || fail "rule 8084 still there after the takeover"
step "firewall: a pending change without its timer got the timer back; past its time it was rolled back by cairn-firewall-recover.service"

# A rollback that fails is not recorded as done: the change stays pending
# with the failure, the overview shows it, and the next try ends it. The
# backend is made to fail by making its files immutable.
case "$backend" in
	nftables) locked="/etc/cairn-firewall" ;;
	ufw) locked="/etc/ufw/user.rules /etc/ufw/user6.rules" ;;
	firewalld) locked="/etc/firewalld/policies" ;;
esac
r=$(api -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8086"]}')
pending=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "rule 8086: $r"
# shellcheck disable=SC2086 # a list of files
chattr +i $locked || fail "chattr +i $locked"
# shellcheck disable=SC2086
unlock() { chattr -i $locked; }
r=$(api -X POST "$base/api/v1/firewall/rollback" -d "{\"id\":\"$pending\"}")
printf '%s' "$r" | grep_q '"firewall.rollback_failed"' || { unlock; fail "a rollback that cannot work answered: $r"; }
api "$base/api/v1/firewall" | json "d[\"pending\"][\"id\"] == \"$pending\" and d[\"pending\"][\"rollbackFailed\"][\"attempts\"] >= 1 and (d[\"lastRollback\"] or {}).get(\"id\") != \"$pending\"" |
	grep_q True || { unlock; fail "the failed rollback is not on record: $(api "$base/api/v1/firewall")"; }
rules | grep_q 8086 || { unlock; fail "rule 8086 is gone although its rollback failed"; }
# The overview gives each module 8 seconds, and under emulation firewalld
# takes about that long to tell its state: asked again while the firewall
# is listed as not having answered.
for _ in 1 2 3 4 5 6 7 8 9 10; do
	ov=$(api "$base/api/v1/overview")
	printf '%s' "$ov" | json '"firewall" in d["unavailable"]' | grep_q True || break
done
printf '%s' "$ov" | grep_q '"firewall.rollback_failed"' || { unlock; fail "the overview does not show the failed rollback: $ov"; }
unlock
# (Its own timer may have got there first once the files could be written.)
api -X POST "$base/api/v1/firewall/rollback" -d "{\"id\":\"$pending\"}" | grep -E '^\{\}|"firewall.no_pending"' >/dev/null || fail "the second rollback: $(api "$base/api/v1/firewall")"
api "$base/api/v1/firewall" | json "d[\"pending\"] is None and d[\"lastRollback\"][\"id\"] == \"$pending\"" | grep_q True || fail "after the second rollback: $(api "$base/api/v1/firewall")"
! rules | grep_q 8086 || fail "rule 8086 still there after the second rollback"
step "firewall: a rollback that failed stayed pending with the failure on record (overview: firewall.rollback_failed); the retry rolled it back"

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
# The timer's own service, as it would run after 5 minutes. Nobody has
# connected to 2222 from another host (a security group in front of the
# server could still block it): the old port stays, the move waits.
systemctl start cairn-ssh-port-close.service 2>/dev/null || true
for _ in $(seq 30); do api "$base/api/v1/ssh" | json '(d["pending"] or {}).get("held")' | grep_q unreached && break; sleep 1; done
api "$base/api/v1/ssh" | json 'd["pending"]["held"]' | grep_q unreached || { journalctl -u cairn-ssh-port-close.service --no-pager >&2; fail "the move is not held: $(api "$base/api/v1/ssh")"; }
[ "$(ssh_ports)" = ":22 :2222 " ] || fail "the old port was closed although nobody reached the new one: $(ssh_ports)"
rules | grep -E '(^|[^0-9])22([^0-9]|$)' >/dev/null || fail "the firewall closed port 22"
run_job POST /api/v1/ssh/port/cancel '' "ssh port cancel while held"
[ "$(ssh_ports)" = ":22 " ] || fail "after the cancel: $(ssh_ports)"
# Again, this time with a connection to 2222 from another host: a network
# namespace of its own, whose address is not one of this host's.
run_job POST /api/v1/ssh/port '{"port":2222,"closeAfterMinutes":5}' "ssh port 2222 a third time"
ip netns add cairn-probe
ip link add cairn-p0 type veth peer name cairn-p1
ip link set cairn-p1 netns cairn-probe
ip addr add 10.99.0.1/30 dev cairn-p0
ip link set cairn-p0 up
ip netns exec cairn-probe ip addr add 10.99.0.2/30 dev cairn-p1
ip netns exec cairn-probe ip link set cairn-p1 up
ip netns exec cairn-probe timeout 40 bash -c 'exec 3<>/dev/tcp/10.99.0.1/2222 && head -c 4 <&3 && sleep 30' >/root/probe.log 2>&1 &
for _ in $(seq 30); do api "$base/api/v1/ssh" | json '(d["pending"] or {}).get("reachedAt", 0) > 0' | grep_q True && break; sleep 1; done
api "$base/api/v1/ssh" | json 'd["pending"]["reachedAt"] > 0' | grep_q True || { cat /root/probe.log >&2; fail "the connection from outside was not seen: $(api "$base/api/v1/ssh")"; }
systemctl start cairn-ssh-port-close.service
for _ in $(seq 30); do [ "$(ssh_ports)" = ":2222 " ] && break; sleep 1; done
[ "$(ssh_ports)" = ":2222 " ] || { journalctl -u cairn-ssh-port-close.service --no-pager >&2; fail "after the timer: $(ssh_ports)"; }
ip netns del cairn-probe
ip link del cairn-p0 2>/dev/null || true
run_job POST /api/v1/ssh/port '{"port":22,"closeAfterMinutes":5}' "ssh port back to 22"
run_job POST /api/v1/ssh/port/finish '' "ssh port finish"
[ "$(ssh_ports)" = ":22 " ] || fail "back on 22: $(ssh_ports)"
sshd -T | grep_q '^port 22$' || fail "sshd -T"
step "SSH: 2222 added next to 22 (firewall$( [ "$family" = el ] && echo ', SELinux')), cancelled; the timer's service kept 22 while nobody had reached 2222 from outside, and closed it once someone had; moved back to 22"

# With socket activation the ports are the socket unit's: one set there
# (as "systemctl edit ssh.socket" does) is kept open like the others,
# although sshd's own configuration does not know it.
if systemctl is-active --quiet ssh.socket; then
	mkdir -p /etc/systemd/system/ssh.socket.d
	printf '[Socket]\nListenStream=2223\n' >/etc/systemd/system/ssh.socket.d/cairn-test.conf
	systemctl daemon-reload
	systemctl restart ssh.socket
	api "$base/api/v1/firewall" | json '2223 in d["sshPorts"] and 22 in d["sshPorts"]' | grep_q True || fail "the port of ssh.socket is not kept open: $(api "$base/api/v1/firewall" | json 'd["sshPorts"]')"
	api "$base/api/v1/ssh" | json '2223 in d["ports"]' | grep_q True || fail "the SSH page does not list the port of ssh.socket"
	rm -f /etc/systemd/system/ssh.socket.d/cairn-test.conf
	rmdir /etc/systemd/system/ssh.socket.d 2>/dev/null || true
	systemctl daemon-reload
	systemctl restart ssh.socket
	api "$base/api/v1/firewall" | json '2223 not in d["sshPorts"]' | grep_q True || fail "port 2223 stayed after the drop-in went"
	step "SSH: a port set on ssh.socket (2223) shows on the firewall and SSH pages"
fi

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
