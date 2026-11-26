// SPDX-License-Identifier: AGPL-3.0-only

package sources

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Repomd is the part of a dnf repodata/repomd.xml file the probe uses.
type Repomd struct {
	// PrimaryChecksum identifies the package list; equal checksums mean
	// equal repositories.
	PrimaryChecksum string
	PrimaryLocation string // relative to the repository base
	PrimarySize     int64
	// Time is the newest timestamp of the listed data.
	Time time.Time
}

// ParseRepomd reads a repomd.xml file.
func ParseRepomd(data []byte) (Repomd, error) {
	var doc struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Checksum string `xml:"checksum"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
			Timestamp int64 `xml:"timestamp"`
			Size      int64 `xml:"size"`
		} `xml:"data"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return Repomd{}, err
	}
	var r Repomd
	var newest int64
	for _, d := range doc.Data {
		newest = max(newest, d.Timestamp)
		if d.Type == "primary" {
			r.PrimaryChecksum = strings.TrimSpace(d.Checksum)
			r.PrimaryLocation = d.Location.Href
			r.PrimarySize = d.Size
		}
	}
	if r.PrimaryChecksum == "" || r.PrimaryLocation == "" || strings.Contains(r.PrimaryLocation, "..") {
		return Repomd{}, errors.New("repomd.xml without a primary index")
	}
	if newest > 0 {
		r.Time = time.Unix(newest, 0)
	}
	return r, nil
}

// DNFSection is one section of a .repo file.
type DNFSection struct {
	File       string
	ID         string
	Enabled    bool
	BaseURLs   []string
	MirrorList string
	Metalink   string
}

// ParseRepoFiles reads .repo files keyed by path.
func ParseRepoFiles(files map[string][]byte) []DNFSection {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []DNFSection
	for _, name := range names {
		var cur *DNFSection
		key := ""
		for _, line := range strings.Split(string(files[name]), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";"):
				continue
			case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
				out = append(out, DNFSection{File: name, ID: strings.TrimSpace(trimmed[1 : len(trimmed)-1]), Enabled: true})
				cur, key = &out[len(out)-1], ""
				continue
			}
			if cur == nil {
				continue
			}
			if (line[0] == ' ' || line[0] == '\t') && key == "baseurl" { // continuation
				cur.BaseURLs = append(cur.BaseURLs, strings.Fields(trimmed)...)
				continue
			}
			k, v, ok := strings.Cut(trimmed, "=")
			if !ok {
				continue
			}
			key, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
			switch key {
			case "enabled":
				cur.Enabled = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
			case "baseurl":
				cur.BaseURLs = append(cur.BaseURLs, strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' })...)
			case "mirrorlist":
				cur.MirrorList = v
			case "metalink":
				cur.Metalink = v
			}
		}
	}
	return out
}

var dnfVar = regexp.MustCompile(`\$\{?([A-Za-z0-9_]+)\}?`)

// ExpandVars replaces dnf variables ($basearch, ${releasever}) that vars
// defines; unknown ones are left as they are.
func ExpandVars(s string, vars map[string]string) string {
	return dnfVar.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.Trim(m, "${}")
		if v, ok := vars[name]; ok {
			return v
		}
		return m
	})
}

// ParseMirrorList reads a plain mirror list: one URL per line, comments
// and anything that is not http(s) skipped.
func ParseMirrorList(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") || strings.HasPrefix(line, "http://") {
			out = append(out, line)
		}
	}
	return out
}

// ParseMetalink reads a metalink document and returns the repository base
// addresses it lists, most preferred first.
func ParseMetalink(data []byte) []string {
	var doc struct {
		URLs []struct {
			Protocol   string `xml:"protocol,attr"`
			Preference int    `xml:"preference,attr"`
			Value      string `xml:",chardata"`
		} `xml:"files>file>resources>url"`
	}
	if err := xml.NewDecoder(bytes.NewReader(data)).Decode(&doc); err != nil {
		return nil
	}
	sort.SliceStable(doc.URLs, func(a, b int) bool { return doc.URLs[a].Preference > doc.URLs[b].Preference })
	var out []string
	for _, u := range doc.URLs {
		v := strings.TrimSpace(u.Value)
		if (u.Protocol != "https" && u.Protocol != "http") || !strings.HasSuffix(v, "repodata/repomd.xml") {
			continue
		}
		out = append(out, strings.TrimSuffix(v, "repodata/repomd.xml"))
	}
	return out
}

func repoBase(u string) string {
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	return u
}

// ProbeDNF compares dnf repositories with the official one, cands[0], by
// the checksum of their package list; the speed sample is that list.
func (p *Prober) ProbeDNF(ctx context.Context, cands []Candidate) {
	if len(cands) == 0 {
		return
	}
	full := p.rank(ctx, cands, func(c *Candidate) string { return repoBase(c.Addr()) + "repodata/repomd.xml" })
	mds := make([]Repomd, len(cands))
	errs := make([]error, len(cands))
	latency := make([]time.Duration, len(cands))
	parallel(full, func(i int) {
		data, lat, err := p.fetch(ctx, repoBase(cands[i].Addr())+"repodata/repomd.xml", maxIndex)
		latency[i] = lat
		if err == nil {
			mds[i], err = ParseRepomd(data)
		}
		errs[i] = err
	})
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
		if !mds[i].Time.IsZero() {
			c.IndexTime = mds[i].Time.Unix()
		}
		switch {
		case i == 0 || (officialOK && mds[i].PrimaryChecksum == mds[0].PrimaryChecksum):
			c.Status, c.Eligible = StatusInSync, true
		case !officialOK:
			c.Status, c.Eligible = StatusUnverified, true
		default:
			lagStatus(c, mds[0].Time, mds[i].Time)
		}
	}
	parallel(full, func(i int) {
		if errs[i] != nil {
			return
		}
		if m := p.sampleURL(ctx, repoBase(cands[i].Addr())+mds[i].PrimaryLocation, p.sample()); m.err == nil {
			cands[i].Speed = m.speed
		}
	})
}
