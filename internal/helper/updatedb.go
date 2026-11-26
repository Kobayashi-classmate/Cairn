// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// The panel's database across an update. A new version may migrate it as
// it starts, and the previous version must not run on what it made of it
// (store.Migrate refuses that), so a rollback puts back a copy taken before
// the installation.
//
// The copy is made while cairn-web is stopped: then nothing writes the
// database, and its file together with its write-ahead log (which holds
// committed transactions when cairn-web did not stop cleanly) is a
// consistent state. Copying while it runs would need SQLite's own backup,
// opening the panel's files as root through paths the panel account can
// replace with links. The panel owns the data directory, so every file
// there is reached through the open directory and by a single name.

// dbFiles are the files of the panel's database that a copy keeps. The
// shared-memory index (-shm) is rebuilt from the log; in WAL mode there is
// no rollback journal.
var dbFiles = []string{"cairn.db", "cairn.db-wal"}

// dbLeftovers are what may lie next to the database. A restore removes
// them first: a newer version's log would otherwise be replayed into the
// older database.
var dbLeftovers = []string{"cairn.db-wal", "cairn.db-shm", "cairn.db-journal"}

// dbManifest describes a complete copy; it is written last.
const dbManifest = "manifest.json"

type dbCopy struct {
	Version string       `json:"version"` // the version the copy was taken from
	Files   []dbCopyFile `json:"files"`
}

type dbCopyFile struct {
	Name   string `json:"name"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// backupDatabase copies the database in dataDir into dest (a root-only
// directory, replaced), taken while version ran.
func backupDatabase(dataDir, dest, version string) error {
	data, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	c := dbCopy{Version: version}
	for _, name := range dbFiles {
		f, err := copyDBFileOut(data, name, filepath.Join(dest, name))
		if errors.Is(err, fs.ErrNotExist) && name != dbFiles[0] {
			continue // stopped cleanly: everything is in the database file
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		c.Files = append(c.Files, f)
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dest, dbManifest+".tmp")
	if err := writeSynced(tmp, body); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dest, dbManifest)); err != nil {
		return err
	}
	return syncPath(dest)
}

// copyDBFileOut copies the file name of the panel's directory to dst. Only
// a regular file with no other name is taken: a link, or a second name of
// a file elsewhere, would make root copy what the panel cannot read.
func copyDBFileOut(data *os.Root, name, dst string) (dbCopyFile, error) {
	in, err := openNoLink(data, name)
	if err != nil {
		return dbCopyFile{}, err
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return dbCopyFile{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || st.Nlink != 1 {
		return dbCopyFile{}, errors.New("not a regular file of its own")
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: in the helper's own state directory.
	if err != nil {
		return dbCopyFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return dbCopyFile{}, err
	}
	return dbCopyFile{Name: name, UID: int(st.Uid), GID: int(st.Gid), Mode: uint32(fi.Mode().Perm()), Size: n,
		SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// restoreDatabase puts the copy in src back into dataDir, with the owner
// and mode the files had, after checking it against its manifest. The
// database is replaced by rename, so it is either the old or the new one.
func restoreDatabase(dataDir, src string) error {
	body, err := os.ReadFile(filepath.Join(src, dbManifest)) //nolint:gosec // G304: in the helper's own state directory.
	if err != nil {
		return fmt.Errorf("no complete copy of the database: %w", err)
	}
	var c dbCopy
	if err := json.Unmarshal(body, &c); err != nil {
		return fmt.Errorf("the copy's manifest: %w", err)
	}
	if len(c.Files) == 0 || c.Files[0].Name != dbFiles[0] {
		return errors.New("the copy's manifest lists no database")
	}
	data, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	dir, err := data.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()

	var temps []string
	defer func() {
		for _, t := range temps {
			_ = data.Remove(t) // gone once renamed
		}
	}()
	for _, f := range c.Files {
		if !slices.Contains(dbFiles, f.Name) {
			return fmt.Errorf("the copy's manifest names %q", f.Name)
		}
		tmp, err := copyDBFileIn(int(dir.Fd()), filepath.Join(src, f.Name), f)
		if tmp != "" {
			temps = append(temps, tmp)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	for _, name := range dbLeftovers {
		if err := data.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for i, f := range c.Files {
		if err := renameAt(data, temps[i], data, f.Name, false); err != nil {
			return err
		}
	}
	return dir.Sync()
}

// copyDBFileIn writes the copied file src into a new private file of the
// open directory dirfd, as f describes it, and returns that file's name.
func copyDBFileIn(dirfd int, src string, f dbCopyFile) (string, error) {
	in, err := os.Open(src) //nolint:gosec // G304: in the helper's own state directory.
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	name, fd, err := createTempAt(dirfd)
	if err != nil {
		return "", err
	}
	out := os.NewFile(uintptr(fd), name)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), in)
	switch {
	case err != nil:
	case n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256:
		err = errors.New("the copy does not match its manifest")
	default:
		if err = out.Chown(f.UID, f.GID); err == nil {
			if err = out.Chmod(fs.FileMode(f.Mode).Perm()); err == nil {
				err = out.Sync()
			}
		}
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return name, err
}

// writeSynced writes a new private file and flushes it to disk.
func writeSynced(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: callers pass paths in the helper's own state directory.
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// syncPath flushes a directory's entries to disk.
func syncPath(p string) error {
	d, err := os.Open(p) //nolint:gosec // G304: callers pass paths in the helper's own state directory.
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
