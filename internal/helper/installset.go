// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// setLocks are the locks of a pkg.install_set operation: the package
// manager for lines from repositories and for the system libraries of
// runtimes, the runtimes directory for runtimes.
func (s *Software) setLocks(req helperapi.InstallSetRequest) []string {
	pkg, rt := false, false
	for _, it := range req.Items {
		_, o, err := s.installableOffer(it)
		switch {
		case err != nil:
		case o.Method == catalog.MethodStandalone:
			rt = true
			pkg = pkg || len(o.Packages) > 0
		default:
			pkg = true
		}
	}
	var locks []string
	if pkg {
		locks = append(locks, LockPkg)
	}
	if rt {
		locks = append(locks, LockRuntimes)
	}
	return locks
}

// installSet implements pkg.install_set: the lines from the package manager
// are installed in one transaction (with the system libraries the runtimes
// need), while the runtimes download meanwhile; the runtimes are unpacked
// and checked once the libraries are in place. One line failing does not
// stop the others; each item of the result says how its line ended.
func (s *Software) installSet(ctx context.Context, _ helperapi.Caller, req helperapi.InstallSetRequest, log io.Writer) (any, error) {
	items := make([]lineItem, 0, len(req.Items))
	for _, r := range req.Items {
		sw, o, err := s.installableOffer(r)
		if err != nil {
			return nil, err
		}
		if o.Method != catalog.MethodStandalone && r.Source != "" && r.Source != sources.IDOfficial {
			return nil, pkgErr(helperapi.CodePkgSourceUnknown, map[string]any{"source": r.Source, "software": r.Software})
		}
		items = append(items, lineItem{req: r, sw: sw, o: o})
	}
	var mu sync.Mutex
	main := &lineWriter{mu: &mu, w: log}
	defer main.Flush()
	var pkgItems []lineItem
	var pkgIdx, rtIdx []int
	var libs []string
	var names []string
	for i, it := range items {
		names = append(names, it.sw.Name+" "+it.o.Line)
		if it.o.Method == catalog.MethodStandalone {
			rtIdx = append(rtIdx, i)
			libs = append(libs, it.o.Packages...)
		} else {
			pkgIdx = append(pkgIdx, i)
			pkgItems = append(pkgItems, it)
		}
	}
	_, _ = fmt.Fprintf(main, "Installing %s.\n", strings.Join(names, ", "))
	res := helperapi.InstallSetResult{Items: make([]helperapi.InstallSetItem, len(items))}
	for i, it := range items {
		res.Items[i] = helperapi.InstallSetItem{Software: it.sw.ID, Line: it.o.Line}
	}

	// Runtime downloads, one after another, while the package manager works.
	fetches := make([]*fetched, len(items))
	errs := make([]error, len(items))
	writers := map[int]*lineWriter{}
	for _, i := range rtIdx {
		writers[i] = &lineWriter{mu: &mu, w: log, prefix: "[" + items[i].sw.Name + " " + items[i].o.Line + "] "}
	}
	var wg sync.WaitGroup
	if len(rtIdx) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, i := range rtIdx {
				if s.Runtimes == nil {
					errs[i] = pkgErr(helperapi.CodePkgNotAvailable, map[string]any{"software": items[i].sw.ID})
					continue
				}
				it := items[i]
				fetches[i], errs[i] = s.Runtimes.fetch(ctx, writers[i], it.sw.ID, it.o.Provider, it.o.Line, it.req.Source, it.req.Policy)
			}
		}()
	}
	var libErr error
	if len(pkgItems) > 0 || len(libs) > 0 {
		var out []pkgOutcome
		out, libErr = s.installPackages(ctx, main, pkgItems, libs)
		for k, i := range pkgIdx {
			if out[k].err != nil {
				errs[i] = out[k].err
			} else {
				r := out[k].res
				res.Items[i].Result = &r
			}
		}
	}
	wg.Wait()
	for _, i := range rtIdx {
		it := items[i]
		if errs[i] == nil && libErr != nil && len(it.o.Packages) > 0 {
			errs[i] = libErr
			fetches[i].discard()
		}
		if errs[i] == nil {
			var v string
			if v, errs[i] = s.Runtimes.finish(ctx, writers[i], fetches[i]); errs[i] == nil {
				r := runtimeResult(it.sw, it.o, v)
				res.Items[i].Result = &r
				s.Log.Info("runtime installed", "software", it.sw.ID, "line", it.o.Line, "version", v)
			}
		}
		writers[i].Flush()
	}

	var failed []string
	for i := range items {
		if errs[i] != nil {
			res.Items[i].Error = asError(errs[i])
			failed = append(failed, names[i])
		}
	}
	if len(failed) > 0 {
		_, _ = fmt.Fprintf(main, "Not installed: %s.\n", strings.Join(failed, ", "))
	} else {
		_, _ = fmt.Fprintf(main, "All %d installed.\n", len(items))
	}
	return res, nil
}

// asError gives a line's failure the protocol's error form.
func asError(err error) *helperapi.Error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return he
	}
	return &helperapi.Error{Code: helperapi.CodeOpFailed, Params: map[string]any{"reason": tailString(err.Error(), 300)}}
}

// lineWriter writes whole lines to a log shared by concurrent steps of one
// operation, each line with the step's prefix, so that their output does
// not interleave mid-line.
type lineWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.emit(l.buf[:i+1])
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 64<<10 { // a very long line without a newline
		l.emit(append(l.buf, '\n'))
		l.buf = nil
	}
	return len(p), nil
}

func (l *lineWriter) emit(line []byte) {
	_, _ = io.WriteString(l.w, l.prefix)
	_, _ = l.w.Write(line)
}

// Flush writes what is left of an unfinished line.
func (l *lineWriter) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.emit(append(l.buf, '\n'))
		l.buf = nil
	}
}
