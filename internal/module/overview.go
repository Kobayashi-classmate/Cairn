// SPDX-License-Identifier: AGPL-3.0-only

package module

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/authz"
)

// Summarizer is implemented by modules that report on the overview page:
// the services they run, how their part of the system stands (checks), and
// what needs attention (notices).
type Summarizer interface {
	// Summarize reports the module's part of the overview for the caller of
	// r. The registry calls it only for enabled modules, concurrently with
	// the others, with a deadline on r's context; the module checks its own
	// read permission with can and reports nothing the caller may not read.
	Summarize(r *http.Request, can func(authz.Permission) bool) (Summary, error)
}

// Summary is a module's part of the overview.
type Summary struct {
	Services []Service
	Checks   []Check
	Notices  []Check
}

// Service is a system service of installed software.
type Service struct {
	// Software and Line name the catalog line the service belongs to.
	Software string `json:"software"`
	Line     string `json:"line"`
	Unit     string `json:"unit"`
	// Version is the installed version of the line's first package.
	Version string `json:"version,omitempty"`
	State   string `json:"state"` // one of the Service* constants
}

// Service states.
const (
	ServiceRunning = "running"
	ServiceStopped = "stopped"
	ServiceFailed  = "failed"
	ServiceUnknown = "unknown"
)

// Check is one finding: a row of the overview's security card, or a notice
// at its top.
type Check struct {
	// Code names the finding ("firewall.on", "host.updates"); the UI words
	// it, with Params.
	Code   string         `json:"code"`
	Status string         `json:"status"` // one of the Status* constants
	Params map[string]any `json:"params,omitempty"`
	// Route is the menu route where the finding is dealt with.
	Route string `json:"route,omitempty"`
}

// Check statuses.
const (
	StatusOK   = "ok"
	StatusWarn = "warn"
	StatusBad  = "bad"
)

// Overview is the merged summary of the enabled modules.
type Overview struct {
	Services []Service `json:"services"`
	Checks   []Check   `json:"checks"`
	Notices  []Check   `json:"notices"`
	// Unavailable lists the modules that failed or did not answer in time.
	Unavailable []string `json:"unavailable"`
}

// Summarize collects the summaries of the enabled modules within timeout,
// in registration order. Modules that fail or are too slow are listed as
// unavailable; errs holds why, for the log.
func (r *Registry) Summarize(req *http.Request, can func(authz.Permission) bool, timeout time.Duration) (ov Overview, errs map[string]error) {
	ctx, cancel := context.WithTimeout(req.Context(), timeout)
	defer cancel()
	req = req.WithContext(ctx)

	type result struct {
		sum  Summary
		err  error
		done bool
	}
	var (
		mu      sync.Mutex
		closed  bool
		results = make([]result, len(r.mods))
		wg      sync.WaitGroup
		asked   []int
	)
	for i, e := range r.mods {
		s, ok := e.m.(Summarizer)
		if !ok || !r.Enabled(e.m.ID()) {
			continue
		}
		asked = append(asked, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			var res result
			func() {
				defer func() {
					if v := recover(); v != nil {
						res.err = fmt.Errorf("panic: %v", v)
					}
				}()
				res.sum, res.err = s.Summarize(req, can)
			}()
			res.done = true
			mu.Lock()
			defer mu.Unlock()
			if !closed {
				results[i] = res
			}
		}()
	}
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	select {
	case <-all:
	case <-ctx.Done():
	}
	mu.Lock()
	closed = true
	mu.Unlock()

	ov = Overview{Services: []Service{}, Checks: []Check{}, Notices: []Check{}, Unavailable: []string{}}
	errs = map[string]error{}
	for _, i := range asked {
		id := r.mods[i].m.ID()
		res := results[i]
		switch {
		case !res.done:
			ov.Unavailable = append(ov.Unavailable, id)
			errs[id] = ctx.Err()
		case res.err != nil:
			ov.Unavailable = append(ov.Unavailable, id)
			errs[id] = res.err
		default:
			ov.Services = append(ov.Services, res.sum.Services...)
			ov.Checks = append(ov.Checks, res.sum.Checks...)
			ov.Notices = append(ov.Notices, res.sum.Notices...)
		}
	}
	return ov, errs
}
