// SPDX-License-Identifier: AGPL-3.0-only

package databases

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Dumps live in the panel's own directory: the helper writes them there
// (owned by the panel), the panel serves, stores uploaded ones and
// deletes them, and the helper reads the one being restored.

const (
	dumpsDir      = "backups/db"
	maxUploadSize = 8 << 30
)

type dumpsResponse struct {
	Dumps []helperapi.DumpInfo `json:"dumps"`
}

type dumpRequest struct {
	Name string `json:"name,omitempty"` // "" for Redis
}

type restoreRequest struct {
	File string `json:"file"`
	// User restores the dump (its rights bound what the dump can do);
	// "" means the database's owner.
	User string `json:"user,omitempty"`
}

func (m *Module) dumpRoutes(r *api.Router, doc func(string) api.RouteOption) {
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/databases/{engine}/dumps", m.listDumps, api.Require(Read),
		doc("Dumps of an engine, newest first"), api.Returns(200, dumpsResponse{}))
	r.Handle("POST /api/v1/databases/{engine}/dumps", m.createDump, api.Require(Manage), api.Body(dumpRequest{}),
		doc("Dump a database, or take a Redis snapshot (starts a job)"), job)
	r.Handle("GET /api/v1/databases/{engine}/dumps/{file}", m.downloadDump, api.Require(Manage), api.StepUp(), api.Ticket(),
		doc("Download a dump; ?link=1 answers {url} with the address to download from instead (for a browser session, with a one-time ticket)"),
		api.ReturnsFile(200, "application/octet-stream"))
	r.Handle("PUT /api/v1/databases/{engine}/dumps/{file}", m.uploadDump, api.Require(Manage), api.StepUp(),
		api.RawBody("application/octet-stream"), doc("Upload a dump (.sql, .sql.gz or, for PostgreSQL, .dump)"), api.Returns(201, helperapi.DumpInfo{}))
	r.Handle("DELETE /api/v1/databases/{engine}/dumps/{file}", m.deleteDump, api.Require(Manage), api.StepUp(),
		doc("Delete a dump"), api.Returns(204, nil))
	r.Handle("POST /api/v1/databases/{engine}/dbs/{name}/restore", m.restore, api.Require(Manage), api.StepUp(), api.Body(restoreRequest{}),
		doc("Load a dump into a database as one of its users (starts a job)"), job)
}

// dumpRoot opens the data directory; links never lead out of it.
func (m *Module) dumpRoot() (*os.Root, error) { return os.OpenRoot(m.DataDir) }

func dumpFileOf(r *http.Request) (string, error) {
	f := r.PathValue("file")
	if !helperapi.ValidDumpName(f) {
		return "", api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "file", "rule": "dump_name"})
	}
	return f, nil
}

func (m *Module) listDumps(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	dumps, err := m.dumpInfos(engine)
	if err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, dumpsResponse{Dumps: dumps})
}

// dumpInfos lists the dumps of an engine, newest first.
func (m *Module) dumpInfos(engine string) ([]helperapi.DumpInfo, error) {
	out := []helperapi.DumpInfo{}
	root, err := m.dumpRoot()
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), path.Join(dumpsDir, engine))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !helperapi.ValidDumpName(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		d := helperapi.DumpInfo{Engine: engine, File: e.Name(), SizeBytes: fi.Size(), CreatedAt: fi.ModTime().Unix()}
		// Names start with the database: <name>_<time>.<ext>.
		if i := strings.LastIndex(e.Name(), "_"); i > 0 && helperapi.EngineKind(engine) != helperapi.DBRedis {
			d.Database = e.Name()[:i]
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (m *Module) createDump(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	var req dumpRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	q := helperapi.DBDumpRequest{Engine: engine, Name: req.Name}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	params := map[string]any{}
	if req.Name != "" {
		params["database"] = req.Name
	}
	who := modutil.ActorOf(r)
	// Dumps only read, so they do not wait for the engine's other jobs.
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "db.dump", Params: withEngine(params, engine), Permission: Read, ManagePermission: Manage, Locks: []string{"db-dump:" + engine},
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var info helperapi.DumpInfo
		if err := j.RunOp(helperapi.ActionDBDump, q, &info); err != nil {
			return err
		}
		j.Logf("Dump %s (%d bytes).\n", info.File, info.SizeBytes)
		return nil
	})
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, who, "db.dump", "ok", withEngine(map[string]any{"database": req.Name, "job": job.ID}, engine))
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

func withEngine(p map[string]any, engine string) map[string]any {
	p["engine"] = engine
	return p
}

// dumpLink is where a dump downloads from.
type dumpLink struct {
	URL string `json:"url"`
}

func (m *Module) downloadDump(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	file, err := dumpFileOf(r)
	if err != nil {
		return err
	}
	root, err := m.dumpRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	p := path.Join(dumpsDir, engine, file)
	fi, err := root.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return api.NewError(http.StatusNotFound, helperapi.CodeDBDumpNotFound, map[string]any{"file": file})
	}
	// ?link=1: the address to download from (this one), as JSON, so a page
	// can report errors before the browser leaves it.
	if r.URL.Query().Get("link") == "1" {
		link, err := modutil.DownloadLink(r, m.DB)
		if err != nil {
			return err
		}
		return api.WriteJSON(w, http.StatusOK, dumpLink{URL: link})
	}
	f, err := root.Open(p)
	if err != nil {
		return api.NewError(http.StatusNotFound, helperapi.CodeDBDumpNotFound, map[string]any{"file": file})
	}
	defer func() { _ = f.Close() }()
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "db.dump_download", "ok", map[string]any{"engine": engine, "file": file})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeContent(w, r, file, fi.ModTime(), f)
	return nil
}

func (m *Module) uploadDump(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	file, err := dumpFileOf(r)
	if err != nil {
		return err
	}
	if strings.HasSuffix(file, ".rdb") || (strings.HasSuffix(file, ".dump") && helperapi.EngineKind(engine) != helperapi.DBPostgreSQL) {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "file", "rule": "dump_format"})
	}
	if r.ContentLength > maxUploadSize {
		return api.NewError(http.StatusRequestEntityTooLarge, codeUploadTooLarge, map[string]any{"max": int64(maxUploadSize)})
	}
	root, err := m.dumpRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	dir := path.Join(dumpsDir, engine)
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	final := path.Join(dir, file)
	if _, err := root.Lstat(final); err == nil {
		return api.NewError(http.StatusConflict, codeDumpExists, map[string]any{"file": file})
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	tmp := path.Join(dir, ".upload-"+hex.EncodeToString(b))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = f.Close()
			_ = root.Remove(tmp)
		}
	}()
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, maxUploadSize))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return api.NewError(http.StatusRequestEntityTooLarge, codeUploadTooLarge, map[string]any{"max": int64(maxUploadSize)})
	}
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, final); err != nil {
		return err
	}
	done = true
	fi, err := root.Lstat(final)
	if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "db.dump_upload", "ok", map[string]any{"engine": engine, "file": file, "size": n})
	return api.WriteJSON(w, http.StatusCreated, helperapi.DumpInfo{Engine: engine, File: file, SizeBytes: n, CreatedAt: fi.ModTime().Unix()})
}

func (m *Module) deleteDump(w http.ResponseWriter, r *http.Request) error {
	engine, err := engineOf(r)
	if err != nil {
		return err
	}
	file, err := dumpFileOf(r)
	if err != nil {
		return err
	}
	root, err := m.dumpRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(path.Join(dumpsDir, engine, file)); errors.Is(err, fs.ErrNotExist) {
		return api.NewError(http.StatusNotFound, helperapi.CodeDBDumpNotFound, map[string]any{"file": file})
	} else if err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "db.dump_delete", "ok", map[string]any{"engine": engine, "file": file})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// restoreUser picks who restores: the requested user or the database's
// owner, which must have full access and a password the panel knows.
func (m *Module) restoreUser(ctx context.Context, engine, database, user string) (string, string, error) {
	if user == "" {
		_ = m.DB.QueryRowContext(ctx, `SELECT owner FROM db_databases WHERE engine = ? AND name = ?`, engine, database).Scan(&user)
	}
	needs := api.NewError(http.StatusBadRequest, codeRestoreNeeds, map[string]any{"database": database})
	if user == "" {
		return "", "", needs
	}
	rec, have, err := m.loadUser(ctx, engine, user)
	if err != nil {
		return "", "", err
	}
	full := slices.ContainsFunc(rec.grants, func(g helperapi.DBGrant) bool { return g.Database == database && g.Level == helperapi.GrantAll })
	if !have || rec.sealed == "" || !full {
		return "", "", needs
	}
	pw, err := m.password(rec)
	return user, pw, err
}

func (m *Module) restore(w http.ResponseWriter, r *http.Request) error {
	engine, err := sqlEngineOf(r)
	if err != nil {
		return err
	}
	var req restoreRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	name := r.PathValue("name")
	user, pw, err := m.restoreUser(r.Context(), engine, name, req.User)
	if err != nil {
		return err
	}
	q := helperapi.DBRestoreRequest{Engine: engine, Name: name, File: req.File, User: user, Password: pw}
	if err := q.Validate(); err != nil {
		return badRequest(err)
	}
	root, err := m.dumpRoot()
	if err != nil {
		return err
	}
	fi, err := root.Lstat(path.Join(dumpsDir, engine, req.File))
	_ = root.Close()
	if err != nil || !fi.Mode().IsRegular() {
		return api.NewError(http.StatusNotFound, helperapi.CodeDBDumpNotFound, map[string]any{"file": req.File})
	}
	return m.start(w, r, "db.restore", engine, map[string]any{"database": name, "file": req.File, "user": user}, func(_ context.Context, j *jobs.Job) error {
		return j.RunOp(helperapi.ActionDBRestore, q, nil)
	})
}
