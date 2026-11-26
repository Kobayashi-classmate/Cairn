// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Files that must survive a crash or a power cut.
//
// A file replaced through a rename holds the old content or the new one
// after a crash only if the new content reached the disk before the
// rename did: ext4 and XFS may commit the rename to their journal ahead of
// the data (ext4 protects a file it replaces, not one it creates; XFS
// neither), and the name then holds an empty file. What the helper writes
// includes /etc/fstab, sshd's configuration, authorized_keys and the
// firewall's rules and state, none of which may come back empty; so the
// content is flushed before the rename, and the directory after it, so
// that the change itself is not lost either.

// fsync flushes an open file, or a directory, to the disk (tests count
// the calls).
var fsync = (*os.File).Sync

// syncFS flushes a whole file system, syncfs(2) (tests count the calls).
var syncFS = unix.Syncfs

// syncDir flushes dir's entries: a rename into it is on the disk once it
// returns. It cannot undo a rename that took place, so a failure is not
// reported, as for a configuration file (see writeConfFile).
func syncDir(dir string) {
	d, err := os.Open(dir) //nolint:gosec // G304: the directory of a file just written.
	if err != nil {
		return
	}
	_ = fsync(d)
	_ = d.Close()
}

// syncRootDir is syncDir for a directory inside root.
func syncRootDir(root *os.Root, dir string) {
	d, err := root.Open(dir)
	if err != nil {
		return
	}
	_ = fsync(d)
	_ = d.Close()
}

// syncFilesystems flushes the file systems holding dirs, each once.
func syncFilesystems(dirs []string) error {
	seen := map[uint64]bool{}
	var errs []error
	for _, dir := range dirs {
		d, err := os.Open(dir) //nolint:gosec // G304: directories of files just written.
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var st unix.Stat_t
		if unix.Fstat(int(d.Fd()), &st) == nil {
			if seen[st.Dev] {
				_ = d.Close()
				continue
			}
			seen[st.Dev] = true
		}
		if err := syncFS(int(d.Fd())); err != nil {
			errs = append(errs, &os.PathError{Op: "syncfs", Path: dir, Err: err})
		}
		_ = d.Close()
	}
	return errors.Join(errs...)
}

// stageFile writes data, with mode, to a new file beside path that is to
// take its name, and returns the new file's name. The mode is set through
// the descriptor before any content is in it, so a private file (a key)
// is never readable by others; with durable the content is flushed to the
// disk.
func stageFile(path string, data []byte, mode os.FileMode, durable bool) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: system configuration directories.
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cairn-*") // created 0600
	if err != nil {
		return "", err
	}
	err = tmp.Chmod(mode)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil && durable {
		err = fsync(tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// fileWrite is a file of a batch for writeFilesAs.
type fileWrite struct {
	path string
	data []byte
	mode os.FileMode
}

// batchFlushEach is the largest batch whose files are flushed one by one;
// a larger one (a refresh of every site) flushes their file systems
// instead, twice in all, rather than twice per file.
const batchFlushEach = 16

// writeFilesAs writes files as writeFileAs does, every one replaced
// atomically and its content on the disk before its rename, with fewer
// flushes for a large batch: all the contents first, then the renames. It
// goes on past a file it cannot write, and returns what failed: files
// renamed are in place, the others are not touched.
func writeFilesAs(files []fileWrite) error {
	each := len(files) <= batchFlushEach
	type staged struct{ tmp, path string }
	var ready []staged
	var dirs []string
	var errs []error
	for _, w := range files {
		tmp, err := stageFile(w.path, w.data, w.mode, each)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ready = append(ready, staged{tmp, w.path})
		if dir := filepath.Dir(w.path); !contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	if !each {
		if err := syncFilesystems(dirs); err != nil {
			for _, s := range ready {
				_ = os.Remove(s.tmp)
			}
			return errors.Join(append(errs, err)...)
		}
	}
	for _, s := range ready {
		if err := os.Rename(s.tmp, s.path); err != nil {
			_ = os.Remove(s.tmp)
			errs = append(errs, err)
		}
	}
	// The renames: as for a single file, nothing to report once they took
	// place.
	if each {
		for _, dir := range dirs {
			syncDir(dir)
		}
	} else {
		_ = syncFilesystems(dirs)
	}
	return errors.Join(errs...)
}
