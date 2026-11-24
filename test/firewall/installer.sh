#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Checks what packaging/install.sh does about the firewall, with a stand-in
# for the helper: what it shows, when it asks, when it switches the firewall
# on without asking and when it leaves it off. Runs as root inside a
# throwaway container with the repository at /src (see run.sh); it writes
# to /usr/lib/cairn, /run/systemd and /t of that container.
set -eu
mkdir -p /usr/lib/cairn /run/systemd/system /t

# The part of the installer under test: its messages and the firewall step.
{
	echo 'set -eu'
	# shellcheck disable=SC2016 # written out for the generated script
	echo 'FIREWALL=${FIREWALL:-1}; ASSUME_YES=${ASSUME_YES:-0}; UI_LANG=${UI_LANG:-en}'
	sed -n '/^msg() {$/,/^# ---- Checks/p' /src/packaging/install.sh | sed '$d'
	# shellcheck disable=SC2016 # a pattern for the installer's own text
	sed -n '/^# ---- Firewall/,/^if \[ "\$MODE" = repo \]; then$/p' /src/packaging/install.sh | sed '$d'
	echo 'setup_firewall'
	echo 'echo "[installer goes on]"'
} >/t/fw.sh
grep -q '^setup_firewall() {$' /t/fw.sh || { echo "FAIL: the firewall step of install.sh was not found" >&2; exit 1; }

# The stand-in helper records how it is called and answers from /t.
cat >/usr/lib/cairn/cairn-helper <<'EOF'
#!/bin/sh
echo "helper $*" >>/t/calls
case "$2" in
	plan) cat /t/plan ;;
	setup)
		echo "Enabling the firewall (nftables): incoming connections are denied except:"
		case " $* " in *" --confirm "*) printf 'pending=x0123456789abcde\nexpires=1\n' ;; esac
		exit "$(cat /t/setup.rc)" ;;
	confirm) exit "$(cat /t/confirm.rc)" ;;
	undo) exit 0 ;;
esac
EOF
chmod +x /usr/lib/cairn/cairn-helper

plan() { # unattended reason ssh-ports ssh_config closing
	kept=$(for p in $3; do printf '%s/tcp ' "$p"; done)
	printf 'backend=nftables\nactive=no\ninstall=nftables\nports=%s28443/tcp 80/tcp 443/tcp\nssh=%s\nssh_config=%s\nsession=\nclosing=%s\nunattended=%s\nreason=%s\nconfirm_minutes=5\n' \
		"$kept" "$3" "$4" "$5" "$1" "$2" >/t/plan
}

fails=0
# check NAME CALLS OUTPUT [VAR=value...]: runs the firewall step with the
# variables set and compares the helper calls and the output (lines joined
# with "|") with two extended regular expressions. With TTY=1 the step gets
# a terminal of its own, which reads the answers from this function's
# standard input.
check() {
	name=$1 calls=$2 out=$3
	shift 3
	: >/t/calls
	echo 0 >/t/setup.rc
	[ -f /t/confirm.rc ] || echo 0 >/t/confirm.rc
	if [ "${TTY:-0}" = 1 ]; then
		got=$(env "$@" script -qec "sh /t/fw.sh" /dev/null 2>&1 | tr -d '\r') || true
	else
		got=$(env "$@" setsid sh /t/fw.sh 2>&1 </dev/null) || true
	fi
	if ! { tr '\n' '|' </t/calls; echo; } | grep -Eq "$calls" || ! { printf '%s' "$got" | tr '\n' '|'; echo; } | grep -Eq "$out"; then
		echo "FAIL: $name"
		echo "--- helper calls:"
		cat /t/calls
		echo "--- output:"
		printf '%s\n' "$got"
		fails=$((fails + 1))
	else
		echo "   ok: $name"
	fi
	rm -f /t/confirm.rc
}

# Nobody to ask.
plan yes "" "22" ok "25/tcp(master) 3306/tcp(mariadbd)"
check "unattended, SSH ports known: switched on; the ports that close are listed" \
	'^helper firewall plan\|helper firewall setup\|$' \
	'denied except on 22/tcp 28443/tcp.*not be reachable from outside any more: 25/tcp\(master\) 3306/tcp\(mariadbd\)\|.*The firewall is on\.\|\[installer goes on\]' \
	ASSUME_YES=1
check "inside an SSH session its server port goes to the helper" \
	'^helper firewall plan --ssh-port 2222\|helper firewall setup --ssh-port 2222\|$' 'The firewall is on\.' \
	ASSUME_YES=1 "SSH_CONNECTION=203.0.113.9 50000 192.0.2.10 2222"
check "a malformed SSH_CONNECTION is ignored" \
	'^helper firewall plan\|helper firewall setup\|$' 'The firewall is on\.' \
	ASSUME_YES=1 "SSH_CONNECTION=203.0.113.9 50000 192.0.2.10 22;reboot"
check "no terminal and no --yes is unattended too" \
	'^helper firewall plan\|helper firewall setup\|$' 'The firewall is on\.' ASSUME_YES=0
plan no ssh_unread "22" unread ""
check "unattended, sshd's configuration unreadable: left off, with the reason and the way on" \
	'^helper firewall plan\|$' \
	'Warning: the SSH server.s configuration cannot be read.*NOT switched on.*would be a guess.*or as root with: /usr/lib/cairn/cairn-helper firewall setup\|\[installer goes on\]' \
	ASSUME_YES=1
plan no ssh_unknown "" ok ""
check "unattended, no SSH server found: left off" \
	'^helper firewall plan\|$' 'Warning: no SSH server was found.*NOT switched on.*no SSH server was found whose port.*firewall setup\|\[installer goes on\]' \
	ASSUME_YES=1
check "the same in the other language" '^helper firewall plan\|$' 'SSH.*/usr/lib/cairn/cairn-helper firewall setup\|\[installer goes on\]' \
	ASSUME_YES=1 UI_LANG=zh-CN
check "--no-firewall" '^$' 'The firewall was left alone' FIREWALL=0

# Someone at a terminal: asked first, then kept only when confirmed.
plan yes "" "22" ok ""
TTY=1
printf 'n\n' >/t/answers
check "asked, answered no" '^helper firewall plan\|$' 'Switch the firewall on now\?.*The firewall was left alone' ASSUME_YES=0 </t/answers
printf '\nyes\n' >/t/answers
check "asked, switched on, confirmed" \
	'^helper firewall plan\|helper firewall setup --confirm\|helper firewall confirm x0123456789abcde\|$' \
	'Open a NEW SSH connection.*within 5 minutes.*The firewall is on\.' ASSUME_YES=0 </t/answers
printf 'y\nno\n' >/t/answers
check "asked, switched on, not confirmed: undone" \
	'^helper firewall plan\|helper firewall setup --confirm\|helper firewall undo x0123456789abcde\|$' \
	'switched off again; nothing else changed' ASSUME_YES=0 </t/answers
printf 'y\n\n' >/t/answers
check "an empty answer is not a yes" \
	'^helper firewall plan\|helper firewall setup --confirm\|helper firewall undo x0123456789abcde\|$' 'switched off again' ASSUME_YES=0 </t/answers
printf 'y\nyes\n' >/t/answers
echo 1 >/t/confirm.rc
check "confirmed after the time ran out" \
	'^helper firewall plan\|helper firewall setup --confirm\|helper firewall confirm x0123456789abcde\|$' 'The answer came too late' ASSUME_YES=0 </t/answers
printf 'y\nyes\n' >/t/answers
check "the other language asks the same way" \
	'^helper firewall plan --ssh-port 22\|helper firewall setup --confirm --ssh-port 22\|helper firewall confirm' ' SSH .* yes .*5 ' \
	ASSUME_YES=0 UI_LANG=zh-CN "SSH_CONNECTION=203.0.113.9 50000 192.0.2.10 22" </t/answers
printf 'y\nyes\n' >/t/answers
check "--yes at a terminal asks nothing" '^helper firewall plan\|helper firewall setup\|$' 'The firewall is on\.' ASSUME_YES=1 </t/answers

[ "$fails" -eq 0 ] || { echo "installer: $fails case(s) failed" >&2; exit 1; }
# shellcheck disable=SC1091
echo "   installer firewall step passed ($(. /etc/os-release && echo "$PRETTY_NAME"), sh is $(readlink -f /bin/sh))"
