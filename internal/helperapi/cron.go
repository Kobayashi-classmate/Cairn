// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"net/url"
	"regexp"
	"strings"
)

// Scheduled tasks: each is a systemd service and timer of its own
// (cairn-cron-<id>). A shell task runs a script as a site owner or an
// ordinary user (root only when root access is switched on); a URL task
// fetches a URL; a backup task runs a backup plan.
const (
	ActionCronApply    = "cron.apply"    // operation
	ActionCronRemove   = "cron.remove"   // operation
	ActionCronRun      = "cron.run"      // operation: start it now
	ActionCronStatus   = "cron.status"   // synchronous
	ActionCronRuns     = "cron.runs"     // synchronous
	ActionCronLog      = "cron.log"      // synchronous
	ActionCronSchedule = "cron.schedule" // synchronous: check an OnCalendar expression
)

// Task types.
const (
	CronShell  = "shell"
	CronURL    = "url"
	CronBackup = "backup"
)

// Cron error codes.
const (
	CodeCronSystemd      = "cron.systemd_required"
	CodeCronBadSchedule  = "cron.bad_schedule"
	CodeCronNotFound     = "cron.not_found"
	CodeCronPlanNotFound = "cron.plan_not_found"
)

// Limits.
const (
	CronScriptMax  = 64 << 10
	CronTimeoutMax = 24 * 60
)

var (
	cronID       = regexp.MustCompile(`^t[0-9a-f]{15}$`)
	planID       = regexp.MustCompile(`^p[0-9a-f]{15}$`)
	invocationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidCronID reports whether s names a task.
func ValidCronID(s string) bool { return cronID.MatchString(s) }

// ValidPlanID reports whether s names a backup plan.
func ValidPlanID(s string) bool { return planID.MatchString(s) }

// ValidSchedule reports whether s can be an OnCalendar expression; the
// helper checks it with systemd-analyze as well.
func ValidSchedule(s string) bool {
	// % starts a specifier in unit files; no expression starts with "-",
	// which would read as an option of systemd-analyze.
	return s != "" && len(s) <= 200 && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, "\x00\n\r\"'\\;$`%")
}

// CronSpec is a task as the panel wants it.
type CronSpec struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Schedule string `json:"schedule"` // OnCalendar
	// User runs a shell task: a site owner, an ordinary user or root.
	User   string `json:"user,omitempty"`
	Script string `json:"script,omitempty"`
	URL    string `json:"url,omitempty"`
	Plan   string `json:"plan,omitempty"`
	// TimeoutMin stops a run that takes longer (0: one hour).
	TimeoutMin int  `json:"timeoutMin,omitempty"`
	Enabled    bool `json:"enabled"`
}

// Validate implements the helper's request validation hook.
func (s CronSpec) Validate() error {
	if !ValidCronID(s.ID) {
		return invalid("id", "cron_id")
	}
	if !ValidSchedule(s.Schedule) {
		return invalid("schedule", "schedule")
	}
	if s.TimeoutMin < 0 || s.TimeoutMin > CronTimeoutMax {
		return invalid("timeoutMin", "range")
	}
	switch s.Type {
	case CronShell:
		if !termUser.MatchString(s.User) {
			return invalid("user", "posix_name")
		}
		if strings.TrimSpace(s.Script) == "" || len(s.Script) > CronScriptMax || strings.ContainsRune(s.Script, 0) {
			return invalid("script", "script")
		}
	case CronURL:
		u, err := url.Parse(s.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(s.URL) > 2048 ||
			strings.ContainsAny(s.URL, "\x00\n\r") {
			return invalid("url", "http_url")
		}
	case CronBackup:
		if !ValidPlanID(s.Plan) {
			return invalid("plan", "plan_id")
		}
	default:
		return invalid("type", "oneof")
	}
	return nil
}

// CronRef names a task.
type CronRef struct {
	ID string `json:"id"`
}

// Validate implements the helper's request validation hook.
func (r CronRef) Validate() error {
	if !ValidCronID(r.ID) {
		return invalid("id", "cron_id")
	}
	return nil
}

// CronState is what systemd says about a task.
type CronState struct {
	Spec CronSpec `json:"spec"`
	// NextRun and LastRun are Unix seconds (0: none).
	NextRun int64  `json:"nextRun,omitempty"`
	LastRun int64  `json:"lastRun,omitempty"`
	Running bool   `json:"running"`
	Result  string `json:"result,omitempty"` // of the last run: success | failed | timeout
}

// CronStatus is the result of cron.status.
type CronStatus struct {
	Systemd bool                 `json:"systemd"`
	Tasks   map[string]CronState `json:"tasks"`
}

// CronRun is one run of a task.
type CronRun struct {
	Invocation string `json:"invocation,omitempty"`
	Started    int64  `json:"started"`
	Finished   int64  `json:"finished,omitempty"`
	Result     string `json:"result"` // running | success | failed | timeout
	ExitCode   int    `json:"exitCode,omitempty"`
	// Manual: started from the panel, not by the timer.
	Manual bool `json:"manual,omitempty"`
}

// CronRuns is the result of cron.runs, newest first.
type CronRuns struct {
	Runs []CronRun `json:"runs"`
}

// CronLogRequest reads a run's output.
type CronLogRequest struct {
	ID         string `json:"id"`
	Invocation string `json:"invocation"`
}

// Validate implements the helper's request validation hook.
func (r CronLogRequest) Validate() error {
	if !ValidCronID(r.ID) {
		return invalid("id", "cron_id")
	}
	if !invocationID.MatchString(r.Invocation) {
		return invalid("invocation", "invocation_id")
	}
	return nil
}

// CronLog is a run's output, as journald has it.
type CronLog struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
}

// ScheduleRequest checks an OnCalendar expression.
type ScheduleRequest struct {
	Schedule string `json:"schedule"`
}

// Validate implements the helper's request validation hook.
func (r ScheduleRequest) Validate() error {
	if !ValidSchedule(r.Schedule) {
		return invalid("schedule", "schedule")
	}
	return nil
}

// ScheduleInfo is systemd's reading of a schedule.
type ScheduleInfo struct {
	Normalized string  `json:"normalized"`
	Next       []int64 `json:"next"` // the next few runs, Unix seconds
}
