// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// flushLog records the flushes and, for each, what the files named in
// watch hold at that moment.
type flushLog struct {
	watch   []string
	entries []string
}

func (l *flushLog) contents() string {
	var parts []string
	for _, p := range l.watch {
		data, _ := os.ReadFile(p) //nolint:gosec // G304: the test's files.
		parts = append(parts, string(data))
	}
	return strings.Join(parts, ",")
}

func recordFlushes(t *testing.T, watch ...string) *flushLog {
	t.Helper()
	l := &flushLog{watch: watch}
	oldFsync, oldSyncFS := fsync, syncFS
	t.Cleanup(func() { fsync, syncFS = oldFsync, oldSyncFS })
	fsync = func(f *os.File) error {
		kind := "file"
		if info, err := f.Stat(); err == nil && info.IsDir() {
			kind = "dir"
		}
		l.entries = append(l.entries, fmt.Sprintf("%s %s [%s]", kind, filepath.Base(f.Name()), l.contents()))
		return oldFsync(f)
	}
	syncFS = func(fd int) error {
		l.entries = append(l.entries, fmt.Sprintf("fs [%s]", l.contents()))
		return oldSyncFS(fd)
	}
	return l
}

// The content reaches the disk before the file takes the name, and the
// directory after: a crash leaves the old file or the new one.
func TestWriteFileAsFlushesBeforeRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	writeTestFile(t, p, "old")
	l := recordFlushes(t, p)
	if err := writeFileAs(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != 2 || !strings.HasPrefix(l.entries[0], "file .cairn-") || !strings.HasSuffix(l.entries[0], "[old]") ||
		l.entries[1] != "dir "+filepath.Base(dir)+" [new]" {
		t.Fatalf("flushes: %q", l.entries)
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}

	// The private configuration of a package manager run is thrown away
	// after it: nothing is flushed for it.
	l.entries = nil
	if err := writeFileAtomic(p, []byte("scratch"), 0o644, false); err != nil || len(l.entries) != 0 {
		t.Fatalf("%v %q", err, l.entries)
	}
}

// writeOwned (the panel's certificate, authorized_keys) and the state of
// operations are flushed the same way.
func TestOwnedFileAndOperationStateAreFlushed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "panel.pem")
	writeTestFile(t, p, "old")
	l := recordFlushes(t, p)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := writeOwned(root, "panel.pem", []byte("new"), 0o600, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != 2 || !strings.HasPrefix(l.entries[0], "file .panel.pem.") || !strings.HasSuffix(l.entries[0], "[old]") ||
		!strings.HasPrefix(l.entries[1], "dir ") || !strings.HasSuffix(l.entries[1], "[new]") {
		t.Fatalf("flushes: %q", l.entries)
	}

	o, _ := newTestOps(t)
	l.entries = nil
	if err := o.save(helperapi.Op{ID: newOpID(), Kind: "test", State: helperapi.OpQueued}); err != nil {
		t.Fatal(err)
	}
	if len(l.entries) != 2 || !strings.HasPrefix(l.entries[0], "file .op-") || !strings.HasPrefix(l.entries[1], "dir ") {
		t.Fatalf("operation state: %q", l.entries)
	}
}

// A batch flushes each file before any takes its name; a large one
// flushes the file system instead, once before the renames and once after.
func TestWriteFilesAsFlushesBeforeTheRenames(t *testing.T) {
	for _, n := range []int{3, batchFlushEach + 1, 200} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			dir := t.TempDir()
			var files []fileWrite
			var paths []string
			for i := range n {
				p := filepath.Join(dir, fmt.Sprintf("site%03d.conf", i))
				writeTestFile(t, p, "o")
				paths = append(paths, p)
				files = append(files, fileWrite{path: p, data: []byte("n"), mode: 0o644})
			}
			l := recordFlushes(t, paths...)
			if err := writeFilesAs(files); err != nil {
				t.Fatal(err)
			}
			old, fresh := strings.TrimSuffix(strings.Repeat("o,", n), ","), strings.TrimSuffix(strings.Repeat("n,", n), ",")
			var want []string
			if n <= batchFlushEach {
				for range n {
					want = append(want, "file ["+old+"]")
				}
				want = append(want, "dir ["+fresh+"]")
			} else {
				want = []string{"fs [" + old + "]", "fs [" + fresh + "]"}
			}
			var got []string
			for _, e := range l.entries {
				kind, rest, _ := strings.Cut(e, " ")
				if kind != "fs" {
					_, rest, _ = strings.Cut(rest, " ")
				}
				got = append(got, kind+" "+rest)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("flushes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			for _, p := range paths {
				if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("%s: %v %v", p, info, err)
				}
			}
			if entries, _ := os.ReadDir(dir); len(entries) != n {
				t.Fatalf("temporary files left: %d entries", len(entries))
			}
		})
	}
}

// A file that cannot be written does not keep the others from theirs; the
// failure is reported.
func TestWriteFilesAsGoesOnPastAFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	writeTestFile(t, blocker, "x")
	good := filepath.Join(dir, "good.conf")
	err := writeFilesAs([]fileWrite{
		{path: filepath.Join(blocker, "under-a-file.conf"), data: []byte("a"), mode: 0o644},
		{path: good, data: []byte("b"), mode: 0o644},
	})
	if err == nil {
		t.Fatal("no error")
	}
	if data, rerr := os.ReadFile(good); rerr != nil || string(data) != "b" { //nolint:gosec // G304: the test's file.
		t.Fatalf("the other file: %q %v", data, rerr)
	}
}
