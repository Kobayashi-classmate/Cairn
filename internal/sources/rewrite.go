// SPDX-License-Identifier: AGPL-3.0-only

package sources

import (
	"errors"
	"regexp"
	"strings"
)

// Rewriting the system's repository files to another mirror. The functions
// here only transform text; the helper backs the files up, writes them and
// checks the result with the package manager. Everything else in a file
// (comments, options, other entries) stays byte for byte.

// ErrMixed means a deb822 stanza serves suites of two roles (for example
// the main archive and the security archive) from one address, so it
// cannot move for one role only.
var ErrMixed = errors.New("sources: a stanza mixes suites of different roles")

// RewriteAPT points the apt entries that serve one of suites from the
// address from at to. Stanzas or lines that also serve one of exclude are
// refused with ErrMixed. It returns the changed files and how many entries
// changed.
func RewriteAPT(files map[string][]byte, suites, exclude []string, from, to string) (map[string][]byte, int, error) {
	out := map[string][]byte{}
	total := 0
	for name, data := range files {
		var text string
		var n int
		var err error
		if strings.HasSuffix(name, ".sources") {
			text, n, err = rewriteDeb822(string(data), suites, exclude, from, to)
		} else {
			text, n = rewriteOneLine(string(data), suites, from, to)
		}
		if err != nil {
			return nil, 0, &MixedError{File: name}
		}
		if n > 0 {
			out[name] = []byte(text)
			total += n
		}
	}
	return out, total, nil
}

// MixedError names the file with a mixed stanza.
type MixedError struct{ File string }

func (e *MixedError) Error() string { return ErrMixed.Error() + ": " + e.File }

// Unwrap lets errors.Is match ErrMixed.
func (e *MixedError) Unwrap() error { return ErrMixed }

func intersects(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

// rewriteOneLine handles "deb [options] URI suite components" lines; a
// line has one suite, so it never mixes roles.
func rewriteOneLine(data string, suites []string, from, to string) (string, int) {
	lines := strings.SplitAfter(data, "\n")
	n := 0
	for i, line := range lines {
		body := line
		if j := strings.IndexByte(body, '#'); j >= 0 {
			body = body[:j]
		}
		f := strings.Fields(body)
		if len(f) < 3 || (f[0] != "deb" && f[0] != "deb-src") {
			continue
		}
		k := 1
		if strings.HasPrefix(f[k], "[") {
			for k < len(f) && !strings.HasSuffix(f[k], "]") {
				k++
			}
			k++
		}
		if k+1 >= len(f) || !SameBase(f[k], from) || !contains(suites, f[k+1]) {
			continue
		}
		// Replace the URI field in place, keeping the spacing.
		idx := fieldIndex(line, k)
		if idx < 0 {
			continue
		}
		lines[i] = line[:idx] + to + line[idx+len(f[k]):]
		n++
	}
	return strings.Join(lines, ""), n
}

// fieldIndex returns the byte offset of the k-th whitespace-separated
// field of line.
func fieldIndex(line string, k int) int {
	in, count := false, -1
	for i, r := range line {
		space := r == ' ' || r == '\t'
		if !space && !in {
			count++
			if count == k {
				return i
			}
		}
		in = !space
	}
	return -1
}

// rewriteDeb822 handles .sources stanzas.
func rewriteDeb822(data string, suites, exclude []string, from, to string) (string, int, error) {
	lines := strings.SplitAfter(data, "\n")
	n := 0
	start := 0
	flush := func(end int) error {
		var uriLine = -1
		var stanzaSuites []string
		key := ""
		for i := start; i < end; i++ {
			l := strings.TrimRight(lines[i], "\n")
			switch {
			case strings.HasPrefix(l, "#"):
			case l != "" && (l[0] == ' ' || l[0] == '\t'):
				if key == "suites" {
					stanzaSuites = append(stanzaSuites, strings.Fields(l)...)
				}
			default:
				k, v, ok := strings.Cut(l, ":")
				if !ok {
					continue
				}
				key = strings.ToLower(strings.TrimSpace(k))
				switch key {
				case "uris":
					uriLine = i
				case "suites":
					stanzaSuites = append(stanzaSuites, strings.Fields(v)...)
				}
			}
		}
		if uriLine < 0 || !intersects(stanzaSuites, suites) {
			return nil
		}
		k, v, _ := strings.Cut(strings.TrimRight(lines[uriLine], "\n"), ":")
		uris := strings.Fields(v)
		hit := false
		for j, u := range uris {
			if SameBase(u, from) {
				uris[j], hit = to, true
			}
		}
		if !hit {
			return nil
		}
		if intersects(stanzaSuites, exclude) {
			return ErrMixed
		}
		nl := ""
		if strings.HasSuffix(lines[uriLine], "\n") {
			nl = "\n"
		}
		lines[uriLine] = k + ": " + strings.Join(uris, " ") + nl
		n++
		return nil
	}
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			if err := flush(i); err != nil {
				return "", 0, err
			}
			start = i + 1
		}
	}
	if err := flush(len(lines)); err != nil {
		return "", 0, err
	}
	return strings.Join(lines, ""), n, nil
}

var (
	dnfBaseURL = regexp.MustCompile(`^(#\s*)?baseurl\s*=\s*(\S+)\s*$`)
	dnfList    = regexp.MustCompile(`^(mirrorlist|metalink)\s*=`)
	// A mirror's address of a section: <root>/<version>/<Repo>/<arch>/os/.
	dnfSectionPath = regexp.MustCompile(`^(.*/)[0-9][0-9.]*/[A-Za-z]+/[^/]+/os/?$`)
)

// MirrorRoot derives a mirror's root from the address of one of its
// sections ("https://m.example/rocky/9.6/AppStream/x86_64/os/" ->
// "https://m.example/rocky/"); "" if the address has another shape.
func MirrorRoot(section string) string {
	if m := dnfSectionPath.FindStringSubmatch(section); m != nil {
		return m[1]
	}
	return ""
}

// RewriteDNF points every section whose (possibly commented) baseurl
// starts with one of roots at the same path below mirror, and comments out
// its mirrorlist and metalink: dnf then uses that mirror only. It returns
// the changed files and how many sections changed.
func RewriteDNF(files map[string][]byte, roots []string, mirror string) (map[string][]byte, int) {
	out := map[string][]byte{}
	total := 0
	for name, data := range files {
		lines := strings.SplitAfter(string(data), "\n")
		n := 0
		type section struct{ start, end int }
		var secs []section
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
				if len(secs) > 0 {
					secs[len(secs)-1].end = i
				}
				secs = append(secs, section{start: i, end: len(lines)})
			}
		}
		for _, s := range secs {
			base := -1
			path := ""
			for i := s.start + 1; i < s.end; i++ {
				m := dnfBaseURL.FindStringSubmatch(strings.TrimSpace(lines[i]))
				if m == nil {
					continue
				}
				for _, r := range roots {
					if p, ok := cutRoot(m[2], r); ok {
						base, path = i, p
						break
					}
				}
				if base >= 0 {
					break
				}
			}
			if base < 0 {
				continue
			}
			nl := "\n"
			if !strings.HasSuffix(lines[base], "\n") {
				nl = ""
			}
			lines[base] = "baseurl=" + mirror + path + nl
			for i := s.start + 1; i < s.end; i++ {
				if dnfList.MatchString(strings.TrimSpace(lines[i])) {
					lines[i] = "#" + strings.TrimLeft(lines[i], " \t")
				}
			}
			n++
		}
		if n > 0 {
			out[name] = []byte(strings.Join(lines, ""))
			total += n
		}
	}
	return out, total
}

// cutRoot strips root from u, ignoring the scheme.
func cutRoot(u, root string) (string, bool) {
	strip := func(s string) string {
		if _, rest, ok := strings.Cut(s, "://"); ok {
			return rest
		}
		return s
	}
	return strings.CutPrefix(strip(u), strip(root))
}
