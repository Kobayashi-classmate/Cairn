// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"regexp"
	"strings"
)

// The system journal, read through journalctl. log.journal and log.units
// cover the units whose messages are for every reader of logs (the panel,
// the installed software, app sites and the tasks that do not run as root
// or as a login user); log.journal_all and log.units_all the whole journal.
const (
	ActionLogJournal    = "log.journal"     // synchronous
	ActionLogUnits      = "log.units"       // synchronous
	ActionLogJournalAll = "log.journal_all" // synchronous
	ActionLogUnitsAll   = "log.units_all"   // synchronous
)

// Log error codes.
const (
	CodeLogNoJournal = "log.no_journal" // no journalctl here
	// CodeLogUnitNotAllowed: log.journal does not read this unit (params:
	// unit); log.journal_all does.
	CodeLogUnitNotAllowed = "log.unit_not_allowed"
)

// JournalMax is the most entries one request returns.
const JournalMax = 1000

var (
	unitName = regexp.MustCompile(`^[A-Za-z0-9@_.:-]{1,200}\.(service|socket|timer|scope|mount|target|path)$`)
	// journalctl --since/--until: dates, times, "today", "-1h" and so on.
	journalTime = regexp.MustCompile(`^[0-9A-Za-z :+-]{1,40}$`)
	cursor      = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,500}$`)
)

// ValidUnitName reports whether s is a systemd unit name.
func ValidUnitName(s string) bool { return unitName.MatchString(s) }

// ValidCursor reports whether s is a journal cursor a request may pass as
// After.
func ValidCursor(s string) bool { return cursor.MatchString(s) }

// JournalRequest selects journal entries: the last Limit ones matching,
// or with After the ones following that cursor.
type JournalRequest struct {
	Unit     string `json:"unit,omitempty"`
	Priority int    `json:"priority,omitempty"` // 0-7, the most verbose shown; 0 means all
	Since    string `json:"since,omitempty"`
	Until    string `json:"until,omitempty"`
	Grep     string `json:"grep,omitempty"`
	Boot     bool   `json:"boot,omitempty"` // this boot only
	After    string `json:"after,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// Validate implements the helper's request validation hook.
func (r JournalRequest) Validate() error {
	if r.Unit != "" && !ValidUnitName(r.Unit) {
		return invalid("unit", "unit_name")
	}
	if r.Priority < 0 || r.Priority > 7 {
		return invalid("priority", "range")
	}
	for f, v := range map[string]string{"since": r.Since, "until": r.Until} {
		if v != "" && !journalTime.MatchString(v) {
			return invalid(f, "journal_time")
		}
	}
	if len(r.Grep) > 200 || strings.ContainsAny(r.Grep, "\x00\n") {
		return invalid("grep", "max")
	}
	if r.After != "" && !ValidCursor(r.After) {
		return invalid("after", "cursor")
	}
	if r.Limit < 0 || r.Limit > JournalMax {
		return invalid("limit", "range")
	}
	return nil
}

// JournalEntry is one journal entry.
type JournalEntry struct {
	Time       int64  `json:"time"` // microseconds
	Priority   int    `json:"priority"`
	Unit       string `json:"unit,omitempty"`
	Identifier string `json:"identifier,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Message    string `json:"message"`
}

// Journal is the result of log.journal; Cursor continues after the last
// entry.
type Journal struct {
	Entries []JournalEntry `json:"entries"`
	Cursor  string         `json:"cursor,omitempty"`
}

// Units is the result of log.units.
type Units struct {
	Units []string `json:"units"`
}
