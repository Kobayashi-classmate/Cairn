// SPDX-License-Identifier: AGPL-3.0-only

// Package events is the in-process event bus. Modules publish what happened
// (a lockout, an account change, ...); the notification center and, later,
// triggers subscribe. Events carry structured parameters, never secrets.
package events

import (
	"sync"
	"time"
)

// Level orders events by urgency.
type Level string

// Levels, lowest first.
const (
	Info     Level = "info"
	Warning  Level = "warning"
	Critical Level = "critical"
)

// Rank returns a comparable number for l; unknown levels rank lowest.
func (l Level) Rank() int {
	switch l {
	case Warning:
		return 1
	case Critical:
		return 2
	}
	return 0
}

// ValidLevel reports whether s is a known level.
func ValidLevel(s string) bool {
	switch Level(s) {
	case Info, Warning, Critical:
		return true
	}
	return false
}

// Event types published in P0. Notification rules match on prefixes such as
// "security." or "users.".
const (
	SecurityLockout      = "security.lockout"
	SecurityLogin        = "security.login"
	SecurityTOTPDisabled = "security.totp_disabled"
	UsersChange          = "users.change"
	SystemTest           = "system.test"
	CloudAnnouncement    = "cloud.announcement"
	// CertRenewFailed: an automatic renewal failed; CertExpiring: a
	// certificate in use expires within a week.
	CertRenewFailed = "cert.renew_failed"
	CertExpiring    = "cert.expiring"
	// AppUnhealthy: an app failed its health checks and was restarted.
	AppUnhealthy = "app.unhealthy"
	// CronFailed: a scheduled task's run failed; BackupFailed: a backup
	// plan's run failed.
	CronFailed   = "cron.failed"
	BackupFailed = "backup.failed"
	// JobFailed: a background job failed (published by the jobs package).
	JobFailed = "system.job_failed"
	// SSHLogin: someone logged in over SSH; FirewallRolledBack: a firewall
	// change was undone because nobody confirmed it in time;
	// FirewallRollbackFailed: undoing one failed, so its rules are still
	// in force.
	SSHLogin               = "ssh.login"
	FirewallRolledBack     = "firewall.rolled_back"
	FirewallRollbackFailed = "firewall.rollback_failed"
)

// Types lists every event type; each has a notification text.
var Types = []string{SecurityLockout, SecurityLogin, SecurityTOTPDisabled, UsersChange, SystemTest, CloudAnnouncement,
	CertRenewFailed, CertExpiring, AppUnhealthy, CronFailed, BackupFailed, JobFailed, SSHLogin, FirewallRolledBack,
	FirewallRollbackFailed}

// Categories are the prefixes a notification channel can subscribe to.
var Categories = []string{"security.", "users.", "system.", "cloud.", "cert.", "app.", "cron.", "backup.", "ssh.", "firewall."}

// Event is something that happened.
type Event struct {
	Type   string
	Level  Level
	Params map[string]any
	// DedupeKey suppresses repeats of the same occurrence for a while
	// (for example one lockout message per user and address).
	DedupeKey string
	TS        int64
}

// Bus delivers events to subscribers. The zero value is ready to use and a
// nil *Bus drops events, which keeps tests and optional wiring simple.
type Bus struct {
	mu   sync.RWMutex
	subs []func(Event)
}

// Subscribe registers fn for every published event. fn must be quick; slow
// work belongs in the subscriber's own queue.
func (b *Bus) Subscribe(fn func(Event)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.subs = append(b.subs, fn)
	b.mu.Unlock()
}

// Publish stamps e and hands it to every subscriber.
func (b *Bus) Publish(e Event) {
	if b == nil {
		return
	}
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	if e.Level == "" {
		e.Level = Info
	}
	b.mu.RLock()
	subs := make([]func(Event), len(b.subs))
	copy(subs, b.subs)
	b.mu.RUnlock()
	for _, fn := range subs {
		fn(e)
	}
}
