# shellcheck shell=sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# /etc/profile.d/cairn-term.sh: colours in Cairn's web terminal.
#
# Cairn starts the web terminal's login shell with CAIRN_TERM=1. Only there,
# and only in an interactive shell on a terminal with colours, this file
# sets what most distributions leave to the user's own ~/.bashrc: LS_COLORS,
# colour aliases for ls, grep, diff and ip, and in bash a coloured prompt in
# place of the distribution's plain default. A user's own files run later and
# win: an alias or a prompt that is already set is never replaced, and SSH
# and console logins are not touched at all.
#
# /etc/profile sources it under sh, bash or zsh (in sh emulation), so it is
# POSIX sh, prints nothing, works under "set -u" and may be sourced twice.

if [ "${CAIRN_TERM-}" = 1 ]; then
	case $- in
	*i*)
		case ${TERM-} in
		*color* | xterm* | screen* | tmux* | rxvt* | linux | ansi | alacritty | foot* | kitty* | konsole* | putty*)
			if [ -z "${LS_COLORS-}" ] && command -v dircolors >/dev/null 2>&1; then
				eval "$(dircolors -b 2>/dev/null)"
			fi

			# _cairn_term_alias NAME OPTION [ARGS...] aliases NAME to
			# "NAME OPTION" unless NAME already has an alias, is missing, or
			# refuses the option: "NAME OPTION ARGS..." must end with status 0
			# or 1 (grep without a match), not with a usage error.
			_cairn_term_alias() {
				alias "$1" >/dev/null 2>&1 && return 0
				command -v "$1" >/dev/null 2>&1 || return 0
				_cairn_term_ok=0
				if command "$@" >/dev/null 2>&1 </dev/null; then
					_cairn_term_ok=1
				elif [ $? -eq 1 ]; then
					_cairn_term_ok=1
				fi
				if [ "$_cairn_term_ok" = 1 ]; then
					# shellcheck disable=SC2139 # expanded now: the command and its option
					alias "$1=$1 $2"
				fi
				unset _cairn_term_ok
				return 0
			}
			_cairn_term_alias ls --color=auto -d /
			_cairn_term_alias grep --color=auto -q x /dev/null
			_cairn_term_alias egrep --color=auto -q x /dev/null
			_cairn_term_alias fgrep --color=auto -q x /dev/null
			_cairn_term_alias diff --color=auto /dev/null /dev/null
			_cairn_term_alias ip -color=auto -V
			unset -f _cairn_term_alias

			# The prompt, in bash only and only while it is still a
			# distribution default: Debian's and Ubuntu's (from
			# /etc/bash.bashrc), bash's built-in one that the EL family has at
			# this point, and the EL family's own (from /etc/bashrc). The user
			# and host are red for root and green otherwise, the directory
			# blue; the layout stays the distribution's.
			if [ -n "${BASH_VERSION-}" ]; then
				# shellcheck disable=SC3028 # bash only, see above
				if [ "${EUID-}" = 0 ]; then
					_cairn_term_user=31
				else
					_cairn_term_user=32
				fi
				# shellcheck disable=SC2016,SC2154 # prompts, expanded by bash when shown
				case ${PS1-} in
				'${debian_chroot:+($debian_chroot)}\u@\h:\w\$ ')
					PS1='${debian_chroot:+($debian_chroot)}\[\e[01;'$_cairn_term_user'm\]\u@\h\[\e[00m\]:\[\e[01;34m\]\w\[\e[00m\]\$ '
					;;
				'\s-\v\$ ' | '[\u@\h \W]\$ ')
					PS1='[\[\e[01;'$_cairn_term_user'm\]\u@\h\[\e[00m\] \[\e[01;34m\]\W\[\e[00m\]]\$ '
					;;
				esac
				unset _cairn_term_user
			fi
			;;
		esac
		;;
	esac
fi
