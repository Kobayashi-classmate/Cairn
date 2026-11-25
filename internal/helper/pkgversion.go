// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"strconv"
	"strings"
)

// Package version order, as dpkg and rpm define it. Used to make sure a
// chosen source never installs an older version (installsource.go).

// debVersionCompare compares two Debian versions ([epoch:]upstream[-revision])
// the way dpkg does: <0, 0 or >0.
func debVersionCompare(a, b string) int {
	ea, ua, ra := splitDebVersion(a)
	eb, ub, rb := splitDebVersion(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := debVerRevCmp(ua, ub); c != 0 {
		return c
	}
	return debVerRevCmp(ra, rb)
}

func splitDebVersion(v string) (epoch int, upstream, revision string) {
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, _ = strconv.Atoi(e)
		v = rest
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// debOrder is dpkg's character weight: "~" before the end, the end and
// digits, letters, then everything else.
func debOrder(c byte) int {
	switch {
	case c == 0 || isDigit(c):
		return 0
	case isAlpha(c):
		return int(c)
	case c == '~':
		return -1
	}
	return int(c) + 256
}

// debVerRevCmp is dpkg's verrevcmp.
func debVerRevCmp(a, b string) int {
	at := func(s string, i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		firstDiff := 0
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			ac, bc := debOrder(at(a, i)), debOrder(at(b, j))
			if ac != bc {
				return ac - bc
			}
			i++
			j++
		}
		for at(a, i) == '0' {
			i++
		}
		for at(b, j) == '0' {
			j++
		}
		for isDigit(at(a, i)) && isDigit(at(b, j)) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if isDigit(at(a, i)) {
			return 1
		}
		if isDigit(at(b, j)) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}

// rpmEVRCompare compares two rpm versions ([epoch:]version[-release], as
// dnf prints %{evr}) the way rpm does: <0, 0 or >0.
func rpmEVRCompare(a, b string) int {
	ea, va, ra := splitEVR(a)
	eb, vb, rb := splitEVR(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := rpmVerCmp(va, vb); c != 0 {
		return c
	}
	return rpmVerCmp(ra, rb)
}

func splitEVR(v string) (epoch int, version, release string) {
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, _ = strconv.Atoi(e)
		v = rest
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

// rpmVerCmp is rpm's rpmvercmp.
func rpmVerCmp(a, b string) int {
	if a == b {
		return 0
	}
	at := func(s string, i int) byte {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	sep := func(c byte) bool { return c != 0 && !isDigit(c) && !isAlpha(c) && c != '~' && c != '^' }
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for sep(at(a, i)) {
			i++
		}
		for sep(at(b, j)) {
			j++
		}
		// "~" sorts before everything, even the end.
		if at(a, i) == '~' || at(b, j) == '~' {
			if at(a, i) != '~' {
				return 1
			}
			if at(b, j) != '~' {
				return -1
			}
			i++
			j++
			continue
		}
		// "^" sorts after the end, before anything else.
		if at(a, i) == '^' || at(b, j) == '^' {
			switch {
			case i >= len(a):
				return -1
			case j >= len(b):
				return 1
			case a[i] != '^':
				return 1
			case b[j] != '^':
				return -1
			}
			i++
			j++
			continue
		}
		if i >= len(a) || j >= len(b) {
			break
		}
		si, sj := i, j
		num := isDigit(a[i])
		class := isAlpha
		if num {
			class = isDigit
		}
		for i < len(a) && class(a[i]) {
			i++
		}
		for j < len(b) && class(b[j]) {
			j++
		}
		// A numeric segment is newer than an alphabetic one.
		if sj == j {
			if num {
				return 1
			}
			return -1
		}
		x, y := a[si:i], b[sj:j]
		if num {
			x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(x) != len(y) {
				if len(x) > len(y) {
					return 1
				}
				return -1
			}
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	switch {
	case i >= len(a) && j >= len(b):
		return 0
	case i >= len(a):
		return -1
	}
	return 1
}
