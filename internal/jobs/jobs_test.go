// SPDX-License-Identifier: AGPL-3.0-only

package jobs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/store"
)

func TestFollowLeavesNoWatchBehind(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cairn.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	m := &Manager{DB: db, Dir: filepath.Join(dir, "jobs"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	rec, err := m.Start(ctx, Spec{Kind: "test", Permission: "x.read", ManagePermission: "x.manage"}, func(context.Context, *Job) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		_, live := m.live[rec.ID]
		m.mu.Unlock()
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Following a finished job, or one that does not exist, answers at once
	// and keeps no entry that nothing would ever remove.
	for _, id := range []string{rec.ID, "0123456789abcdef01234567", "not-an-id"} {
		_, _, _ = m.Follow(ctx, id, 0, time.Second)
	}
	m.mu.Lock()
	n := len(m.watch)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d watch entries left", n)
	}
}

func TestCompleteRunes(t *testing.T) {
	han := "\xe4\xb8\xad" // a three-byte character
	for _, c := range []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"ab" + han, 5},
		{"ab" + han[:1], 2},
		{"ab" + han[:2], 2},
		{han[:2], 2},  // nothing before it: move on anyway
		{"ab\xff", 3}, // not UTF-8: left alone
		{"ab\x80", 3}, // a stray continuation byte
	} {
		if got := completeRunes([]byte(c.in)); got != c.want {
			t.Errorf("completeRunes(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestReadLogKeepsCharactersWhole(t *testing.T) {
	m := &Manager{Dir: t.TempDir()}
	id := "0123456789abcdef01234567"
	// A character straddles the chunk size.
	text := strings.Repeat("a", maxLogChunk-1) + "\xe4\xb8\xad" + "tail"
	if err := os.WriteFile(m.logPath(id), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	offset := int64(0)
	for range 4 {
		chunk, err := m.ReadLog(id, offset)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(chunk.Log) {
			t.Fatalf("chunk at %d is not valid UTF-8", offset)
		}
		got.WriteString(chunk.Log)
		offset = chunk.Offset
		if offset >= chunk.LogSize {
			break
		}
	}
	if got.String() != text {
		t.Fatalf("read %d bytes back, want %d", got.Len(), len(text))
	}
}
