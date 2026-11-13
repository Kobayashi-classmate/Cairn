// SPDX-License-Identifier: AGPL-3.0-only

package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// OSRelease holds the fields of os-release(5) that Cairn relies on.
type OSRelease struct {
	ID         string
	IDLike     []string
	VersionID  string
	Name       string
	PrettyName string
	Codename   string
}

// osReleasePaths lists the standard locations, in lookup order.
var osReleasePaths = []string{"/etc/os-release", "/usr/lib/os-release"}

// ReadOSRelease reads the os-release file of the running system.
func ReadOSRelease() (OSRelease, error) {
	var lastErr error
	for _, p := range osReleasePaths {
		f, err := os.Open(p) //nolint:gosec // G304: p is one of the fixed os-release paths.
		if err != nil {
			lastErr = err
			continue
		}
		defer f.Close()
		return ParseOSRelease(f)
	}
	return OSRelease{}, fmt.Errorf("platform: no os-release file found: %w", lastErr)
}

// ParseOSRelease parses the os-release(5) format: KEY=VALUE lines, where
// values may be unquoted, single-quoted or double-quoted with backslash
// escapes. Unknown keys are ignored.
func ParseOSRelease(r io.Reader) (OSRelease, error) {
	var osr OSRelease
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val, err := unquote(raw)
		if err != nil {
			return OSRelease{}, fmt.Errorf("platform: os-release key %s: %w", key, err)
		}
		switch key {
		case "ID":
			osr.ID = strings.ToLower(val)
		case "ID_LIKE":
			osr.IDLike = strings.Fields(strings.ToLower(val))
		case "VERSION_ID":
			osr.VersionID = val
		case "NAME":
			osr.Name = val
		case "PRETTY_NAME":
			osr.PrettyName = val
		case "VERSION_CODENAME":
			osr.Codename = val
		}
	}
	if err := sc.Err(); err != nil {
		return OSRelease{}, err
	}
	if osr.ID == "" {
		// os-release(5): ID defaults to "linux" when absent.
		osr.ID = "linux"
	}
	return osr, nil
}

func unquote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s, nil
	}
	switch s[0] {
	case '"':
		if s[len(s)-1] != '"' {
			return "", errors.New("unterminated double quote")
		}
		// Shell-style double quotes: only \", \\, \$ and \` are escapes.
		inner := s[1 : len(s)-1]
		var b strings.Builder
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if c == '\\' && i+1 < len(inner) && strings.IndexByte("\"\\$`", inner[i+1]) >= 0 {
				i++
				c = inner[i]
			}
			b.WriteByte(c)
		}
		return b.String(), nil
	case '\'':
		if s[len(s)-1] != '\'' {
			return "", errors.New("unterminated single quote")
		}
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

// compareVersions compares dotted numeric versions such as "24.04" and "9".
// Missing components count as zero; non-numeric components compare as zero.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := max(len(as), len(bs))
	for i := range n {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
