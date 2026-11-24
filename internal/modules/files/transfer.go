// SPDX-License-Identifier: AGPL-3.0-only

package files

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
)

// Uploads follow tus 1.0 (core and creation): POST creates an upload,
// HEAD tells how much arrived, PATCH appends, DELETE abandons. The data
// goes to the panel's uploads/<id>.part; once complete, a job has the
// helper move it into place.

const (
	tusVersion    = "1.0.0"
	maxUpload     = 32 << 30
	uploadTTL     = 24 * time.Hour
	uploadsDir    = "uploads"
	spaceReserve  = 1 << 30
	fetchTimeout  = 6 * time.Hour
	cleanupPeriod = time.Hour
)

// Images shown in the browser; everything else is a download.
var inlineTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon", ".svg": "image/svg+xml",
}

func (m *Module) uploadRoutes(r *api.Router, doc func(string) api.RouteOption) {
	r.Handle("POST /api/v1/files/uploads", m.createUpload, api.Require(Manage), api.NoBody(),
		doc("Start a resumable upload (tus): Upload-Length and Upload-Metadata (root, dir, filename, overwrite)"), api.Returns(201, nil))
	r.Handle("HEAD /api/v1/files/uploads/{id}", m.headUpload, api.Require(Manage), api.NoBody(),
		doc("How much of an upload arrived (Upload-Offset)"), api.Returns(200, nil))
	r.Handle("PATCH /api/v1/files/uploads/{id}", m.patchUpload, api.Require(Manage), api.RawBody("application/offset+octet-stream"),
		doc("Append to an upload at Upload-Offset; the last part starts the job placing it (Cairn-Job header)"), api.Returns(204, nil))
	r.Handle("DELETE /api/v1/files/uploads/{id}", m.deleteUpload, api.Require(Manage), doc("Abandon an upload"), api.Returns(204, nil))
	r.Handle("POST /api/v1/files/fetch", m.fetch, api.Require(Manage), api.Body(fetchRequest{}),
		doc("Download a URL into a directory (starts a job)"), api.Returns(202, jobResponse{}))
}

// Downloads ----------------------------------------------------------------

func (m *Module) download(w http.ResponseWriter, r *http.Request) error {
	ref, err := refOf(r)
	if err != nil {
		return err
	}
	if err := stepUpFor(r, ref.Root); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	var entry helperapi.FileEntry
	file, err := m.Helper.CallFD(ctx, helperapi.ActionFileOpen, callerOf(r), ref, &entry)
	cancel()
	if err != nil {
		return helperError(err)
	}
	defer func() { _ = file.Close() }()
	name := entry.Name
	ctype, inline := inlineTypes[strings.ToLower(path.Ext(name))]
	inline = inline && r.URL.Query().Get("inline") == "true"
	h := w.Header()
	if inline {
		h.Set("Content-Type", ctype)
		// Shown on its own, an image (an SVG above all) runs nothing.
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": name}))
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "file.download", "ok", map[string]any{"root": ref.Root, "path": ref.Path, "size": entry.Size})
	http.ServeContent(w, r, "", time.Unix(entry.ModTime, 0), file)
	return nil
}

// Uploads -----------------------------------------------------------------

type upload struct {
	id, root, dir, name string
	userID              int64
	size, offset        int64
	overwrite           bool
	job                 string
}

func (m *Module) dataRoot() (*os.Root, error) {
	root, err := os.OpenRoot(m.DataDir)
	if err != nil {
		return nil, err
	}
	if err := root.Mkdir(uploadsDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func partOf(id string) string { return uploadsDir + "/" + id + ".part" }

// freeSpace is what may still be written to the uploads directory.
func (m *Module) freeSpace() int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Join(m.DataDir, uploadsDir), &st); err != nil {
		return 0
	}
	return int64(st.Bavail)*st.Bsize - spaceReserve //nolint:gosec // G115: block counts fit.
}

func tusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Cache-Control", "no-store")
}

// parseMetadata reads Upload-Metadata: "key base64,key base64".
func parseMetadata(s string) (map[string]string, bool) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, " ")
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, false
		}
		out[k] = string(dec)
	}
	return out, true
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Module) createUpload(w http.ResponseWriter, r *http.Request) error {
	tusHeaders(w)
	size, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || size < 0 {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "Upload-Length", "rule": "integer"})
	}
	if size > maxUpload {
		return api.NewError(http.StatusRequestEntityTooLarge, helperapi.CodeFileTooLarge, map[string]any{"max": int64(maxUpload)})
	}
	meta, ok := parseMetadata(r.Header.Get("Upload-Metadata"))
	if !ok {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "Upload-Metadata", "rule": "tus_metadata"})
	}
	target := helperapi.FileNewRequest{Root: meta["root"], Dir: meta["dir"], Name: meta["filename"]}
	if err := target.Validate(); err != nil {
		return badRequest(err)
	}
	if err := checkRoot(r, target.Root); err != nil {
		return err
	}
	// Asked when the upload begins only: its parts may take longer than the
	// verification lasts, and they belong to the account that began it.
	if err := stepUpFor(r, target.Root); err != nil {
		return err
	}
	root, err := m.dataRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if size > m.freeSpace() {
		return api.NewError(http.StatusInsufficientStorage, helperapi.CodeFileNoSpace, map[string]any{"size": size})
	}
	id := newID()
	f, err := root.OpenFile(partOf(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_ = f.Close()
	now := time.Now()
	if _, err := m.DB.ExecContext(r.Context(), `INSERT INTO file_uploads (id, user_id, root, dir, name, size, overwrite, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, modutil.ActorOf(r).UserID, target.Root, target.Dir, target.Name, size, meta["overwrite"] == "true",
		now.Unix(), now.Add(uploadTTL).Unix()); err != nil {
		_ = root.Remove(partOf(id))
		return err
	}
	w.Header().Set("Location", "/api/v1/files/uploads/"+id)
	w.Header().Set("Upload-Expires", now.Add(uploadTTL).UTC().Format(http.TimeFormat))
	if size == 0 {
		// Nothing to send: clients make no PATCH, so it is complete now.
		job, err := m.place(r, upload{id: id, root: target.Root, dir: target.Dir, name: target.Name, userID: modutil.ActorOf(r).UserID,
			overwrite: meta["overwrite"] == "true"})
		if err != nil {
			return err
		}
		w.Header().Set("Upload-Offset", "0")
		w.Header().Set("Cairn-Job", job.ID)
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

func (m *Module) loadUpload(r *http.Request) (upload, error) {
	id := r.PathValue("id")
	notFound := api.NewError(http.StatusNotFound, helperapi.CodeFileNotFound, map[string]any{"upload": id})
	if !helperapi.ValidUploadID(id) {
		return upload{}, notFound
	}
	var u upload
	err := m.DB.QueryRowContext(r.Context(), `SELECT id, user_id, root, dir, name, size, offset, overwrite, job FROM file_uploads
		WHERE id = ? AND expires_at > ?`, id, time.Now().Unix()).Scan(&u.id, &u.userID, &u.root, &u.dir, &u.name, &u.size, &u.offset, &u.overwrite, &u.job)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && u.userID != modutil.ActorOf(r).UserID) {
		return upload{}, notFound
	}
	return u, err
}

func (m *Module) headUpload(w http.ResponseWriter, r *http.Request) error {
	tusHeaders(w)
	u, err := m.loadUpload(r)
	if err != nil {
		return err
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.size, 10))
	if u.job != "" {
		w.Header().Set("Cairn-Job", u.job)
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func (m *Module) patchUpload(w http.ResponseWriter, r *http.Request) error {
	tusHeaders(w)
	if ct := r.Header.Get("Content-Type"); ct != "application/offset+octet-stream" {
		return api.NewError(http.StatusUnsupportedMediaType, "validation.invalid", map[string]any{"field": "Content-Type", "rule": "tus"})
	}
	unlock, err := m.uploads.Acquire(r.Context(), []string{r.PathValue("id")}, nil)
	if err != nil {
		return err
	}
	defer unlock()
	u, err := m.loadUpload(r)
	if err != nil {
		return err
	}
	// Checked again: the caller may have lost access since the upload began.
	if err := checkRoot(r, u.root); err != nil {
		return err
	}
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset != u.offset {
		return api.NewError(http.StatusConflict, "file.upload_offset", map[string]any{"offset": u.offset})
	}
	if u.job != "" || u.offset == u.size && u.size > 0 {
		return api.NewError(http.StatusConflict, "file.upload_complete", nil)
	}
	root, err := m.dataRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(partOf(u.id), os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return api.NewError(http.StatusNotFound, helperapi.CodeFileNotFound, map[string]any{"upload": u.id})
	}
	if _, err := f.Seek(u.offset, io.SeekStart); err != nil {
		_ = f.Close()
		return err
	}
	// Whatever arrived counts, even when the connection breaks.
	n, copyErr := io.Copy(f, io.LimitReader(r.Body, u.size-u.offset))
	closeErr := f.Close()
	u.offset += n
	if _, err := m.DB.ExecContext(context.WithoutCancel(r.Context()), `UPDATE file_uploads SET offset = ?, expires_at = ? WHERE id = ?`,
		u.offset, time.Now().Add(uploadTTL).Unix(), u.id); err != nil {
		return err
	}
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(u.offset, 10))
	if u.offset == u.size {
		job, err := m.place(r, u)
		if err != nil {
			return err
		}
		w.Header().Set("Cairn-Job", job.ID)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// place starts the job that has the helper move a complete upload.
func (m *Module) place(r *http.Request, u upload) (jobs.Record, error) {
	req := helperapi.FilePlaceRequest{Upload: u.id, Root: u.root, Dir: u.dir, Name: u.name, Overwrite: u.overwrite}
	who := modutil.ActorOf(r)
	params := map[string]any{"root": u.root, "path": join(u.dir, u.name), "size": u.size}
	see, manage := jobPermissions(u.root)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "file.upload", Params: params, Permission: see, ManagePermission: manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		err := j.RunOp(helperapi.ActionFilePlace, req, nil)
		m.dropUpload(context.WithoutCancel(ctx), u.id)
		return err
	})
	if err != nil {
		return job, err
	}
	_, _ = m.DB.ExecContext(r.Context(), `UPDATE file_uploads SET job = ? WHERE id = ?`, job.ID, u.id)
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, "file.upload", "ok", audit)
	return job, nil
}

// dropUpload forgets an upload and removes what is left of it.
func (m *Module) dropUpload(ctx context.Context, id string) {
	if root, err := m.dataRoot(); err == nil {
		_ = root.Remove(partOf(id))
		_ = root.Close()
	}
	_, _ = m.DB.ExecContext(ctx, `DELETE FROM file_uploads WHERE id = ?`, id)
}

func (m *Module) deleteUpload(w http.ResponseWriter, r *http.Request) error {
	tusHeaders(w)
	unlock, err := m.uploads.Acquire(r.Context(), []string{r.PathValue("id")}, nil)
	if err != nil {
		return err
	}
	defer unlock()
	u, err := m.loadUpload(r)
	if err != nil {
		return err
	}
	if u.job != "" {
		return api.NewError(http.StatusConflict, "file.upload_complete", nil)
	}
	m.dropUpload(r.Context(), u.id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Run removes expired uploads and part files nothing refers to.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(cleanupPeriod)
	defer t.Stop()
	for {
		m.cleanup(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Module) cleanup(ctx context.Context) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id FROM file_uploads WHERE expires_at <= ? AND job = ''`, time.Now().Unix())
	if err != nil {
		return
	}
	var expired []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			expired = append(expired, id)
		}
	}
	_ = rows.Close()
	for _, id := range expired {
		m.dropUpload(ctx, id)
	}
	entries, err := os.ReadDir(filepath.Join(m.DataDir, uploadsDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".part")
		if !ok || !helperapi.ValidUploadID(id) {
			continue
		}
		var n int
		if m.DB.QueryRowContext(ctx, `SELECT count(*) FROM file_uploads WHERE id = ?`, id).Scan(&n) == nil && n == 0 {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
				m.dropUpload(ctx, id)
			}
		}
	}
}

// Downloads from URLs -----------------------------------------------------------

type fetchRequest struct {
	Root      string `json:"root"`
	Dir       string `json:"dir"`
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"` // default: the URL's last part
	Overwrite bool   `json:"overwrite,omitempty"`
}

// fetchClient connects to public addresses only: a URL (or a redirect, or
// a name resolving there) must not reach this host's own services, the
// internal network or a cloud metadata service on the user's behalf. The
// check runs on every connection, after name resolution.
func fetchClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, Control: netx.DialControl(netx.Internal)}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: fetchTimeout}
}

func (m *Module) fetch(w http.ResponseWriter, r *http.Request) error {
	var req fetchRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(req.URL) > 2048 {
		return api.NewError(http.StatusBadRequest, "validation.invalid", map[string]any{"field": "url", "rule": "http_url"})
	}
	if req.Name == "" {
		req.Name = path.Base(u.Path)
	}
	target := helperapi.FileNewRequest{Root: req.Root, Dir: req.Dir, Name: req.Name}
	if err := target.Validate(); err != nil {
		return badRequest(err)
	}
	if err := checkRoot(r, req.Root); err != nil {
		return err
	}
	if err := stepUpFor(r, req.Root); err != nil {
		return err
	}
	who := modutil.ActorOf(r)
	id := newID()
	params := map[string]any{"root": req.Root, "path": join(req.Dir, req.Name), "url": u.Redacted()}
	client := m.HTTP
	if client == nil {
		client = fetchClient()
	}
	see, manage := jobPermissions(req.Root)
	job, err := m.Jobs.Start(r.Context(), jobs.Spec{
		Kind: "file.fetch", Params: params, Permission: see, ManagePermission: manage,
		Actor: jobs.Actor{UserID: who.UserID, Username: who.Username, Source: who.Source, Remote: api.ClientIP(r)},
	}, func(ctx context.Context, j *jobs.Job) error {
		defer m.dropUpload(context.WithoutCancel(ctx), id)
		if err := m.fetchURL(ctx, j, client, req.URL, id); err != nil {
			return err
		}
		return j.RunOp(helperapi.ActionFilePlace, helperapi.FilePlaceRequest{Upload: id, Root: req.Root, Dir: req.Dir, Name: req.Name,
			Overwrite: req.Overwrite}, nil)
	})
	if err != nil {
		return err
	}
	audit := map[string]any{"job": job.ID}
	for k, v := range params {
		audit[k] = v
	}
	modutil.Audit(r, m.DB, m.Log, who, "file.fetch", "ok", audit)
	return api.WriteJSON(w, http.StatusAccepted, jobResponse{Job: job})
}

// fetchURL fetches a URL into uploads/<id>.part, as the panel's own user.
func (m *Module) fetchURL(ctx context.Context, j *jobs.Job, client *http.Client, rawURL, id string) error {
	failed := func(detail string) error {
		return api.NewError(http.StatusBadGateway, "file.fetch_failed", map[string]any{"detail": detail})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return failed(err.Error())
	}
	req.Header.Set("User-Agent", "Cairn")
	res, err := client.Do(req)
	if err != nil {
		return failed(err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return failed(res.Status)
	}
	limit := min(int64(maxUpload), m.freeSpace())
	if res.ContentLength > limit {
		return api.NewError(http.StatusInsufficientStorage, helperapi.CodeFileNoSpace, map[string]any{"size": res.ContentLength})
	}
	root, err := m.dataRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(partOf(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	j.Logf("Downloading %s.\n", res.Request.URL.Redacted())
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return failed(err.Error())
	}
	if n > limit {
		return api.NewError(http.StatusInsufficientStorage, helperapi.CodeFileNoSpace, map[string]any{"size": n})
	}
	j.Logf("Downloaded %d bytes.\n", n)
	return nil
}
