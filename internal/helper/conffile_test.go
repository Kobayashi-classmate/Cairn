// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// serviceUID owns the service directories in these tests (nobody).
const serviceUID = 65534

// confTarget is a root-only file outside the service directory that the
// service account would like the helper to read or replace.
type confTarget struct {
	path, content string
	mode          os.FileMode
}

func newConfTarget(t *testing.T) confTarget {
	t.Helper()
	v := confTarget{path: filepath.Join(t.TempDir(), "shadow"), content: "root:$6$secret:20000::::::\n", mode: 0o600}
	if err := os.WriteFile(v.path, []byte(v.content), v.mode); err != nil {
		t.Fatal(err)
	}
	return v
}

// untouched fails the test when the target's content or mode changed.
func (v confTarget) untouched(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(v.path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(v.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != v.content || info.Mode().Perm() != v.mode {
		t.Fatalf("the file outside the directory changed: mode=%v content=%q", info.Mode().Perm(), data)
	}
}

// serviceDir makes root/etc/svc owned by the service account.
func serviceDir(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "etc/svc")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(dir, serviceUID, serviceUID); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func wantRefused(t *testing.T, what string, err error, reason string) {
	t.Helper()
	ue, ok := asConfUnsafe(err)
	if !ok || ue.Reason != reason {
		t.Fatalf("%s: want a refusal (%s), got %v", what, reason, err)
	}
}

func TestConfFileRefusesWhatIsNotAPlainFile(t *testing.T) {
	cases := []struct {
		name, reason string
		plant        func(t *testing.T, dir string, v confTarget)
	}{
		{"link to a file elsewhere", confLink, func(t *testing.T, dir string, v confTarget) {
			if err := os.Symlink(v.path, filepath.Join(dir, "svc.conf")); err != nil {
				t.Fatal(err)
			}
		}},
		{"pipe", confSpecial, func(t *testing.T, dir string, _ confTarget) {
			if err := syscall.Mkfifo(filepath.Join(dir, "svc.conf"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"second name of a file elsewhere", confHardLinks, func(t *testing.T, dir string, v confTarget) {
			if err := os.Link(v.path, filepath.Join(dir, "svc.conf")); err != nil {
				t.Skipf("hard link across directories: %v", err)
			}
		}},
		{"directory", confSpecial, func(t *testing.T, dir string, _ confTarget) {
			if err := os.Mkdir(filepath.Join(dir, "svc.conf"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			dir := serviceDir(t, root)
			// The hard link needs both names on one file system.
			v := confTarget{path: filepath.Join(root, "shadow"), content: "root:$6$secret:20000::::::\n", mode: 0o600}
			if err := os.WriteFile(v.path, []byte(v.content), v.mode); err != nil {
				t.Fatal(err)
			}
			c.plant(t, dir, v)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _, err := readConfFile(root, "/etc/svc/svc.conf", 1<<20)
				wantRefused(t, "read", err, c.reason)
				err = writeConfFile(root, "/etc/svc/svc.conf", []byte("new\n"), confWrite{Create: true, Mode: 0o644})
				wantRefused(t, "write", err, c.reason)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("blocked on the file")
			}
			v.untouched(t)
			// Nothing was left behind in the directory.
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".cairn-") {
					t.Fatalf("temporary file left: %s", e.Name())
				}
			}
		})
	}
}

func TestConfFileRefusesLinkedDirectory(t *testing.T) {
	root := t.TempDir()
	v := newConfTarget(t)
	// The service account owns /etc/svc and swaps conf.d for a link to a
	// directory of root's.
	dir := serviceDir(t, root)
	if err := os.Symlink(filepath.Dir(v.path), filepath.Join(dir, "conf.d")); err != nil {
		t.Fatal(err)
	}
	_, _, err := readConfFile(root, "/etc/svc/conf.d/shadow", 1<<20)
	wantRefused(t, "read", err, confLink)
	err = writeConfFile(root, "/etc/svc/conf.d/shadow", []byte("new\n"), confWrite{Create: true, Mode: 0o644})
	wantRefused(t, "write", err, confLink)
	err = writeConfFile(root, "/etc/svc/conf.d/99-cairn.conf", []byte("new\n"), confWrite{Create: true, Mode: 0o644, MkdirAll: true})
	wantRefused(t, "create", err, confLink)
	_, err = openConfDir(root, "/etc/svc/conf.d", true)
	wantRefused(t, "open directory", err, confLink)
	if err := removeConfFile(root, "/etc/svc/conf.d/shadow"); err == nil {
		t.Fatal("remove through a linked directory")
	}
	v.untouched(t)
	if _, err := os.Stat(filepath.Join(filepath.Dir(v.path), "99-cairn.conf")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a file appeared in the linked directory: %v", err)
	}
	// A link of root's higher up, where only root can write, is another
	// matter (TestConfDirFollowsOnlyRootsLinks); the service's own link
	// below it is refused all the same.
	if err := os.Rename(filepath.Join(root, "etc"), filepath.Join(root, "etc.real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("etc.real", filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	_, _, err = readConfFile(root, "/etc/svc/conf.d/shadow", 1<<20)
	wantRefused(t, "read through /etc", err, confLink)
	// Something other than a directory on the way.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "etc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = readConfFile(other, "/etc/svc.conf", 1<<20)
	wantRefused(t, "file on the way", err, confNotDir)
}

func TestConfFileReplaceKeepsModeAndOwner(t *testing.T) {
	root := t.TempDir()
	dir := serviceDir(t, root)
	conf := filepath.Join(dir, "svc.conf")
	writeTestFile(t, conf, "port 1\n") // 0640
	uid := os.Geteuid()
	if uid == 0 {
		if err := os.Chown(conf, serviceUID, serviceUID); err != nil {
			t.Fatal(err)
		}
		uid = serviceUID
	}
	data, info, err := readConfFile(root, "/etc/svc/svc.conf", 1<<20)
	if err != nil || string(data) != "port 1\n" || info.Mode().Perm() != 0o640 {
		t.Fatalf("read: %q %v %v", data, info, err)
	}
	if _, _, err := readConfFile(root, "/etc/svc/svc.conf", 3); !errors.Is(err, errConfTooLarge) {
		t.Fatalf("limit: %v", err)
	}
	if err := writeConfFile(root, "/etc/svc/svc.conf", []byte("port 2\n"), confWrite{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(conf)
	fi, _ := os.Lstat(conf)
	st := fi.Sys().(*syscall.Stat_t)
	if string(got) != "port 2\n" || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o640 || int(st.Uid) != uid || st.Nlink != 1 ||
		(os.Geteuid() == 0 && int(st.Gid) != serviceUID) {
		t.Fatalf("after the save: %q mode=%v uid=%d gid=%d links=%d", got, fi.Mode(), st.Uid, st.Gid, st.Nlink)
	}
	// Without Create a missing file is an error, with it the new file is
	// root's and has the mode asked for; missing directories are made
	// only when asked.
	if err := writeConfFile(root, "/etc/svc/new.conf", []byte("x\n"), confWrite{}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing without create: %v", err)
	}
	if err := writeConfFile(root, "/etc/svc/sub/new.conf", []byte("x\n"), confWrite{Create: true, Mode: 0o644}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	if err := writeConfFile(root, "/etc/svc/sub/new.conf", []byte("x\n"), confWrite{Create: true, Mode: 0o644, MkdirAll: true}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "sub/new.conf")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("new file: %v %v", fi, err)
	}
	if err := removeConfFile(root, "/etc/svc/sub/new.conf"); err != nil {
		t.Fatal(err)
	}
	if err := removeConfFile(root, "/etc/svc/sub/new.conf"); err != nil {
		t.Fatalf("removing a missing file: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".cairn-") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

// The temporary file in the service-owned directory is swapped for a
// link while it is being written: nothing done to it afterwards (mode,
// owner, label) reaches the file the link points to.
func TestConfFileReplaceIgnoresSwappedTemporaryFile(t *testing.T) {
	root := t.TempDir()
	dir := serviceDir(t, root)
	conf := filepath.Join(dir, "redis.conf")
	v := newConfTarget(t)
	var stop atomic.Bool
	var swapped atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Stands in for an inotify watcher run by the directory owner.
		for !stop.Load() {
			ents, _ := os.ReadDir(dir)
			for _, e := range ents {
				if strings.HasPrefix(e.Name(), ".cairn-") && e.Type().IsRegular() {
					p := filepath.Join(dir, e.Name())
					if os.Symlink(v.path, p+".l") == nil && os.Rename(p+".l", p) == nil {
						swapped.Add(1)
					}
				}
			}
		}
	}()
	big := []byte(strings.Repeat("# padding line to widen the write window\n", 50000))
	deadline := time.Now().Add(3 * time.Second)
	tries := 0
	for time.Now().Before(deadline) {
		tries++
		_ = os.Remove(conf)
		if err := os.WriteFile(conf, []byte("port 6379\n"), 0o666); err != nil { //nolint:gosec // G306: the service account widened its file
			t.Fatal(err)
		}
		_ = os.Chmod(conf, 0o666) //nolint:gosec // G302: past the umask
		_ = writeConfFile(root, "/etc/svc/redis.conf", big, confWrite{})
	}
	stop.Store(true)
	<-done
	t.Logf("tries=%d swaps=%d", tries, swapped.Load())
	v.untouched(t)
}

// An administrator may have moved a configuration or data directory and
// left a link. Such a link is followed only where nobody but root can have
// put it: root's link in a directory of root's that no one else may write
// to. A link in a directory of the service's account, or in one others may
// write to, is refused whoever owns it.
func TestConfDirFollowsOnlyRootsLinks(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: the links and directories must be root's")
	}
	root := t.TempDir()
	mk := func(dir string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, dir), mode); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(root, at)); err != nil {
			t.Fatal(err)
		}
	}
	mk("data/redis", 0o750)
	mk("var/lib", 0o755)
	mk("etc", 0o755)
	if err := os.WriteFile(filepath.Join(root, "data/redis/dump.rdb"), []byte("REDIS"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Moved to another disk by the administrator: a relative link, an
	// absolute one, and a link to a link.
	link("../../data/redis", "var/lib/redis")
	link("/data/redis", "etc/redis")
	link("redis", "etc/redis-alias")
	for _, p := range []string{"/var/lib/redis/dump.rdb", "/etc/redis/dump.rdb", "/etc/redis-alias/dump.rdb"} {
		data, _, err := readConfFile(root, p, 1<<20)
		if err != nil || string(data) != "REDIS" {
			t.Errorf("%s through root's link: %q %v", p, data, err)
		}
	}
	if err := writeConfFile(root, "/var/lib/redis/new.conf", []byte("x\n"), confWrite{Create: true, Mode: 0o640}); err != nil {
		t.Fatalf("write through root's link: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "data/redis/new.conf")); err != nil || string(data) != "x\n" {
		t.Fatalf("the file written through root's link: %q %v", data, err)
	}

	// The same link, root's own, in a directory others may write to.
	mk("shared", 0o777)
	link("../data/redis", "shared/redis")
	_, _, err := readConfFile(root, "/shared/redis/dump.rdb", 1<<20)
	wantRefused(t, "a link where others may write", err, confLink)
	// A link that is not root's, in root's directory.
	link("../data/redis", "etc/theirs")
	if err := os.Lchown(filepath.Join(root, "etc/theirs"), serviceUID, serviceUID); err != nil {
		t.Fatal(err)
	}
	_, _, err = readConfFile(root, "/etc/theirs/dump.rdb", 1<<20)
	wantRefused(t, "a link that is not root's", err, confLink)
	// Root's link inside the service's own directory.
	if err := os.Chown(filepath.Join(root, "data/redis"), serviceUID, serviceUID); err != nil {
		t.Fatal(err)
	}
	mk("data/other", 0o700)
	if err := os.WriteFile(filepath.Join(root, "data/other/secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	link("../other", "data/redis/sub")
	_, _, err = readConfFile(root, "/var/lib/redis/sub/secret", 1<<20)
	wantRefused(t, "a link in the service's directory", err, confLink)
	// Links that lead around in a circle end in a refusal, not a hang.
	link("loop-b", "etc/loop-a")
	link("loop-a", "etc/loop-b")
	_, _, err = readConfFile(root, "/etc/loop-a/x", 1<<20)
	wantRefused(t, "a circle of links", err, confLink)
}

// Making missing directories never makes what a link points to.
func TestConfDirDoesNotMakeALinksTarget(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root: the link and directories must be root's")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/app"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/gone/away", filepath.Join(root, "etc/app/conf.d")); err != nil {
		t.Fatal(err)
	}
	if f, err := openConfDir(root, "/etc/app/conf.d", true); err == nil {
		_ = f.Close()
		t.Fatal("a dangling link's target was made")
	}
	if _, err := os.Lstat(filepath.Join(root, "gone")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the link's target: %v", err)
	}
	// Below a link whose target is there, missing directories are made.
	if err := os.MkdirAll(filepath.Join(root, "data/app"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../data/app", filepath.Join(root, "etc/app/moved")); err != nil {
		t.Fatal(err)
	}
	f, err := openConfDir(root, "/etc/app/moved/conf.d", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if fi, err := os.Stat(filepath.Join(root, "data/app/conf.d")); err != nil || !fi.IsDir() {
		t.Fatalf("the directory below the link: %v", err)
	}
}
