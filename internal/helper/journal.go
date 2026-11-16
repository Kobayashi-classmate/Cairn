// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// The system journal: journalctl with filters built from validated
// fields, one argument each.

// RegisterLogs adds the journal actions.
func RegisterLogs(r *Registry, root string) {
	Register(r, helperapi.ActionLogJournal, func(ctx context.Context, _ helperapi.Caller, req helperapi.JournalRequest) (helperapi.Journal, error) {
		return readJournal(ctx, root, req)
	})
	Register(r, helperapi.ActionLogUnits, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.Units, error) {
		return listUnits(ctx), nil
	})
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

func readJournal(ctx context.Context, root string, req helperapi.JournalRequest) (helperapi.Journal, error) {
	out := helperapi.Journal{Entries: []helperapi.JournalEntry{}}
	if !hasJournal(root) {
		return out, fileErr(helperapi.CodeLogNoJournal, nil)
	}
	limit := req.Limit
	if limit == 0 {
		limit = 200
	}
	args := []string{"--no-pager", "--output=json", "--lines=" + strconv.Itoa(limit)}
	if req.Unit != "" {
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
	return out, nil
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
