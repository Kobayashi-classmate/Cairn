// SPDX-License-Identifier: AGPL-3.0-only

// Package semver parses and compares release versions (MAJOR.MINOR.PATCH with
// an optional -prerelease) and evaluates simple constraints such as
// ">=0.2.0 <0.4.0". Development builds (git hashes, "~dev") do not parse,
// which keeps them out of automatic update decisions.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed release version.
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // dot-separated prerelease identifiers
}

// Parse accepts "1.2.3", "v1.2.3" and "1.2.3-beta.1". Build metadata after
// "+" is ignored.
func Parse(s string) (Version, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		build := s[i+1:]
		for _, id := range strings.Split(build, ".") {
			if id == "" || !validIdent(id) {
				return Version{}, fmt.Errorf("semver: bad build metadata %q", build)
			}
		}
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("semver: %q is not MAJOR.MINOR.PATCH", s)
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("semver: bad number %q in %q", p, s)
		}
		switch i {
		case 0:
			v.Major = n
		case 1:
			v.Minor = n
		default:
			v.Patch = n
		}
	}
	if hasPre {
		if pre == "" {
			return Version{}, fmt.Errorf("semver: empty prerelease in %q", s)
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" || !validIdent(id) {
				return Version{}, fmt.Errorf("semver: bad prerelease %q", pre)
			}
		}
	}
	return v, nil
}

// validIdent reports whether s uses only [0-9A-Za-z-].
func validIdent(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
		default:
			return false
		}
	}
	return true
}

// String formats v.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// Compare returns -1, 0 or 1 following SemVer 2.0.0 precedence.
func Compare(a, b Version) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1 // a release ranks above its prereleases
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case xerr == nil && yerr == nil:
			if xn != yn {
				if xn < yn {
					return -1
				}
				return 1
			}
		case xerr == nil:
			return -1 // numeric identifiers rank below alphanumeric ones
		case yerr == nil:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}

// Satisfies reports whether v meets every space-separated clause of
// constraint, each "<op>X.Y.Z" with op one of >=, >, <=, <, =. An empty
// constraint or "*" matches everything.
func Satisfies(v Version, constraint string) (bool, error) {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" || constraint == "*" {
		return true, nil
	}
	for _, clause := range strings.Fields(constraint) {
		op := strings.TrimRight(clause, "0123456789.-+abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
		target, err := Parse(strings.TrimPrefix(clause, op))
		if err != nil {
			return false, err
		}
		c := Compare(v, target)
		var ok bool
		switch op {
		case ">=":
			ok = c >= 0
		case ">":
			ok = c > 0
		case "<=":
			ok = c <= 0
		case "<":
			ok = c < 0
		case "=", "":
			ok = c == 0
		default:
			return false, fmt.Errorf("semver: bad operator %q", op)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
