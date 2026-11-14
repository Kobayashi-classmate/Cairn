// SPDX-License-Identifier: AGPL-3.0-only

// Package locks provides named locks for background work: a waiter blocks
// until every name it needs is free. Both the root helper (operations) and
// the web process (jobs) use it.
package locks

import (
	"context"
	"sync"
	"time"
)

// RecheckInterval is how often a waiter re-checks external holders (see
// Acquire), which cannot signal when they finish.
var RecheckInterval = 2 * time.Second

// Set is a set of named locks. The zero value is ready to use.
type Set struct {
	mu      sync.Mutex
	holders map[string]bool
	changed chan struct{}
}

// Held reports whether name is currently held.
func (s *Set) Held(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holders[name]
}

// TryAcquire takes all names if every one is free. The returned function
// releases them.
func (s *Set) TryAcquire(names []string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		if s.holders[n] {
			return nil, false
		}
	}
	if s.holders == nil {
		s.holders = map[string]bool{}
	}
	for _, n := range names {
		s.holders[n] = true
	}
	return func() { s.release(names) }, true
}

func (s *Set) release(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range names {
		delete(s.holders, n)
	}
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// Acquire waits until all names are free and no external holder reports one
// of them busy (busy[name]() true), then takes them. It returns ctx's error
// if ctx ends first.
func (s *Set) Acquire(ctx context.Context, names []string, busy map[string]func() bool) (func(), error) {
	for {
		externallyBusy := false
		for _, n := range names {
			if f := busy[n]; f != nil && f() {
				externallyBusy = true
			}
		}
		if !externallyBusy {
			if release, ok := s.TryAcquire(names); ok {
				return release, nil
			}
		}
		s.mu.Lock()
		if s.changed == nil {
			s.changed = make(chan struct{})
		}
		ch := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ch:
		case <-time.After(RecheckInterval):
		}
	}
}
