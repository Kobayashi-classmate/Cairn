#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# Runs as root inside the test VM, after security.sh, around a reboot that
# host.sh makes: a firewall change that nobody confirmed must not stay in
# force because the machine was restarted. Its rollback timer is a
# transient unit and gone after the reboot, while its rules are on disk
# and loaded again at boot.
#
#   reboot.sh before   makes a change (a rule for port 8085) and leaves it
#                      unconfirmed
#   reboot.sh after    after the reboot: the change is rolled back (at once
#                      when its time ran out while the machine was down,
#                      else when it runs out), and changes are possible again
set -euo pipefail
grep_q() { grep "$@" >/dev/null; }
step() { printf '  - %s\n' "$*"; }
fail() {
	printf 'FAIL: %s\n' "$*" >&2
	journalctl -b -u cairn-firewall-recover.service -u cairn-helper.service -u 'cairn-fw-*' --no-pager -n 60 >&2 || true
	systemctl list-timers --all --no-pager >&2 || true
	exit 1
}
port=$(sed -n 's/^listen = "0.0.0.0:\([0-9]*\)"/\1/p' /etc/cairn/config.toml)
base="https://127.0.0.1:${port}"
jar=/root/reboot-jar
pw=vm-test-password-1
# A browser session is the cookie in the jar and the session key that
# signing in answers with: keep_key saves it next to the jar, and every
# request sends it (the cookie alone is refused).
skey() { cat "$jar.key" 2>/dev/null || true; }
keep_key() (
	out=$(cat)
	printf '%s' "$out" | sed -n 's/.*"sessionKey":"\([A-Za-z0-9_-]*\)".*/\1/p' >"$jar.key"
	printf '%s\n' "$out"
)
req() { curl -sk -c "$jar" -b "$jar" -H "X-Cairn-Session: $(skey)" -H 'X-Cairn-Request: 1' -H 'Content-Type: application/json' "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
sign_in() {
	for _ in $(seq 120); do curl -sk -o /dev/null "$base/api/v1/meta" && break; sleep 1; done
	rm -f "$jar" "$jar.key"
	req -X POST "$base/api/v1/auth/login" -d "{\"username\":\"admin\",\"password\":\"$pw\"}" | keep_key | grep_q '"username":"admin"' || fail "login"
	req -X POST "$base/api/v1/auth/elevate" -d "{\"password\":\"$pw\"}" | grep_q '"elevatedUntil"' || fail "elevate"
}
rules() {
	if nft list table inet cairn >/dev/null 2>&1; then
		nft list table inet cairn
	elif command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
		firewall-cmd --info-policy=cairn
	else
		ufw status
	fi
}

case "${1:-}" in
	before)
		sign_in
		r=$(req -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8085"],"note":"reboot"}')
		pending=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "rule 8085: $r"
		printf '%s\n' "$pending" >/root/reboot-pending
		rules | grep_q 8085 || fail "rule 8085 not applied"
		systemctl list-timers --all | grep_q "cairn-fw-rollback-$pending" || fail "no rollback timer"
		sync
		step "a rule for port 8085 is in force, unconfirmed (change $pending); the machine restarts now"
		;;
	after)
		pending=$(cat /root/reboot-pending)
		[ "$(systemctl show cairn-firewall-recover.service -p ConditionResult --value)" = yes ] || fail "cairn-firewall-recover.service did not run at boot"
		sign_in
		# The reboot takes longer than the change had left as a rule, but not
		# always: allow for what is left of its minute, and for the retries.
		rolled=""
		for _ in $(seq 150); do
			rolled=$(req "$base/api/v1/firewall" | json "d[\"pending\"] is None and (d[\"lastRollback\"] or {}).get(\"id\") == \"$pending\"") || true
			[ "$rolled" = True ] && break
			sleep 1
		done
		[ "$rolled" = True ] || fail "the unconfirmed change was not rolled back after the reboot: $(req "$base/api/v1/firewall")"
		! rules | grep_q 8085 || fail "rule 8085 is still in force after the reboot"
		journalctl -b -u cairn-firewall-recover.service -u cairn-helper.service --no-pager | grep_q 'left pending' ||
			fail "nothing in the journal about taking the change over"
		# Nothing is stuck: the next change is accepted (and rolled back by hand).
		r=$(req -X POST "$base/api/v1/firewall/rules" -d '{"action":"allow","protocol":"tcp","ports":["8088"]}')
		next=$(printf '%s' "$r" | json 'd["pending"]["id"]') || fail "a change after the reboot: $r"
		req -X POST "$base/api/v1/firewall/rollback" -d "{\"id\":\"$next\"}" | grep_q '^{}' || fail "rolling the next change back"
		step "after the reboot the unconfirmed rule for 8085 was rolled back (change $pending); the next change was accepted"
		;;
	*)
		echo "usage: reboot.sh before|after" >&2
		exit 2
		;;
esac
