// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// tarEntry is a header and its body.
type tarEntry struct {
	h    *tar.Header
	body string
}

// tarOf builds a tar archive.
func tarOf(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, en := range entries {
		h, body := en.h, en.body
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(tw, body)
	}
	_ = tw.Close()
	return buf.String()
}

func TestArchiveLinkChainsAndHardLinks(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/blog/app/secret", "blog secret")
	e.put("/srv/www/shop/app/existing.txt", "was there")
	// A link the site's owner made to another site.
	if err := os.Symlink("../../blog/app", filepath.Join(e.f.Root, "/srv/www/shop/app/peek")); err != nil {
		t.Fatal(err)
	}
	e.put("/srv/www/shop/app/evil.tar", tarOf(t,
		tarEntry{&tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg}, "fine"},
		// x resolves to the destination itself; y would climb above it
		// through x, which no lexical check sees.
		tarEntry{&tar.Header{Name: "x", Typeflag: tar.TypeSymlink, Linkname: "."}, ""},
		tarEntry{&tar.Header{Name: "y", Typeflag: tar.TypeSymlink, Linkname: "x/.."}, ""},
		tarEntry{&tar.Header{Name: "sub/z", Typeflag: tar.TypeSymlink, Linkname: "../ok.txt"}, ""},
		// Hard links: only to files this archive wrote.
		tarEntry{&tar.Header{Name: "h-ok", Typeflag: tar.TypeLink, Linkname: "ok.txt"}, ""},
		tarEntry{&tar.Header{Name: "h-existing", Typeflag: tar.TypeLink, Linkname: "existing.txt"}, ""},
		tarEntry{&tar.Header{Name: "h-peek", Typeflag: tar.TypeLink, Linkname: "peek/secret"}, ""},
	))
	op, res, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/evil.tar", Dest: "shop/app"})
	if op.State != helperapi.OpSucceeded || res.Done != 4 {
		t.Fatalf("extract: %+v %+v\n%s", op, res, log)
	}
	if e.exists("/srv/www/shop/app/y") {
		t.Error("a link climbing through another link was made")
	}
	if target, _ := os.Readlink(filepath.Join(e.f.Root, "/srv/www/shop/app/sub/z")); target != "../ok.txt" {
		t.Errorf("a leading .. inside the destination is fine: %q", target)
	}
	if e.read("/srv/www/shop/app/h-ok") != "fine" {
		t.Error("a hard link to a file of the archive becomes a copy")
	}
	for _, p := range []string{"/srv/www/shop/app/h-existing", "/srv/www/shop/app/h-peek"} {
		if e.exists(p) {
			t.Errorf("%s was copied from a file the archive did not write", p)
		}
	}
}

func TestExtractKeepsExistingDirModes(t *testing.T) {
	e := newFilesEnv(t)
	dir := filepath.Join(e.f.Root, "/srv/www/shop/app/conf")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: the mode under test
		t.Fatal(err)
	}
	e.put("/srv/www/shop/app/d.tar", tarOf(t,
		tarEntry{&tar.Header{Name: "conf/", Typeflag: tar.TypeDir, Mode: 0o777}, ""},
		tarEntry{&tar.Header{Name: "fresh/", Typeflag: tar.TypeDir, Mode: 0o711}, ""},
	))
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/d.tar", Dest: "shop/app"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("extract: %+v\n%s", op, log)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o750 {
		t.Errorf("an existing directory's mode changed without overwrite: %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/fresh")); fi.Mode().Perm() != 0o711 {
		t.Errorf("a new directory takes the archive's mode: %v", fi.Mode())
	}
	if op, _, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/d.tar", Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpSucceeded {
		t.Fatalf("extract with overwrite: %+v", op)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o777 {
		t.Errorf("overwriting takes the archive's mode: %v", fi.Mode())
	}
}

func TestCompressLinkStartAndModes(t *testing.T) {
	e := newFilesEnv(t)
	e.put("/srv/www/blog/app/secret", "blog secret")
	e.put("/srv/www/shop/app/pub/index.html", "hi")
	if err := os.Chmod(filepath.Join(e.f.Root, "/srv/www/shop/app/pub/index.html"), 0o644); err != nil { //nolint:gosec // G302: a world-readable file
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(e.f.Root, "/srv/www/shop/app/pub"), 0o755); err != nil { //nolint:gosec // G302: a world-readable directory
		t.Fatal(err)
	}
	if err := os.Symlink("../../blog/app", filepath.Join(e.f.Root, "/srv/www/shop/app/peek")); err != nil {
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"peek"}, Format: "tar.gz", Name: "peek.tar.gz"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("compress: %+v\n%s", op, log)
	}
	data, _ := os.ReadFile(filepath.Join(e.f.Root, "/srv/www/shop/app/peek.tar.gz"))
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name+":"+string(h.Typeflag))
	}
	if len(names) != 1 || names[0] != "peek:"+string(tar.TypeSymlink) {
		t.Fatalf("a link named for an archive is archived as the link: %v", names)
	}
	if op, _, _ := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"pub"}, Format: "zip", Name: "pub.zip"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("compress: %+v", op)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/pub.zip")); fi.Mode().Perm() != 0o640 {
		t.Errorf("an archive is not world-readable: %v", fi.Mode())
	}
	e.put("/srv/www/shop/app/pub/private.key", "k") // 0600
	if op, _, _ := e.run(helperapi.ActionFileCompress, helperapi.FileCompressRequest{Root: "sites", Dir: "shop/app", Names: []string{"pub"}, Format: "tar.zst", Name: "pub.tar.zst"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("compress: %+v", op)
	}
	if fi, _ := os.Stat(filepath.Join(e.f.Root, "/srv/www/shop/app/pub.tar.zst")); fi.Mode().Perm() != 0o600 {
		t.Errorf("an archive of something private is its owner's only: %v", fi.Mode())
	}
}

func TestCopyN(t *testing.T) {
	var buf bytes.Buffer
	if err := copyN(&buf, strings.NewReader("abcdef"), 4); err != nil || buf.String() != "abcd" {
		t.Fatalf("a grown file is cut: %q %v", buf.String(), err)
	}
	buf.Reset()
	if err := copyN(&buf, strings.NewReader("ab"), 4); err != nil || buf.String() != "ab\x00\x00" {
		t.Fatalf("a shrunk file is padded: %q %v", buf.String(), err)
	}
}

// unarEnv fakes systemd and the unpacking account for unar's unit; run
// answers systemd-run with the unar command line that follows "--".
func unarEnv(t *testing.T, run func(ctx context.Context, args []string, unit []string) error) *filesEnv {
	t.Helper()
	e := newFilesEnv(t)
	e.put(unarBinary, "")
	oldRun, oldEvery, oldSystemd, oldUser := dbRun, unarSpaceCheck, systemdRunning, lookupUser
	t.Cleanup(func() { dbRun, unarSpaceCheck, systemdRunning, lookupUser = oldRun, oldEvery, oldSystemd, oldUser })
	unarSpaceCheck = 5 * time.Millisecond
	systemdRunning = func() bool { return true }
	lookupUser = func(name string) (int, int, error) {
		if name == unpackUser {
			return 64000, 64000, nil
		}
		return oldUser(name)
	}
	dbRun = func(ctx context.Context, c execx.Cmd) (execx.Result, error) {
		if c.Name == "systemctl" {
			return execx.Result{}, nil
		}
		if c.Name != "systemd-run" {
			return execx.Result{}, errors.New("unexpected " + c.Name)
		}
		for i, a := range c.Args {
			if a == "--" {
				return execx.Result{}, run(ctx, c.Args[i+1:], c.Args[:i])
			}
		}
		return execx.Result{}, errors.New("no command")
	}
	return e
}

func TestUnarInItsOwnUnitAndBudget(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changes owners")
	}
	var avail atomic.Int64
	avail.Store(1 << 30)
	oldSpace := spaceOf
	t.Cleanup(func() { spaceOf = oldSpace })
	spaceOf = func(*os.File) int64 { return avail.Load() }
	var cmd, unit []string
	fill := false
	e := unarEnv(t, func(ctx context.Context, args, props []string) error {
		cmd, unit = args, props
		out, archive := args[len(args)-2], args[len(args)-1]
		// unar reads its own copy, in its scratch directory.
		if data, err := os.ReadFile(archive); err != nil || string(data) != "Rar!" {
			return fmt.Errorf("archive copy %s: %q %v", archive, data, err)
		}
		if fi, err := os.Stat(out); err != nil || fi.Sys().(*syscall.Stat_t).Uid != 64000 {
			return fmt.Errorf("output directory %s: %v", out, err)
		}
		if err := os.MkdirAll(filepath.Join(out, "inner"), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, "inner/a.txt"), []byte("unpacked"), 0o600); err != nil {
			return err
		}
		if fill {
			// Writing on until stopped.
			avail.Store(100)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		return nil
	})
	// The archive's directory is closed to everyone but root: unar never
	// needs to reach it, nor the destination.
	e.put("/srv/www/shop/app/locked/a.rar", "Rar!")
	if err := os.Chmod(filepath.Join(e.f.Root, "/srv/www/shop/app/locked"), 0o700); err != nil { //nolint:gosec // G302: a directory closed to all but root
		t.Fatal(err)
	}
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/locked/a.rar", Dest: "shop/app"}); op.State != helperapi.OpSucceeded {
		t.Fatalf("extract: %+v\n%s", op, log)
	}
	if cmd[0] != unarBinary || !slices.Contains(unit, "--property=User="+unpackUser) ||
		!slices.Contains(unit, "--property=TemporaryFileSystem="+filepath.Join(e.f.Root, unpackDir)) ||
		!strings.HasPrefix(cmd[len(cmd)-1], filepath.Join(e.f.Root, unpackDir)+"/") {
		t.Fatalf("unar's unit: %v -- %v", unit, cmd)
	}
	if e.read("/srv/www/shop/app/inner/a.txt") != "unpacked" {
		t.Fatal("unpacked content")
	}
	if entries, _ := os.ReadDir(filepath.Join(e.f.Root, unpackDir)); len(entries) != 0 {
		t.Fatalf("scratch left: %v", entries)
	}
	fill = true
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/locked/a.rar", Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileNoSpace {
		t.Fatalf("unar filling the disk is stopped: %+v\n%s", op, log)
	}
	// No systemd, no account: a clear refusal, not a bad archive.
	fill = false
	systemdRunning = func() bool { return false }
	if op, _, log := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/locked/a.rar", Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpFailed ||
		op.Error.Code != helperapi.CodeFileSystemd || !strings.Contains(log, "needs systemd") {
		t.Fatalf("without systemd: %+v\n%s", op.Error, log)
	}
	systemdRunning = func() bool { return true }
	lookupUser = func(string) (int, int, error) { return 0, 0, os.ErrNotExist }
	if op, _, _ := e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/locked/a.rar", Dest: "shop/app", Overwrite: true}); op.State != helperapi.OpFailed ||
		op.Error.Code != helperapi.CodeFileUnpackFailed || op.Error.Params["reason"] != "account" {
		t.Fatalf("without the account: %+v", op.Error)
	}
}

func TestUnarSpaceWatchDoesNotHangOnAPipe(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changes owners")
	}
	var dest string
	e := unarEnv(t, func(context.Context, []string, []string) error {
		// The destination's owner puts a pipe in its place while unar
		// runs.
		_ = os.Rename(dest, dest+".old")
		_ = syscall.Mkfifo(dest, 0o644)
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	dest = filepath.Join(e.f.Root, "/srv/www/shop/app/dst")
	e.put("/srv/www/shop/app/a.rar", "Rar!")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _, _ = e.run(helperapi.ActionFileExtract, helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/a.rar", Dest: "shop/app/dst"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if f, err := os.OpenFile(dest, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("the extraction hangs on a pipe at the destination")
	}
}

func TestUnarFailureInTheJobLog(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changes owners")
	}
	e := unarEnv(t, func(context.Context, []string, []string) error { return nil })
	e.put("/srv/www/shop/app/a.rar", "Rar!")
	req := helperapi.FileExtractRequest{Root: "sites", Path: "shop/app/a.rar", Dest: "shop/app", Overwrite: true}
	for _, c := range []struct {
		name    string
		stderr  string
		err     error
		wantLog string
	}{
		// The unit's output reaches the log as it is written (execx's
		// Output); the log also says how the run ended.
		{"unit failed", "Failed to start transient service unit: denied\n",
			&execx.ExitError{Name: "systemd-run", ExitCode: 1, Stderr: "Failed to start transient service unit: denied"}, "exit status 1"},
		// systemd-run itself could not run: nothing was written, the
		// reason is in the log all the same.
		{"not started", "", errors.New("exec: \"systemd-run\": executable file not found in $PATH"), "executable file not found"},
	} {
		dbRun = func(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
			if cmd.Name != "systemd-run" {
				return execx.Result{}, nil
			}
			if c.stderr != "" && cmd.Output != nil {
				_, _ = io.WriteString(cmd.Output, c.stderr)
			}
			return execx.Result{}, c.err
		}
		op, _, log := e.run(helperapi.ActionFileExtract, req)
		if op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeFileBadArchive || !strings.Contains(log, c.wantLog) ||
			(c.stderr != "" && !strings.Contains(log, strings.TrimSpace(c.stderr))) {
			t.Fatalf("%s: %+v\n%s", c.name, op.Error, log)
		}
	}
	// unar that works afterwards still extracts.
	dbRun = func(_ context.Context, cmd execx.Cmd) (execx.Result, error) {
		if cmd.Name != "systemd-run" {
			return execx.Result{}, nil
		}
		out := cmd.Args[len(cmd.Args)-2] // unar ... -o OUT ARCHIVE
		return execx.Result{}, os.WriteFile(filepath.Join(out, "ok.txt"), []byte("ok"), 0o600)
	}
	if op, _, log := e.run(helperapi.ActionFileExtract, req); op.State != helperapi.OpSucceeded || e.read("/srv/www/shop/app/ok.txt") != "ok" {
		t.Fatalf("extract after the failures: %+v\n%s", op, log)
	}
}

func TestVolumes(t *testing.T) {
	e := newFilesEnv(t)
	for _, n := range []string{"x.part1.rar", "x.part2.rar", "x.part10.rar", "x.partial.rar", "y.part2.rar", "z.rar"} {
		e.put("/srv/www/shop/app/"+n, "v")
	}
	root, err := e.f.openRoot("sites")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	app, err := root.dir("shop/app", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	got := volumes(app, "x.part1.rar")
	slices.Sort(got)
	if strings.Join(got, " ") != "x.part1.rar x.part10.rar x.part2.rar" {
		t.Fatalf("volumes: %v", got)
	}
	if got := volumes(app, "z.rar"); len(got) != 1 || got[0] != "z.rar" {
		t.Fatalf("single: %v", got)
	}
}
