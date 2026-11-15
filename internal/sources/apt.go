// SPDX-License-Identifier: AGPL-3.0-only

package sources

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Release is the part of an apt Release/InRelease file the probe uses.
type Release struct {
	Date time.Time
	// SHA256 maps index paths (main/binary-amd64/Packages) to their hash.
	SHA256 map[string]string
	// Sizes maps index paths to their size in bytes.
	Sizes map[string]int64
}

// ParseRelease reads a Release or (clear-signed) InRelease file. The
// signature is not checked: apt checks it with the pinned key before it
// uses an index; the probe only compares indexes.
func ParseRelease(data []byte) (Release, error) {
	r := Release{SHA256: map[string]string{}, Sizes: map[string]int64{}}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	inHeader := bytes.HasPrefix(data, []byte("-----BEGIN PGP SIGNED MESSAGE-----"))
	field := ""
	for sc.Scan() {
		line := sc.Text()
		if inHeader { // armor headers up to the first blank line
			if strings.TrimSpace(line) == "" {
				inHeader = false
			}
			continue
		}
		if strings.HasPrefix(line, "-----BEGIN PGP SIGNATURE-----") {
			break
		}
		if strings.HasPrefix(line, " ") {
			if field == "sha256" {
				f := strings.Fields(line)
				if len(f) == 3 {
					r.SHA256[f[2]] = f[0]
					if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
						r.Sizes[f[2]] = n
					}
				}
			}
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			field = ""
			continue
		}
		field = strings.ToLower(name)
		if field == "date" {
			r.Date = parseDate(strings.TrimSpace(value))
		}
	}
	if err := sc.Err(); err != nil {
		return r, err
	}
	if len(r.SHA256) == 0 {
		return r, errors.New("no SHA256 index list")
	}
	return r, nil
}

func parseDate(s string) time.Time {
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// APTEntry is one (URI, suite) pair configured in the system's apt sources.
type APTEntry struct {
	File       string
	URI        string
	Suite      string
	Components []string
}

// ParseAPTSources reads apt source files: one-line .list files and deb822
// .sources files, keyed by path. Disabled stanzas and deb-src are skipped.
func ParseAPTSources(files map[string][]byte) []APTEntry {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []APTEntry
	for _, name := range names {
		if strings.HasSuffix(name, ".sources") {
			out = append(out, parseDeb822(name, files[name])...)
		} else {
			out = append(out, parseOneLine(name, files[name])...)
		}
	}
	return out
}

func parseOneLine(name string, data []byte) []APTEntry {
	var out []APTEntry
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != "deb" {
			continue
		}
		f = f[1:]
		if strings.HasPrefix(f[0], "[") { // options: [arch=amd64 signed-by=...]
			for len(f) > 0 && !strings.HasSuffix(f[0], "]") {
				f = f[1:]
			}
			if len(f) == 0 {
				continue
			}
			f = f[1:]
		}
		if len(f) < 2 {
			continue
		}
		out = append(out, APTEntry{File: name, URI: f[0], Suite: f[1], Components: f[2:]})
	}
	return out
}

func parseDeb822(name string, data []byte) []APTEntry {
	var out []APTEntry
	flush := func(st map[string]string) {
		if len(st) == 0 || strings.EqualFold(st["enabled"], "no") || !contains(strings.Fields(st["types"]), "deb") {
			return
		}
		for _, uri := range strings.Fields(st["uris"]) {
			for _, suite := range strings.Fields(st["suites"]) {
				out = append(out, APTEntry{File: name, URI: uri, Suite: suite, Components: strings.Fields(st["components"])})
			}
		}
	}
	st := map[string]string{}
	key := ""
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.TrimSpace(line) == "":
			flush(st)
			st, key = map[string]string{}, ""
		case line[0] == ' ' || line[0] == '\t':
			if key != "" {
				st[key] += " " + strings.TrimSpace(line)
			}
		default:
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(k))
			st[key] = strings.TrimSpace(v)
		}
	}
	flush(st)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// APTTarget says what to compare in apt repositories.
type APTTarget struct {
	Suites     []string // the first one also serves the speed sample
	Components []string // compared components, e.g. main
	Arch       string   // dpkg architecture: amd64, arm64
}

func (t APTTarget) indexes() []string {
	out := make([]string, 0, len(t.Components))
	for _, c := range t.Components {
		out = append(out, c+"/binary-"+t.Arch+"/Packages")
	}
	return out
}

func distsURL(base, suite string) string {
	return strings.TrimSuffix(base, "/") + "/dists/" + suite + "/"
}

// ProbeAPT compares apt repositories with the official one, cands[0]. A
// candidate is in sync when every compared Packages index of every suite
// has the official hash; otherwise the index dates tell whether it is
// behind. The speed sample is the first suite's compressed Packages index.
func (p *Prober) ProbeAPT(ctx context.Context, cands []Candidate, t APTTarget) {
	if len(cands) == 0 || len(t.Suites) == 0 {
		return
	}
	full := p.rank(ctx, cands, func(c *Candidate) string { return distsURL(c.URL, t.Suites[0]) + "InRelease" })
	releases := make([][]*Release, len(cands)) // [candidate][suite]
	errs := make([]error, len(cands))
	latency := make([]time.Duration, len(cands))
	parallel(full, func(i int) {
		c := &cands[i]
		releases[i] = make([]*Release, len(t.Suites))
		for s, suite := range t.Suites {
			data, lat, err := p.fetch(ctx, distsURL(c.URL, suite)+"InRelease", maxIndex)
			if s == 0 {
				latency[i] = lat
			}
			if err != nil {
				errs[i] = err
				return
			}
			r, err := ParseRelease(data)
			if err != nil {
				errs[i] = err
				return
			}
			releases[i][s] = &r
		}
	})
	ref := releases[0]
	officialOK := errs[0] == nil
	for _, i := range full {
		c := &cands[i]
		if latency[i] > 0 {
			c.LatencyMS = latency[i].Milliseconds()
		}
		if errs[i] != nil {
			failed(c, errs[i])
			continue
		}
		c.IndexTime = releases[i][0].Date.Unix()
		if releases[i][0].Date.IsZero() {
			c.IndexTime = 0
		}
		switch {
		case i == 0:
			c.Status, c.Eligible = StatusInSync, true
		case !officialOK:
			c.Status, c.Eligible = StatusUnverified, true
		default:
			p.compareAPT(c, ref, releases[i], t)
		}
	}
	// Speed samples, for the candidates that answered.
	parallel(full, func(i int) {
		if errs[i] != nil {
			return
		}
		u := distsURL(cands[i].URL, t.Suites[0]) + sampleIndex(releases[i][0], t)
		if m := p.sampleURL(ctx, u, p.sample()); m.err == nil {
			cands[i].Speed = m.speed
		}
	})
}

func (p *Prober) compareAPT(c *Candidate, ref, got []*Release, t APTTarget) {
	same := true
	var lagOfficial, lagGot time.Time
	for s := range t.Suites {
		for _, ix := range t.indexes() {
			want, have := ref[s].SHA256[ix], got[s].SHA256[ix]
			if want == "" {
				continue // the official suite lacks it too (e.g. no arm64)
			}
			if have == "" {
				c.Status = StatusNotFound
				return
			}
			if want != have {
				same = false
				if lagOfficial.IsZero() || got[s].Date.Before(lagGot) {
					lagOfficial, lagGot = ref[s].Date, got[s].Date
				}
			}
		}
	}
	if same {
		c.Status, c.Eligible = StatusInSync, true
		return
	}
	lagStatus(c, lagOfficial, lagGot)
}

// sampleIndex picks the largest compressed Packages index of the target
// listed in a Release file (mirrors often do not serve the uncompressed
// one), to sample the transfer rate.
func sampleIndex(r *Release, t APTTarget) string {
	best, size := "", int64(-1)
	for _, ix := range t.indexes() {
		for _, ext := range []string{".xz", ".gz", ".bz2"} {
			if n, ok := r.Sizes[ix+ext]; ok {
				if n > size {
					best, size = ix+ext, n
				}
				break
			}
		}
	}
	if best == "" {
		return path.Join(t.Components[0], "binary-"+t.Arch, "Packages.xz")
	}
	return best
}
