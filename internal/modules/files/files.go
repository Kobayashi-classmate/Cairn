// SPDX-License-Identifier: AGPL-3.0-only

// Package files is the file manager. The root helper does every file
// access, inside the roots it allows (the sites, the panel's backups and,
// when an administrator turned it on, the whole system); this module
// turns requests into helper actions and jobs, serves downloads from the
// descriptors the helper hands over, and takes resumable uploads (tus)
// into the panel's own directory before the helper moves them into place.
package files

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Read   authz.Permission = "files.read"
	Manage authz.Permission = "files.manage"
	// Admin turns full-disk access on and off (administrators only).
	Admin authz.Permission = "files.admin"
)

// Helper is what the module needs from the root helper: calls, and calls
// that answer with an open file.
type Helper interface {
	jobs.HelperClient
	CallFD(ctx context.Context, action string, caller helperapi.Caller, req, resp any) (*os.File, error)
}

// Module implements module.Module.
type Module struct {
	Jobs   *jobs.Manager
	Helper Helper
	DB     *sql.DB
	Log    *slog.Logger
	// DataDir is the panel's data directory; uploads live in its uploads/.
	DataDir string
	// HTTP fetches files from URLs (nil: a default client).
	HTTP *http.Client

	uploads uploadLocks
}

// ID implements module.Module.
func (m *Module) ID() string { return "files" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Read, Roles: []authz.Role{authz.Operator}},
			{Name: Manage, Roles: []authz.Role{authz.Operator}},
			{Name: Admin},
		},
		Nav: []module.NavItem{
			{Key: "files", LabelKey: "common.nav.files", Icon: "folder", Route: "files", Permission: Read, Order: 19},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("files", s) }
	job := api.Returns(202, jobResponse{})
	r.Handle("GET /api/v1/files/roots", m.roots, api.Require(Read), doc("Roots, the access switches and the archive tools"), api.Returns(200, rootsResponse{}))
	r.Handle("PUT /api/v1/files/settings", m.settings, api.Require(Admin), api.StepUp(), api.Body(settingsRequest{}),
		doc("Turn full-disk access on or off"), api.Returns(200, helperapi.AccessSettings{}))
	r.Handle("GET /api/v1/files/list", m.list, api.Require(Read), doc("A directory: ?root=&path=&hidden=true"), api.Returns(200, helperapi.FileList{}))
	r.Handle("GET /api/v1/files/stat", m.stat, api.Require(Read), doc("One entry: ?root=&path="), api.Returns(200, helperapi.FileEntry{}))
	r.Handle("GET /api/v1/files/content", m.content, api.Require(Read), doc("A text file for the editor: ?root=&path="), api.Returns(200, helperapi.FileContent{}))
	r.Handle("PUT /api/v1/files/content", m.save, api.Require(Manage), api.Body(helperapi.FileWriteRequest{}),
		doc("Save a text file; etag is the version read (empty: a new file); answers with the new etag"), api.Returns(200, helperapi.FileSaved{}))
	r.Handle("GET /api/v1/files/download", m.download, api.Require(Read),
		doc("Download a file: ?root=&path=; &inline=true shows images in the browser"), api.ReturnsFile(200, "application/octet-stream"))
	r.Handle("POST /api/v1/files/mkdir", m.mkdir, api.Require(Manage), api.Body(helperapi.FileNewRequest{}), doc("Make a directory"), api.Returns(200, helperapi.FileEntry{}))
	r.Handle("POST /api/v1/files/touch", m.touch, api.Require(Manage), api.Body(helperapi.FileNewRequest{}), doc("Make an empty file"), api.Returns(200, helperapi.FileEntry{}))
	r.Handle("POST /api/v1/files/rename", m.rename, api.Require(Manage), api.Body(helperapi.FileRenameRequest{}), doc("Rename an entry"), api.Returns(200, helperapi.FileEntry{}))
	r.Handle("GET /api/v1/files/search", m.search, api.Require(Read),
		doc("Find entries below ?root=&path= whose name (or, with content=true, text) contains ?q="), api.Returns(200, helperapi.FileSearchResult{}))
	r.Handle("POST /api/v1/files/copy", m.op("file.copy", helperapi.ActionFileCopy, decodeTransfer), api.Require(Manage),
		api.Body(helperapi.FileTransferRequest{}), doc("Copy entries into a directory (starts a job)"), job)
	r.Handle("POST /api/v1/files/move", m.op("file.move", helperapi.ActionFileMove, decodeTransfer), api.Require(Manage),
		api.Body(helperapi.FileTransferRequest{}), doc("Move entries into a directory (starts a job)"), job)
	r.Handle("POST /api/v1/files/delete", m.op("file.delete", helperapi.ActionFileDelete, decodeDelete), api.Require(Manage),
		api.Body(helperapi.FileDeleteRequest{}), doc("Move entries to the trash, or with permanent=true (second verification) delete them (starts a job)"), job)
	r.Handle("POST /api/v1/files/chmod", m.op("file.chmod", helperapi.ActionFileChmod, decodeChmod), api.Require(Manage),
		api.Body(helperapi.FileChmodRequest{}), doc("Change permissions (starts a job)"), job)
	r.Handle("POST /api/v1/files/chown", m.op("file.chown", helperapi.ActionFileChown, decodeChown), api.Require(Manage), api.StepUp(),
		api.Body(helperapi.FileChownRequest{}), doc("Change owner and group (starts a job)"), job)
	r.Handle("POST /api/v1/files/compress", m.op("file.compress", helperapi.ActionFileCompress, decodeCompress), api.Require(Manage),
		api.Body(helperapi.FileCompressRequest{}), doc("Archive entries (zip, tar.gz, tar.zst, tar.xz) (starts a job)"), job)
	r.Handle("POST /api/v1/files/extract", m.op("file.extract", helperapi.ActionFileExtract, decodeExtract), api.Require(Manage),
		api.Body(helperapi.FileExtractRequest{}), doc("Extract an archive (starts a job)"), job)
	r.Handle("GET /api/v1/files/trash", m.trash, api.Require(Read), doc("A root's trash: ?root="), api.Returns(200, helperapi.TrashList{}))
	r.Handle("POST /api/v1/files/trash/restore", m.op("file.restore", helperapi.ActionTrashRestore, decodeRestore), api.Require(Manage),
		api.Body(helperapi.TrashRequest{}), doc("Put trash entries back (starts a job)"), job)
	r.Handle("POST /api/v1/files/trash/purge", m.op("file.purge", helperapi.ActionTrashPurge, decodePurge), api.Require(Manage), api.StepUp(),
		api.Body(helperapi.TrashPurgeRequest{}), doc("Delete trash entries for good (starts a job)"), job)
	r.Handle("GET /api/v1/files/favorites", m.favorites, api.Require(Read), doc("Your favourite directories"), api.Returns(200, favoritesResponse{}))
	r.Handle("POST /api/v1/files/favorites", m.addFavorite, api.Require(Read), api.Body(helperapi.FileRef{}), doc("Add a favourite directory"), api.Returns(204, nil))
	r.Handle("DELETE /api/v1/files/favorites", m.removeFavorite, api.Require(Read), doc("Remove a favourite: ?root=&path="), api.Returns(204, nil))
	m.uploadRoutes(r, doc)
}

type rootsResponse struct {
	helperapi.FileRoots
	Archivers helperapi.Archivers `json:"archivers"`
}

type settingsRequest struct {
	FullDisk bool `json:"fullDisk"`
}

type jobResponse struct {
	Job jobs.Record `json:"job"`
}

type favoritesResponse struct {
	Favorites []helperapi.FileRef `json:"favorites"`
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func badRequest(err error) error {
	var he *helperapi.Error
	if errors.As(err, &he) {
		return api.NewError(http.StatusBadRequest, he.Code, he.Params)
	}
	return err
}

// helperError gives a helper refusal its HTTP status.
func helperError(err error) error {
	he, ok := helperapi.AsError(err)
	if !ok {
		return err
	}
	status := http.StatusConflict
	switch he.Code {
	case helperapi.CodeFileNotFound, helperapi.CodeFileRootUnavailable:
		status = http.StatusNotFound
	case helperapi.CodeInvalidRequest, "validation.invalid":
		status = http.StatusBadRequest
	case helperapi.CodeFileTooLarge:
		status = http.StatusRequestEntityTooLarge
	case helperapi.CodeUnavailable:
		status = http.StatusServiceUnavailable
	}
	return api.NewError(status, he.Code, he.Params)
}

func (m *Module) call(r *http.Request, action string, req, resp any) error {
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	return helperError(m.Helper.Call(ctx, action, callerOf(r), req, resp))
}

func refOf(r *http.Request) (helperapi.FileRef, error) {
	q := helperapi.FileRef{Root: r.URL.Query().Get("root"), Path: r.URL.Query().Get("path")}
	return q, badRequest(q.Validate())
}

func (m *Module) roots(w http.ResponseWriter, r *http.Request) error {
	var resp rootsResponse
	if err := m.call(r, helperapi.ActionFileRoots, nil, &resp.FileRoots); err != nil {
		return err
	}
	if err := m.call(r, helperapi.ActionFileArchiving, nil, &resp.Archivers); err != nil {
		return err
	}
	if resp.Roots == nil {
		resp.Roots = []helperapi.FileRoot{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) settings(w http.ResponseWriter, r *http.Request) error {
	var req settingsRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	var cur helperapi.FileRoots
	if err := m.call(r, helperapi.ActionFileRoots, nil, &cur); err != nil {
		return err
	}
	s := cur.Settings
	s.FullDisk = req.FullDisk
	if err := m.call(r, helperapi.ActionAccessSet, s, &s); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "files.full_disk", "ok", map[string]any{"enabled": req.FullDisk})
	return api.WriteJSON(w, http.StatusOK, s)
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	ref, err := refOf(r)
	if err != nil {
		return err
	}
	var resp helperapi.FileList
	req := helperapi.FileListRequest{Root: ref.Root, Path: ref.Path, Hidden: r.URL.Query().Get("hidden") == "true"}
	if err := m.call(r, helperapi.ActionFileList, req, &resp); err != nil {
		return err
	}
	if resp.Entries == nil {
		resp.Entries = []helperapi.FileEntry{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) stat(w http.ResponseWriter, r *http.Request) error {
	ref, err := refOf(r)
	if err != nil {
		return err
	}
	var resp helperapi.FileEntry
	if err := m.call(r, helperapi.ActionFileStat, ref, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) content(w http.ResponseWriter, r *http.Request) error {
	ref, err := refOf(r)
	if err != nil {
		return err
	}
	var resp helperapi.FileContent
	if err := m.call(r, helperapi.ActionFileRead, ref, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// audited runs a synchronous change and audits it.
func audited[Req interface{ Validate() error }](m *Module, action, kind string, params func(Req) map[string]any) api.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req Req
		if err := api.DecodeJSON(r, &req); err != nil {
			return err
		}
		if err := req.Validate(); err != nil {
			return badRequest(err)
		}
		var resp helperapi.FileEntry
		if err := m.call(r, action, req, &resp); err != nil {
			return err
		}
		modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), kind, "ok", params(req))
		return api.WriteJSON(w, http.StatusOK, resp)
	}
}

// save answers with the new version, for the next save.
func (m *Module) save(w http.ResponseWriter, r *http.Request) error {
	var req helperapi.FileWriteRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.FileSaved
	if err := m.call(r, helperapi.ActionFileWrite, req, &resp); err != nil {
		return err
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "file.save", "ok",
		map[string]any{"root": req.Root, "path": req.Path, "size": len(req.Content), "new": req.ETag == ""})
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) mkdir(w http.ResponseWriter, r *http.Request) error {
	return audited(m, helperapi.ActionFileMkdir, "file.mkdir", func(q helperapi.FileNewRequest) map[string]any {
		return map[string]any{"root": q.Root, "path": join(q.Dir, q.Name)}
	})(w, r)
}

func (m *Module) touch(w http.ResponseWriter, r *http.Request) error {
	return audited(m, helperapi.ActionFileTouch, "file.touch", func(q helperapi.FileNewRequest) map[string]any {
		return map[string]any{"root": q.Root, "path": join(q.Dir, q.Name)}
	})(w, r)
}

func (m *Module) rename(w http.ResponseWriter, r *http.Request) error {
	return audited(m, helperapi.ActionFileRename, "file.rename", func(q helperapi.FileRenameRequest) map[string]any {
		return map[string]any{"root": q.Root, "path": q.Path, "name": q.Name}
	})(w, r)
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (m *Module) search(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	req := helperapi.FileSearchRequest{Root: q.Get("root"), Path: q.Get("path"), Query: q.Get("q"),
		Content: q.Get("content") == "true", CaseSensitive: q.Get("case") == "true"}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.FileSearchResult
	if err := m.call(r, helperapi.ActionFileSearch, req, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) trash(w http.ResponseWriter, r *http.Request) error {
	req := helperapi.FileRootRef{Root: r.URL.Query().Get("root")}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var resp helperapi.TrashList
	if err := m.call(r, helperapi.ActionTrashList, req, &resp); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

// Jobs --------------------------------------------------------------------

// decoded is a validated operation request with what the job shows.
type decoded struct {
	req    any
	params map[string]any
	// stepUp: this request needs a second verification.
	stepUp bool
}

func decodeInto[Req interface{ Validate() error }](r *http.Request, params func(Req) (map[string]any, bool)) (decoded, error) {
	var req Req
	if err := api.DecodeJSON(r, &req); err != nil {
		return decoded{}, err
	}
	if err := req.Validate(); err != nil {
		return decoded{}, badRequest(err)
	}
	p, stepUp := params(req)
	return decoded{req: req, params: p, stepUp: stepUp}, nil
}

func first(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func decodeTransfer(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileTransferRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": first(q.Paths), "count": len(q.Paths), "dest": q.Dest}, false
	})
}

func decodeDelete(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileDeleteRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": first(q.Paths), "count": len(q.Paths), "permanent": q.Permanent}, q.Permanent
	})
}

func decodeChmod(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileChmodRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": first(q.Paths), "count": len(q.Paths), "mode": strconv.FormatUint(uint64(q.Mode), 8),
			"recursive": q.Recursive}, false
	})
}

func decodeChown(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileChownRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": first(q.Paths), "count": len(q.Paths), "user": q.User, "group": q.Group,
			"recursive": q.Recursive}, false
	})
}

func decodeCompress(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileCompressRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": join(q.Dir, q.Name), "count": len(q.Names)}, false
	})
}

func decodeExtract(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.FileExtractRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "path": q.Path, "dest": q.Dest}, false
	})
}

func decodeRestore(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.TrashRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "count": len(q.IDs)}, false
	})
}

func decodePurge(r *http.Request) (decoded, error) {
	return decodeInto(r, func(q helperapi.TrashPurgeRequest) (map[string]any, bool) {
		return map[string]any{"root": q.Root, "count": len(q.IDs)}, false
	})
}

// op handles a request that runs as a helper operation inside a job.
func (m *Module) op(kind, action string, decode func(*http.Request) (decoded, error)) api.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		d, err := decode(r)
		if err != nil {
			return err
		}
		if p, ok := api.PrincipalFrom(r.Context()); d.stepUp && (!ok || p.ElevatedUntil <= time.Now().Unix()) {
			return api.ErrStepUpRequired
		}
		job, err := m.startOp(r, kind, action, d.req, d.params)
		if err != nil {
			return err
		}
		return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
	}
}

func (m *Module) startOp(r *http.Request, kind, action string, req any, params map[string]any) (jobs.Record, error) {
	who := modutil.ActorOf(r)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: kind, Params: params, Permission: Read, ManagePermission: Manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(_ context.Context, j *jobs.Job) error {
		var res helperapi.FileOpResult
		err := j.RunOp(action, req, &res)
		if err == nil && len(res.Failed) > 0 {
			j.Logf("%d done, %d failed.\n", res.Done, len(res.Failed))
		}
		return err
	})
	if err != nil {
		return job, err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, kind, "ok", audit)
	return job, nil
}

// Favourites ---------------------------------------------------------------

func (m *Module) favorites(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.DB.QueryContext(r.Context(), `SELECT root, path FROM file_favorites WHERE user_id = ? ORDER BY root, path`, modutil.ActorOf(r).UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	resp := favoritesResponse{Favorites: []helperapi.FileRef{}}
	for rows.Next() {
		var f helperapi.FileRef
		if err := rows.Scan(&f.Root, &f.Path); err != nil {
			return err
		}
		resp.Favorites = append(resp.Favorites, f)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) addFavorite(w http.ResponseWriter, r *http.Request) error {
	var req helperapi.FileRef
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		return badRequest(err)
	}
	var n int
	_ = m.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM file_favorites WHERE user_id = ?`, modutil.ActorOf(r).UserID).Scan(&n)
	if n >= 100 {
		return api.NewError(http.StatusConflict, "file.too_many_favorites", map[string]any{"max": 100})
	}
	if _, err := m.DB.ExecContext(r.Context(), `INSERT OR IGNORE INTO file_favorites (user_id, root, path, created_at) VALUES (?, ?, ?, ?)`,
		modutil.ActorOf(r).UserID, req.Root, req.Path, time.Now().Unix()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (m *Module) removeFavorite(w http.ResponseWriter, r *http.Request) error {
	ref, err := refOf(r)
	if err != nil {
		return err
	}
	if _, err := m.DB.ExecContext(r.Context(), `DELETE FROM file_favorites WHERE user_id = ? AND root = ? AND path = ?`,
		modutil.ActorOf(r).UserID, ref.Root, ref.Path); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
