// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeJournal makes root look like it has journalctl and answers journalctl
// with filtered for a request carrying filters and tail for the unfiltered
// one-line query. It records every argument list.
func fakeJournal(t *testing.T, filtered, tail string) (string, *[][]string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr/bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "usr/bin/journalctl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	old := queryCommand
	t.Cleanup(func() { queryCommand = old })
	queryCommand = func(_ context.Context, name string, args ...string) (string, error) {
		if name != "journalctl" {
			t.Fatalf("unexpected command %s", name)
		}
		calls = append(calls, args)
		if slices.Equal(args, []string{"--no-pager", "--output=json", "--lines=1"}) {
			return tail, nil
		}
		return filtered, nil
	}
	return root, &calls
}

const tailEntry = `{"__CURSOR":"s=abc;i=42;b=def;m=1;t=2;x=3","__REALTIME_TIMESTAMP":"1700000000000000","MESSAGE":"newest","PRIORITY":"6"}`

func TestReadJournalEmptyReturnsTailCursor(t *testing.T) {
	root, calls := fakeJournal(t, "", tailEntry+"\n")
	out, err := readJournal(context.Background(), root, helperapi.JournalRequest{Unit: "nginx.service", Grep: "nothing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 0 {
		t.Fatalf("entries = %v, want none", out.Entries)
	}
	if out.Cursor != "s=abc;i=42;b=def;m=1;t=2;x=3" {
		t.Fatalf("cursor = %q, want the journal's tail", out.Cursor)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %v, want the query and the tail lookup", *calls)
	}
}

func TestReadJournalKeepsCursors(t *testing.T) {
	entry := `{"__CURSOR":"s=abc;i=7","__REALTIME_TIMESTAMP":"1700000000000000","MESSAGE":"hello","PRIORITY":"3"}`
	root, calls := fakeJournal(t, entry+"\n", tailEntry)
	out, err := readJournal(context.Background(), root, helperapi.JournalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Cursor != "s=abc;i=7" {
		t.Fatalf("out = %+v, want the entry's own cursor", out)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v, want no tail lookup", *calls)
	}
	// Long messages come in full: without --all journalctl gives null.
	if !slices.Contains((*calls)[0], "--all") {
		t.Fatalf("query %v lacks --all", (*calls)[0])
	}

	// Following with nothing new keeps the cursor it was given.
	root, calls = fakeJournal(t, "", tailEntry)
	out, err = readJournal(context.Background(), root, helperapi.JournalRequest{After: "s=abc;i=7"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Cursor != "s=abc;i=7" || len(*calls) != 1 {
		t.Fatalf("cursor = %q after %v, want the request's", out.Cursor, *calls)
	}
	if !slices.ContainsFunc((*calls)[0], func(a string) bool { return strings.HasPrefix(a, "--after-cursor=") }) {
		t.Fatalf("args = %v, want --after-cursor", (*calls)[0])
	}
}

func TestReadJournalEmptyJournal(t *testing.T) {
	// No entries at all, or a cursor the API would refuse: no cursor.
	for _, tail := range []string{"", `{"__CURSOR":"bad cursor!"}`} {
		root, _ := fakeJournal(t, "", tail)
		out, err := readJournal(context.Background(), root, helperapi.JournalRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if out.Cursor != "" {
			t.Fatalf("tail %q: cursor = %q, want none", tail, out.Cursor)
		}
	}
}
