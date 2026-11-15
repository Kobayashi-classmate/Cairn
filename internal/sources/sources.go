// SPDX-License-Identifier: AGPL-3.0-only

// Package sources checks download sources. A probe compares each candidate
// (the official address, mirrors of the upstream's official mirror network,
// approved supplementary mirrors, the address the system uses now) with the
// official source and measures it from this machine: whether it serves
// exactly the official file or index, how far behind it is, how long the
// first response takes and how fast it transfers. Nothing here changes the
// system; the root helper decides what to do with the result.
package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Candidate statuses.
const (
	StatusInSync      = "in_sync"     // the same file or index as the official source
	StatusNewer       = "newer"       // the index differs and is newer than the official one
	StatusUnverified  = "unverified"  // reachable, but the official source could not be compared
	StatusBehind      = "behind"      // the index is older than the official one
	StatusMismatch    = "mismatch"    // a different file (size)
	StatusNotFound    = "not_found"   // missing (404, or a web page instead of the file)
	StatusUnreachable = "unreachable" // no usable answer
	StatusSkipped     = "skipped"     // not compared: slower to answer than the ones that were
)

// Kinds of candidates.
const (
	KindOfficial       = "official"        // the upstream's own address
	KindOfficialMirror = "official_mirror" // listed by the upstream's official mirror network
	KindSupplementary  = "supplementary"   // an approved mirror outside it
	KindOther          = "other"           // configured on this system, not known to the catalog
)

// IDOfficial is the candidate ID of the official address.
const IDOfficial = "official"

// Candidate is one source of a download or repository.
type Candidate struct {
	// ID identifies the candidate within its slot: IDOfficial, a catalog
	// operator ID, or the host of a mirror-network entry.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	// URL is the file (downloads) or the repository base address.
	URL   string `json:"url"`
	InUse bool   `json:"inUse"`

	Status   string `json:"status"`
	Eligible bool   `json:"eligible"`
	// IndexTime is when the candidate's index was generated (unix seconds);
	// LagSeconds how much older it is than the official index.
	IndexTime  int64 `json:"indexTime,omitempty"`
	LagSeconds int64 `json:"lagSeconds,omitempty"`
	// LatencyMS is the time to the first response; Speed the transfer rate
	// over the sample in bytes per second (0: not measured).
	LatencyMS int64 `json:"latencyMs,omitempty"`
	Speed     int64 `json:"speed,omitempty"`
	// Size is the full size of a download in bytes (0 if unknown).
	Size        int64  `json:"size,omitempty"`
	Recommended bool   `json:"recommended"`
	Error       string `json:"error,omitempty"`
}

// Prober runs probes. The zero value works with defaults.
type Prober struct {
	HTTP *http.Client
	// Sample is the size of the speed sample in bytes (default 2 MiB);
	// SampleTime caps the time spent on one sample (default 3 s).
	Sample     int64
	SampleTime time.Duration
	// Shortlist is how many candidates besides the official and the in-use
	// ones are compared in full when there are more (default 3); the others
	// are ranked by their first response only.
	Shortlist int
	// Parallel caps concurrent requests (default 8).
	Parallel int

	once sync.Once
	sem  chan struct{}
}

// minSample is the least a speed sample must transfer to count (a
// variable for tests).
var minSample int64 = 128 << 10

const (
	userAgent  = "Cairn"
	maxIndex   = 16 << 20
	maxList    = 1 << 20
	reqTimeout = 6 * time.Second
)

func (p *Prober) init() {
	p.once.Do(func() {
		n := p.Parallel
		if n <= 0 {
			n = 8
		}
		p.sem = make(chan struct{}, n)
	})
}

func (p *Prober) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: reqTimeout}}
}

func (p *Prober) sample() int64 {
	if p.Sample > 0 {
		return p.Sample
	}
	return 2 << 20
}

func (p *Prober) sampleTime() time.Duration {
	if p.SampleTime > 0 {
		return p.SampleTime
	}
	return 3 * time.Second
}

func (p *Prober) shortlist() int {
	if p.Shortlist > 0 {
		return p.Shortlist
	}
	return 3
}

func (p *Prober) acquire(ctx context.Context) error {
	p.init()
	select {
	case p.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Prober) release() { <-p.sem }

// errNotFound marks a missing resource (404/410, or a web page served in
// place of a file, as some mirrors do).
var errNotFound = errors.New("not found")

// measure is the outcome of one sampled request.
type measure struct {
	total   int64 // full size of the resource, 0 if unknown
	latency time.Duration
	speed   int64
	err     error
}

func newRequest(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

func checkResponse(res *http.Response) error {
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return errNotFound
	case res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent:
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); mt == "text/html" {
		return errNotFound
	}
	return nil
}

// sampleURL requests the first n bytes of u and measures the first response
// and the transfer rate.
func (p *Prober) sampleURL(ctx context.Context, u string, n int64) measure {
	if err := p.acquire(ctx); err != nil {
		return measure{err: err}
	}
	defer p.release()
	ctx, cancel := context.WithTimeout(ctx, reqTimeout+p.sampleTime())
	defer cancel()
	req, err := newRequest(ctx, u)
	if err != nil {
		return measure{err: err}
	}
	req.Header.Set("Range", "bytes=0-"+strconv.FormatInt(n-1, 10))
	start := time.Now()
	res, err := p.client().Do(req)
	if err != nil {
		return measure{err: err}
	}
	defer res.Body.Close()
	m := measure{latency: time.Since(start)}
	if m.err = checkResponse(res); m.err != nil {
		return m
	}
	m.total = res.ContentLength
	if res.StatusCode == http.StatusPartialContent {
		m.total = rangeTotal(res.Header.Get("Content-Range"))
	}
	// Read the sample, or as much as arrives within the sample time. The
	// rate is taken over the part after the first quarter, past the TCP
	// slow start that dominates small transfers; samples too small to tell
	// (an index of a few KB) report no speed.
	first := time.Now()
	deadline := first.Add(p.sampleTime())
	buf := make([]byte, 32<<10)
	var got, warm int64
	var warmAt time.Time
	for got < n && time.Now().Before(deadline) {
		k, err := res.Body.Read(buf[:min(int64(len(buf)), n-got)])
		got += int64(k)
		if warmAt.IsZero() && got >= n/4 {
			warm, warmAt = got, time.Now()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && got == 0 {
				m.err = err
				return m
			}
			break
		}
	}
	if got >= minSample {
		if d := time.Since(warmAt); !warmAt.IsZero() && got-warm >= max(minSample/2, 1) && d > 0 {
			m.speed = int64(float64(got-warm) / d.Seconds())
		} else if d := time.Since(first); d > 0 {
			m.speed = int64(float64(got) / d.Seconds())
		}
	}
	return m
}

// rangeTotal returns the full length from a Content-Range header
// ("bytes 0-1048575/31234567"), or 0.
func rangeTotal(h string) int64 {
	_, total, ok := strings.Cut(h, "/")
	if !ok {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// fetch downloads a small document (an index or a mirror list).
func (p *Prober) fetch(ctx context.Context, u string, limit int64) ([]byte, time.Duration, error) {
	if err := p.acquire(ctx); err != nil {
		return nil, 0, err
	}
	defer p.release()
	ctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	req, err := newRequest(ctx, u)
	if err != nil {
		return nil, 0, err
	}
	start := time.Now()
	res, err := p.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	latency := time.Since(start)
	if res.StatusCode == http.StatusPartialContent {
		return nil, latency, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if err := checkResponse(res); err != nil {
		return nil, latency, err
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("response too large")
	}
	return data, latency, err
}

// FetchList downloads a mirror registry's answer.
func (p *Prober) FetchList(ctx context.Context, u string) ([]byte, error) {
	data, _, err := p.fetch(ctx, u, maxList)
	return data, err
}

// failed records why a candidate could not be used.
func failed(c *Candidate, err error) {
	c.Eligible = false
	if errors.Is(err, errNotFound) {
		c.Status = StatusNotFound
		return
	}
	c.Status = StatusUnreachable
	c.Error = shortError(err)
}

func shortError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// parallel runs f for every index concurrently (the prober's request cap
// still applies) and waits.
func parallel(idxs []int, f func(i int)) {
	var wg sync.WaitGroup
	for _, i := range idxs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(i)
		}()
	}
	wg.Wait()
}

func all(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// ProbeFiles checks candidates serving one download. The first candidate
// must be the official one; the others must serve a file of the same size
// (the installer verifies the official checksum after the download).
func (p *Prober) ProbeFiles(ctx context.Context, cands []Candidate) {
	ms := make([]measure, len(cands))
	parallel(all(len(cands)), func(i int) { ms[i] = p.sampleURL(ctx, cands[i].URL, p.sample()) })
	officialSize := ms[0].total
	if ms[0].err != nil {
		officialSize = 0
	}
	for i := range cands {
		c, m := &cands[i], ms[i]
		c.LatencyMS, c.Speed = m.latency.Milliseconds(), m.speed
		if m.err == nil {
			c.Size = m.total
		}
		switch {
		case m.err != nil:
			failed(c, m.err)
		case i == 0:
			c.Status, c.Eligible = StatusInSync, true
		case officialSize == 0:
			c.Status, c.Eligible = StatusUnverified, true
		case m.total != officialSize:
			c.Status = StatusMismatch
		default:
			c.Status, c.Eligible = StatusInSync, true
		}
	}
}

// rank orders the indexes of the candidates that are neither the official
// one (index 0) nor in use by their first response, measured with probe,
// and marks the ones beyond the shortlist as skipped. It returns the
// indexes to compare in full.
func (p *Prober) rank(ctx context.Context, cands []Candidate, probe func(c *Candidate) string) []int {
	full := []int{0}
	var rest []int
	for i := 1; i < len(cands); i++ {
		if cands[i].InUse {
			full = append(full, i)
		} else {
			rest = append(rest, i)
		}
	}
	if len(rest) <= p.shortlist() {
		return append(full, rest...)
	}
	lat := make([]time.Duration, len(cands))
	parallel(rest, func(i int) {
		m := p.sampleURL(ctx, probe(&cands[i]), 1)
		if m.err != nil {
			failed(&cands[i], m.err)
			lat[i] = -1
			return
		}
		lat[i] = m.latency
		cands[i].LatencyMS = m.latency.Milliseconds()
	})
	var ok []int
	for _, i := range rest {
		if lat[i] >= 0 {
			ok = append(ok, i)
		}
	}
	for a := 1; a < len(ok); a++ { // insertion sort by latency
		for b := a; b > 0 && lat[ok[b]] < lat[ok[b-1]]; b-- {
			ok[b], ok[b-1] = ok[b-1], ok[b]
		}
	}
	n := min(p.shortlist(), len(ok))
	for _, i := range ok[n:] {
		cands[i].Status = StatusSkipped
	}
	return append(full, ok[:n]...)
}

// lagStatus sets the status of a candidate whose index differs from the
// official one.
func lagStatus(c *Candidate, official, got time.Time) {
	switch {
	case official.IsZero() || got.IsZero():
		c.Status, c.Eligible = StatusUnverified, true
	case got.Before(official):
		c.Status, c.LagSeconds = StatusBehind, int64(official.Sub(got).Seconds())
	default:
		c.Status, c.Eligible = StatusNewer, true
	}
}

// Fastest returns the index of the fastest eligible candidate among those
// allowed, preferring the earlier one on a tie or when nothing was
// measured, or -1.
func Fastest(cands []Candidate, allowed func(Candidate) bool) int {
	best, speed := -1, int64(-1)
	for i, c := range cands {
		if c.Eligible && allowed(c) && c.Speed > speed {
			best, speed = i, c.Speed
		}
	}
	return best
}

// SameBase reports whether two repository addresses are the same, ignoring
// the scheme and a trailing slash.
func SameBase(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(s, "/")
		if _, rest, ok := strings.Cut(s, "://"); ok {
			return strings.ToLower(rest)
		}
		return strings.ToLower(s)
	}
	return norm(a) == norm(b)
}

// Host returns the host name of an address.
func Host(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}
