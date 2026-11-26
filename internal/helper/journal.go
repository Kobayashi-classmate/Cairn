// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// The system journal: journalctl with filters built from validated
// fields, one argument each.

// Logs reads the system journal. Not all of it is for every reader of
// logs: it also holds what tasks that run as root print, the commands run
// with sudo, who signed in over SSH, and the helper's own record of every
// action. log.journal and log.units cover the units in readable only;
// log.journal_all and log.units_all the whole journal, which the web
// process asks for administrators only.
type Logs struct {
	// Root prefixes system paths ("" means /; tests).
	Root     string
	Software *Software // the installed software's services (nil: none)
	Sites    *Sites    // the app sites' units (nil: none)
	Cron     *Cron     // the tasks' units (nil: none)

	mu   sync.Mutex
	seen []string
	at   time.Time
}

// panelWebUnit is the panel's own service of the units readable lists.
// The helper's (cairn-helper.service) is not one: its journal is a record
// of who did what, which the panel's audit log keeps from operators.
const panelWebUnit = "cairn-web.service"

// readableFor is how long a list of readable units is used again (the
// journal page polls while it follows).
const readableFor = 10 * time.Second

// RegisterLogs adds the journal actions.
func RegisterLogs(r *Registry, l *Logs) {
	Register(r, helperapi.ActionLogJournal, l.journal)
	Register(r, helperapi.ActionLogUnits, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.Units, error) {
		out := helperapi.Units{Units: []string{}}
		if systemdRunning() {
			out.Units = append(out.Units, l.readable(ctx)...)
		}
		return out, nil
	})
	Register(r, helperapi.ActionLogJournalAll, func(ctx context.Context, _ helperapi.Caller, req helperapi.JournalRequest) (helperapi.Journal, error) {
		return readJournal(ctx, l.Root, req, nil)
	})
	Register(r, helperapi.ActionLogUnitsAll, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.Units, error) {
		return listUnits(ctx), nil
	})
}

// journal reads the readable units' entries: of the one the request names,
// which must be one of them, or of them all.
func (l *Logs) journal(ctx context.Context, _ helperapi.Caller, req helperapi.JournalRequest) (helperapi.Journal, error) {
	units := l.readable(ctx)
	if req.Unit != "" {
		if !slices.Contains(units, req.Unit) {
			return helperapi.Journal{Entries: []helperapi.JournalEntry{}}, fileErr(helperapi.CodeLogUnitNotAllowed, map[string]any{"unit": req.Unit})
		}
		units = []string{req.Unit}
	}
	return readJournal(ctx, l.Root, req, units)
}

// readable lists the units whose messages are for every reader of logs:
// the panel's web service, the services of the installed software, the
// app sites' units and the units of the tasks whose output is not kept
// for administrators (see sharedLog), sorted.
func (l *Logs) readable(ctx context.Context) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen != nil && time.Since(l.at) < readableFor {
		return l.seen
	}
	units := []string{panelWebUnit}
	if l.Software != nil {
		if names, err := l.Software.installedServices(ctx); err == nil {
			for _, n := range names {
				if !helperapi.ValidUnitName(n) {
					n += ".service" // the catalog names services without the suffix
				}
				units = append(units, n)
			}
		}
	}
	if l.Sites != nil {
		if states, err := l.Sites.states(); err == nil {
			for code, s := range states {
				if s.Spec.App != nil {
					units = append(units, appUnit(code))
				}
			}
		}
	}
	if l.Cron != nil {
		users := l.Cron.Files.termUsers()
		for _, s := range l.Cron.specs() {
			if sharedLog(s, users) {
				units = append(units, cronUnit(s.ID)+".service")
			}
		}
	}
	units = slices.DeleteFunc(units, func(u string) bool { return !helperapi.ValidUnitName(u) })
	slices.Sort(units)
	l.seen, l.at = slices.Compact(units), time.Now()
	return l.seen
}

// sharedLog reports whether what a task prints is for every reader of its
// runs: anything but a shell task that runs as root or as another user
// than a site's owner (users, as terminals list them; one not listed
// counts as privileged), whose output the panel keeps for administrators.
func sharedLog(s helperapi.CronSpec, users helperapi.TermUsers) bool {
	if s.Type != helperapi.CronShell {
		return true
	}
	for _, u := range users.Users {
		if u.Name == s.User {
			return u.Kind == helperapi.TermUserSite
		}
	}
	return false
}

func hasJournal(root string) bool {
	for _, p := range []string{"/usr/bin/journalctl", "/bin/journalctl"} {
		if _, err := os.Stat(root + p); err == nil { //nolint:gosec // G703: fixed paths under a test prefix.
			return true
		}
	}
	return false
}

// journalField reads a field of journalctl's JSON, which is a string, or
// an array of bytes for binary values.
func journalField(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var nums []int
	if json.Unmarshal(raw, &nums) == nil {
		for _, n := range nums {
			b = append(b, byte(n&0xff)) //nolint:gosec // G115: journalctl prints bytes.
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return ""
}

// readJournal reads the journal for req; units, when not nil, are the
// units it reads (any of them, req.Unit among them), else the whole
// journal or req.Unit's part.
func readJournal(ctx context.Context, root string, req helperapi.JournalRequest, units []string) (helperapi.Journal, error) {
	out := helperapi.Journal{Entries: []helperapi.JournalEntry{}}
	if !hasJournal(root) {
		return out, fileErr(helperapi.CodeLogNoJournal, nil)
	}
	if units != nil && len(units) == 0 {
		out.Cursor = req.After
		return out, nil // none to read, not all of them
	}
	limit := req.Limit
	if limit == 0 {
		limit = 200
	}
	// --all: without it journalctl's JSON gives null for fields longer
	// than 4096 bytes, so long messages would show as empty.
	args := []string{"--no-pager", "--output=json", "--all", "--lines=" + strconv.Itoa(limit)}
	switch {
	case units != nil:
		// journalctl takes each --unit as an alternative. Names are never
		// patterns: ValidUnitName has no glob characters.
		for _, u := range units {
			args = append(args, "--unit="+u)
		}
	case req.Unit != "":
		args = append(args, "--unit="+req.Unit)
	}
	if req.Priority > 0 {
		args = append(args, "--priority="+strconv.Itoa(req.Priority))
	}
	if req.Since != "" {
		args = append(args, "--since="+req.Since)
	}
	if req.Until != "" {
		args = append(args, "--until="+req.Until)
	}
	if req.Grep != "" {
		args = append(args, "--grep="+req.Grep, "--case-sensitive=no")
	}
	if req.Boot {
		args = append(args, "--boot")
	}
	if req.After != "" {
		args = append(args, "--after-cursor="+req.After)
	}
	text, err := queryCommand(ctx, "journalctl", args...)
	if err != nil {
		return out, fileErr(helperapi.CodeOpFailed, map[string]any{"detail": errDetail(err)})
	}
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &fields) != nil {
			continue
		}
		e := helperapi.JournalEntry{Message: journalField(fields["MESSAGE"]), Identifier: journalField(fields["SYSLOG_IDENTIFIER"]),
			Unit: journalField(fields["_SYSTEMD_UNIT"])}
		e.Time, _ = strconv.ParseInt(journalField(fields["__REALTIME_TIMESTAMP"]), 10, 64)
		e.Priority, _ = strconv.Atoi(journalField(fields["PRIORITY"]))
		e.PID, _ = strconv.Atoi(journalField(fields["_PID"]))
		if p := journalField(fields["PRIORITY"]); p == "" {
			e.Priority = 6
		}
		if len(e.Message) > 16<<10 {
			e.Message = e.Message[:16<<10]
		}
		out.Entries = append(out.Entries, e)
		out.Cursor = journalField(fields["__CURSOR"])
	}
	if out.Cursor == "" {
		out.Cursor = req.After
	}
	if out.Cursor == "" {
		// Nothing matched yet: continue from the journal's current end, so
		// following picks up only entries written from now on.
		out.Cursor = journalEndCursor(ctx)
	}
	return out, nil
}

// journalEndCursor is the cursor of the newest entry in the whole journal
// (no filters), or "" when it cannot be read. --after-cursor accepts it
// whatever filters the next request has.
func journalEndCursor(ctx context.Context) string {
	text, err := queryCommand(ctx, "journalctl", "--no-pager", "--output=json", "--lines=1")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		var fields map[string]json.RawMessage
		if line == "" || json.Unmarshal([]byte(line), &fields) != nil {
			continue
		}
		if c := journalField(fields["__CURSOR"]); c != "" && helperapi.ValidCursor(c) {
			return c
		}
	}
	return ""
}

// listUnits lists the service units systemd knows.
func listUnits(ctx context.Context) helperapi.Units {
	out := helperapi.Units{Units: []string{}}
	if !systemdRunning() {
		return out
	}
	text, err := queryCommand(ctx, "systemctl", "list-units", "--type=service", "--all", "--no-legend", "--plain")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		if f := strings.Fields(line); len(f) > 0 && helperapi.ValidUnitName(f[0]) {
			out.Units = append(out.Units, f[0])
		}
	}
	sort.Strings(out.Units)
	return out
}
