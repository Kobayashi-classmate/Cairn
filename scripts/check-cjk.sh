#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Fails when CJK characters appear in program files. All program text must be
# English; user-facing text lives in locale files. Exempt: locale directories,
# testdata, localized READMEs (README.md links to them by native name), and
# packaging/install.sh, which must work as a single downloaded file and so
# carries its own translated messages in one marked block.
set -eu

cd "$(git rev-parse --show-toplevel)"
export LC_ALL=C.UTF-8

pattern='[\x{3000}-\x{303F}\x{3040}-\x{30FF}\x{3400}-\x{4DBF}\x{4E00}-\x{9FFF}\x{F900}-\x{FAFF}\x{FF00}-\x{FFEF}]'

found=0
# Tracked files plus untracked files that are not ignored.
git ls-files -z --cached --others --exclude-standard |
	grep -zvE '(^|/)(locales|testdata)/|^README(\.[^/]+)?\.md$|^web/package-lock\.json$|^packaging/install\.sh$' |
	xargs -0 -r grep -nP "$pattern" -- >/tmp/cairn-cjk.$$ 2>/dev/null || true

if [ -s /tmp/cairn-cjk.$$ ]; then
	echo "CJK characters found outside locale files:" >&2
	cat /tmp/cairn-cjk.$$ >&2
	found=1
fi
rm -f /tmp/cairn-cjk.$$

if [ "$found" -ne 0 ]; then
	echo "Write program text in English and move user-facing strings to locale files." >&2
	exit 1
fi
echo "check-cjk: ok"
