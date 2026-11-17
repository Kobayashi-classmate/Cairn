// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/sources"
)

// Switching the system's package sources (decided for P1 batch 9): only to
// a candidate the probe found eligible (the same index as the official
// source), after a backup of every file it changes; the package manager
// then reads the new indexes, and on failure the files come back.

const syssrcKeep = 10

func registerSysSources(r *Registry, o *Ops, h *Host) {
	Register(r, helperapi.ActionSysSourcesProbe, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.SysSourcesProbe, error) {
		if h.Software == nil || h.manager() == "" {
			return helperapi.SysSourcesProbe{Slots: []helperapi.SourceSlot{}}, hostErr(helperapi.CodeSysSrcUnsupported, nil)
		}
		return helperapi.SysSourcesProbe{Slots: h.probeSystemSources(ctx)}, nil
	})
	Register(r, helperapi.ActionSysSourcesBackups, func(_ context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.SysSourceBackups, error) {
		return helperapi.SysSourceBackups{Backups: h.sourceBackups()}, nil
	})
	pkg := func(any) []string { return []string{LockPkg, "syssrc"} }
	RegisterOp(r, o, helperapi.ActionSysSourcesSwitch, OpSpec[helperapi.SysSourceSwitchRequest]{
		Locks: func(q helperapi.SysSourceSwitchRequest) []string { return pkg(q) }, Run: h.switchSource})
	RegisterOp(r, o, helperapi.ActionSysSourcesRestore, OpSpec[helperapi.SysSourceRef]{
		Locks: func(q helperapi.SysSourceRef) []string { return pkg(q) }, Run: h.restoreSource})
}

// systemRoles are the roles the catalog describes for this distribution.
func (h *Host) systemRoles() []string {
	var roles []string
	for _, role := range []string{"main", "security"} {
		if _, ok := h.Software.systemRepo(role); ok {
			roles = append(roles, role)
		}
	}
	return roles
}

func (h *Host) probeSystemSources(ctx context.Context) []helperapi.SourceSlot {
	slots := []helperapi.SourceSlot{}
	if h.Software == nil {
		return slots
	}
	for _, role := range h.systemRoles() {
		sl := helperapi.SourceSlot{ID: "system:" + role, Kind: "system", Software: []string{}, Selectable: true}
		h.Software.probeSystem(ctx, &sl, role)
		slots = append(slots, sl)
	}
	return slots
}

func (h *Host) syssrcDir(parts ...string) string {
	return filepath.Join(append([]string{h.StateDir, "syssrc"}, parts...)...)
}

// sourceBackup is a backup with the files' contents.
type sourceBackup struct {
	helperapi.SysSourceBackup
	Contents map[string]string `json:"contents"`
	Modes    map[string]uint32 `json:"modes"`
}

func (h *Host) sourceBackups() []helperapi.SysSourceBackup {
	out := []helperapi.SysSourceBackup{}
	entries, _ := os.ReadDir(h.syssrcDir())
	for _, e := range entries {
		if b, err := h.loadBackup(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			out = append(out, b.SysSourceBackup)
		}
	}
	sortByTime(out)
	return out
}

func (h *Host) loadBackup(id string) (sourceBackup, error) {
	var b sourceBackup
	if (helperapi.SysSourceRef{ID: id}).Validate() != nil {
		return b, hostErr(helperapi.CodeSysSrcBackupNotFound, map[string]any{"id": id})
	}
	data, err := os.ReadFile(h.syssrcDir(id + ".json"))
	if err != nil {
		return b, hostErr(helperapi.CodeSysSrcBackupNotFound, map[string]any{"id": id})
	}
	return b, json.Unmarshal(data, &b)
}

func (h *Host) refreshIndexes(ctx context.Context, log io.Writer) error {
	var out strings.Builder
	w := io.MultiWriter(log, &limitedBuilder{b: &out, max: 64 << 10})
	var err error
	if h.manager() == "apt" {
		err = runCommand(ctx, w, "apt-get", "update")
		// apt-get update succeeds with W: lines when a source fails.
		if err == nil && (strings.Contains(out.String(), "\nE: ") || strings.Contains(out.String(), "\nW: Failed to fetch")) {
			err = errors.New("apt-get update could not read every source")
		}
	} else {
		err = runCommand(ctx, w, "dnf", "makecache", "--assumeyes", "--refresh")
	}
	if err != nil {
		return hostErr(helperapi.CodeSysSrcUpdateFailed, map[string]any{"detail": tailString(strings.TrimSpace(out.String()), 600)})
	}
	return nil
}

// writeAll writes files, keeping each file's mode.
func (h *Host) writeAll(files map[string][]byte, modes map[string]uint32) error {
	for p, data := range files {
		mode := os.FileMode(0o644)
		if m, ok := modes[p]; ok {
			mode = os.FileMode(m)
		}
		if err := writeFileMode(h.path(p), data, mode); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) switchSource(ctx context.Context, _ helperapi.Caller, q helperapi.SysSourceSwitchRequest, log io.Writer) (any, error) {
	if h.Software == nil || h.manager() == "" || !slices.Contains(h.systemRoles(), q.Role) {
		return nil, hostErr(helperapi.CodeSysSrcUnsupported, map[string]any{"role": q.Role})
	}
	_, _ = fmt.Fprintf(log, "Checking the candidates of the %s source again.\n", q.Role)
	sl := helperapi.SourceSlot{ID: "system:" + q.Role, Kind: "system"}
	h.Software.probeSystem(ctx, &sl, q.Role, q.Candidate)
	i := slices.IndexFunc(sl.Candidates, func(c sources.Candidate) bool { return c.ID == q.Candidate })
	if i < 0 || !sl.Candidates[i].Eligible {
		return nil, hostErr(helperapi.CodeSysSrcNotEligible, map[string]any{"candidate": q.Candidate})
	}
	to := sl.Candidates[i]
	if to.InUse {
		return nil, hostErr(helperapi.CodeSysSrcInUse, map[string]any{"candidate": q.Candidate})
	}
	var from []string
	for _, c := range sl.Candidates {
		if c.InUse {
			from = append(from, c.URL)
		}
	}
	if len(from) == 0 {
		return nil, hostErr(helperapi.CodeSysSrcUnsupported, map[string]any{"reason": "no_source_in_use"})
	}
	var orig map[string][]byte
	changed := map[string][]byte{}
	count := 0
	if h.manager() == "apt" {
		orig = h.Software.readConfig("/etc/apt/sources.list", "/etc/apt/sources.list.d/*.list", "/etc/apt/sources.list.d/*.sources")
		// Entries written as "mirror+file:" lists name the mirror in use
		// indirectly: they move too.
		for _, e := range sources.ParseAPTSources(orig) {
			if r := h.Software.resolveAPTURI(e.URI); r != e.URI && slices.ContainsFunc(from, func(f string) bool { return sources.SameBase(f, r) }) &&
				!slices.Contains(from, e.URI) {
				from = append(from, e.URI)
			}
		}
		var exclude []string
		for _, role := range h.systemRoles() {
			if role == q.Role {
				continue
			}
			sys, _ := h.Software.systemRepo(role)
			for _, su := range sys.Suites {
				exclude = append(exclude, h.Software.expand(su, ""))
			}
		}
		work := maps.Clone(orig)
		for _, f := range from {
			out, n, err := sources.RewriteAPT(work, sl.Suites, exclude, f, strings.TrimSuffix(to.URL, "/"))
			var mixed *sources.MixedError
			if errors.As(err, &mixed) {
				return nil, hostErr(helperapi.CodeSysSrcMixed, map[string]any{"file": mixed.File})
			}
			if err != nil {
				return nil, err
			}
			for p, d := range out {
				changed[p] = d
				work[p] = d
			}
			count += n
		}
	} else {
		if !strings.HasPrefix(to.URL, "https://") {
			// The panel's .repo files check package signatures but not
			// the metadata, so a mirror must be reached over HTTPS.
			return nil, hostErr(helperapi.CodeSysSrcHTTPSRequired, map[string]any{"candidate": q.Candidate})
		}
		mirror := sources.MirrorRoot(to.URL)
		if mirror == "" {
			return nil, hostErr(helperapi.CodeSysSrcUnsupported, map[string]any{"reason": "mirror_layout"})
		}
		sys, _ := h.Software.systemRepo(q.Role)
		roots := slices.Clone(sys.Roots)
		for _, f := range from {
			if r := sources.MirrorRoot(f); r != "" {
				roots = append(roots, r)
			}
		}
		orig = h.Software.readConfig("/etc/yum.repos.d/*.repo")
		changed, count = sources.RewriteDNF(orig, roots, mirror)
	}
	if count == 0 {
		return nil, hostErr(helperapi.CodeSysSrcUnsupported, map[string]any{"reason": "no_entry"})
	}
	// Back up exactly the files that change.
	b := sourceBackup{SysSourceBackup: helperapi.SysSourceBackup{ID: h.newBackupID(), At: h.clock().Unix(), Role: q.Role,
		From: sources.Host(from[0]), To: sources.Host(to.URL), Files: []string{}}, Contents: map[string]string{}, Modes: map[string]uint32{}}
	for p := range changed {
		rel := strings.TrimPrefix(p, h.Software.Root)
		b.Files = append(b.Files, rel)
		b.Contents[rel] = string(orig[p])
		if info, err := os.Stat(p); err == nil {
			b.Modes[rel] = uint32(info.Mode().Perm())
		}
	}
	slices.Sort(b.Files)
	data, _ := json.MarshalIndent(b, "", "  ")
	if err := os.MkdirAll(h.syssrcDir(), 0o700); err != nil {
		return nil, err
	}
	if err := writeFileMode(h.syssrcDir(b.ID+".json"), data, 0o600); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Backed up %s as %s.\n", strings.Join(b.Files, ", "), b.ID)
	rel := map[string][]byte{}
	for p, d := range changed {
		rel[strings.TrimPrefix(p, h.Software.Root)] = d
	}
	_, _ = fmt.Fprintf(log, "Switching the %s source from %s to %s (%d entries).\n", q.Role, b.From, b.To, count)
	if err := h.writeAll(rel, b.Modes); err != nil {
		_ = h.putBack(b)
		return nil, err
	}
	if err := h.refreshIndexes(ctx, log); err != nil {
		_, _ = fmt.Fprintf(log, "The new source did not work; putting the previous files back.\n")
		_ = h.putBack(b)
		_ = h.refreshIndexes(ctx, io.Discard)
		_ = os.Remove(h.syssrcDir(b.ID + ".json"))
		return nil, err
	}
	h.pruneBackups()
	return b.SysSourceBackup, nil
}

func (h *Host) putBack(b sourceBackup) error {
	files := map[string][]byte{}
	for p, c := range b.Contents {
		files[p] = []byte(c)
	}
	return h.writeAll(files, b.Modes)
}

func (h *Host) restoreSource(ctx context.Context, _ helperapi.Caller, q helperapi.SysSourceRef, log io.Writer) (any, error) {
	b, err := h.loadBackup(q.ID)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Putting back %s from %s.\n", strings.Join(b.Files, ", "), b.ID)
	if err := h.putBack(b); err != nil {
		return nil, err
	}
	return nil, h.refreshIndexes(ctx, log)
}

func (h *Host) newBackupID() string {
	r := make([]byte, 3)
	_, _ = rand.Read(r)
	return "b" + strconv.FormatInt(h.clock().Unix(), 10) + hex.EncodeToString(r)
}

func (h *Host) pruneBackups() {
	list := h.sourceBackups()
	for _, b := range list[min(len(list), syssrcKeep):] {
		_ = os.Remove(h.syssrcDir(b.ID + ".json"))
	}
}
