// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func (c *client) tus(method, path string, body []byte, headers map[string]string) (int, http.Header) {
	c.e.t.Helper()
	req, _ := http.NewRequest(method, c.e.srv.URL+path, bytes.NewReader(body))
	req.Header.Set(api.CSRFHeader, "1")
	req.Header.Set("Tus-Resumable", "1.0.0")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return res.StatusCode, res.Header
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestFilesModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{{ID: "sites", Path: "/srv/www", Available: true}}})
	e.helper.respond(helperapi.ActionFileArchiving, helperapi.Archivers{SevenZip: true, RAR: true})
	r := admin.expect(admin.do("GET", "/api/v1/files/roots", nil), 200, "")
	if r.obj("archivers")["sevenZip"] != true || len(r.body["roots"].([]any)) != 1 {
		t.Fatalf("roots %s", r.raw)
	}
	admin.expect(admin.do("GET", "/api/v1/files/list?root=sites&path=../etc", nil), 400, "validation.invalid")
	admin.expect(admin.do("GET", "/api/v1/files/list?root=nowhere", nil), 400, "validation.invalid")
	e.helper.mu.Lock()
	e.helper.errs = map[string]error{helperapi.ActionFileList: &helperapi.Error{Code: helperapi.CodeFileNotFound}}
	e.helper.mu.Unlock()
	admin.expect(admin.do("GET", "/api/v1/files/list?root=sites&path=gone", nil), 404, helperapi.CodeFileNotFound)

	// Downloads come from the descriptor the helper hands over.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.helper.respond(helperapi.ActionFileOpen, helperapi.FileEntry{Name: "logo.svg", Size: 6, ModTime: time.Now().Unix()})
	e.helper.giveFile(helperapi.ActionFileOpen, func() (*os.File, error) { return os.Open(filepath.Join(dir, "logo.svg")) })
	res, body := admin.raw("GET", "/api/v1/files/download?root=sites&path=shop/app/logo.svg&inline=true", nil)
	if res.StatusCode != 200 || string(body) != "<svg/>" || res.Header.Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("inline: %d %q %v", res.StatusCode, body, res.Header)
	}
	if res, _ := admin.raw("GET", "/api/v1/files/download?root=sites&path=shop/app/logo.svg", nil); res.Header.Get("Content-Type") != "application/octet-stream" ||
		res.Header.Get("Content-Disposition") != `attachment; filename=logo.svg` {
		t.Fatalf("attachment: %v", res.Header)
	}

	// Permanent deletion and owners need a second verification.
	e.opsSucceed(helperapi.ActionFileDelete, helperapi.ActionFileCopy, helperapi.ActionFileChown, helperapi.ActionFilePlace)
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/files/delete", map[string]any{"root": "sites", "paths": []string{"shop/app/old"}}), 202, ""))
	admin.expect(admin.do("POST", "/api/v1/files/delete", map[string]any{"root": "sites", "paths": []string{"shop/app/old"}, "permanent": true}), 403, "auth.step_up_required")
	admin.expect(admin.do("POST", "/api/v1/files/chown", map[string]any{"root": "sites", "paths": []string{"shop/app"}, "user": "www-data"}), 403, "auth.step_up_required")
	admin.elevate("")
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/files/delete", map[string]any{"root": "sites", "paths": []string{"shop/app/old"}, "permanent": true}), 202, ""))
	if q := sentAs[helperapi.FileDeleteRequest](t, e, helperapi.ActionFileDelete); !q.Permanent || q.Paths[0] != "shop/app/old" {
		t.Fatalf("delete %+v", q)
	}
	admin.expect(admin.do("POST", "/api/v1/files/copy", map[string]any{"root": "sites", "paths": []string{}, "dest": "x"}), 400, "validation.invalid")

	// A resumable upload: created, sent in two parts, then placed.
	meta := "root " + b64("sites") + ",dir " + b64("shop/app") + ",filename " + b64("big.bin")
	status, h := admin.tus("POST", "/api/v1/files/uploads", nil, map[string]string{"Upload-Length": "10", "Upload-Metadata": meta})
	loc := h.Get("Location")
	if status != 201 || !strings.HasPrefix(loc, "/api/v1/files/uploads/") {
		t.Fatalf("create: %d %v", status, h)
	}
	if status, _ := admin.tus("POST", "/api/v1/files/uploads", nil, map[string]string{"Upload-Length": "10",
		"Upload-Metadata": "root " + b64("sites") + ",dir " + b64("..") + ",filename " + b64("x")}); status != 400 {
		t.Fatalf("upload to ..: %d", status)
	}
	patch := map[string]string{"Content-Type": "application/offset+octet-stream", "Upload-Offset": "0"}
	if status, h := admin.tus("PATCH", loc, []byte("01234"), patch); status != 204 || h.Get("Upload-Offset") != "5" {
		t.Fatalf("first part: %d %v", status, h)
	}
	if status, _ := admin.tus("PATCH", loc, []byte("56789"), patch); status != 409 {
		t.Fatalf("wrong offset: %d", status)
	}
	if status, h := admin.tus("HEAD", loc, nil, nil); status != 200 || h.Get("Upload-Offset") != "5" || h.Get("Upload-Length") != "10" {
		t.Fatalf("head: %d %v", status, h)
	}
	id := strings.TrimPrefix(loc, "/api/v1/files/uploads/")
	if data, _ := os.ReadFile(filepath.Join(e.dataDir, "uploads", id+".part")); string(data) != "01234" {
		t.Fatalf("part file %q", data)
	}
	patch["Upload-Offset"] = "5"
	status, h = admin.tus("PATCH", loc, []byte("56789"), patch)
	if status != 204 || h.Get("Cairn-Job") == "" {
		t.Fatalf("last part: %d %v", status, h)
	}
	if final, log := waitJob(t, admin, h.Get("Cairn-Job")); final["state"] != "succeeded" {
		t.Fatalf("place job %v\n%s", final, log)
	}
	if q := sentAs[helperapi.FilePlaceRequest](t, e, helperapi.ActionFilePlace); q.Upload != id || q.Name != "big.bin" || q.Dir != "shop/app" {
		t.Fatalf("place %+v", q)
	}
	// An empty file is complete as soon as it is created.
	status, h = admin.tus("POST", "/api/v1/files/uploads", nil, map[string]string{"Upload-Length": "0",
		"Upload-Metadata": "root " + b64("sites") + ",dir " + b64("shop/app") + ",filename " + b64("empty.txt")})
	if status != 201 || h.Get("Cairn-Job") == "" {
		t.Fatalf("empty upload: %d %v", status, h)
	}
	if final, _ := waitJob(t, admin, h.Get("Cairn-Job")); final["state"] != "succeeded" {
		t.Fatalf("empty upload job %v", final)
	}
	if status, _ := admin.tus("HEAD", loc, nil, nil); status != 404 {
		t.Fatalf("finished upload still there: %d", status)
	}

	// Fetching a URL: the panel downloads it, the helper places it.
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "remote file") }))
	defer src.Close()
	admin.expect(admin.do("POST", "/api/v1/files/fetch", map[string]any{"root": "sites", "dir": "shop/app", "url": "file:///etc/passwd"}), 400, "validation.invalid")
	// The real client connects to public addresses only, also when a URL
	// redirects: the test servers are on loopback.
	redirect := httptest.NewServer(http.RedirectHandler(src.URL+"/dl/readme.txt", http.StatusFound))
	defer redirect.Close()
	for _, u := range []string{src.URL + "/dl/readme.txt", redirect.URL + "/x"} {
		job := admin.expect(admin.do("POST", "/api/v1/files/fetch", map[string]any{"root": "sites", "dir": "shop/app", "url": u}), 202, "").obj("job")
		final, _ := waitJob(t, admin, job["id"].(string))
		errInfo, _ := final["error"].(map[string]any)
		params, _ := errInfo["params"].(map[string]any)
		if detail, _ := params["detail"].(string); final["state"] != "failed" || !strings.Contains(detail, "not allowed") {
			t.Fatalf("fetch from loopback %v", final)
		}
	}
	e.files.HTTP = src.Client()
	admin.runJob(admin.expect(admin.do("POST", "/api/v1/files/fetch", map[string]any{"root": "sites", "dir": "shop/app", "url": src.URL + "/dl/readme.txt"}), 202, ""))
	if q := sentAs[helperapi.FilePlaceRequest](t, e, helperapi.ActionFilePlace); q.Name != "readme.txt" || q.Upload == id {
		t.Fatalf("fetch place %+v", q)
	}

	admin.expect(admin.do("POST", "/api/v1/files/favorites", map[string]any{"root": "sites", "path": "shop/app"}), 204, "")
	if f := admin.expect(admin.do("GET", "/api/v1/files/favorites", nil), 200, "").body["favorites"].([]any); len(f) != 1 {
		t.Fatalf("favorites %v", f)
	}
	admin.expect(admin.do("DELETE", "/api/v1/files/favorites?root=sites&path=shop/app", nil), 204, "")

	// Full-disk access: administrators, with a second verification.
	e.helper.respond(helperapi.ActionAccessSet, helperapi.AccessSettings{FullDisk: true})
	admin.expect(admin.do("PUT", "/api/v1/files/settings", map[string]any{"fullDisk": true}), 200, "")
	if q := sentAs[helperapi.AccessSettings](t, e, helperapi.ActionAccessSet); !q.FullDisk {
		t.Fatalf("access %+v", q)
	}
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	op.expect(op.do("PUT", "/api/v1/files/settings", map[string]any{"fullDisk": true}), 403, "auth.forbidden")
	// Uploads are their owner's.
	if status, _ := op.tus("HEAD", loc, nil, nil); status != 404 {
		t.Fatalf("someone else's upload: %d", status)
	}

	// Only the sites root is open to operators: the backups root holds every
	// dump and backup, the system root is the whole disk.
	e.helper.respond(helperapi.ActionFileRoots, helperapi.FileRoots{Roots: []helperapi.FileRoot{
		{ID: "sites", Path: "/srv/www", Available: true}, {ID: "backups", Path: "/var/lib/cairn/backups", Available: true}, {ID: "system", Path: "/", Available: true}}})
	if roots := op.expect(op.do("GET", "/api/v1/files/roots", nil), 200, "").body["roots"].([]any); len(roots) != 1 {
		t.Fatalf("operator's roots %v", roots)
	}
	if roots := admin.expect(admin.do("GET", "/api/v1/files/roots", nil), 200, "").body["roots"].([]any); len(roots) != 3 {
		t.Fatalf("administrator's roots %v", roots)
	}
	e.helper.mu.Lock()
	e.helper.errs = nil
	e.helper.mu.Unlock()
	for _, root := range []string{"backups", "system"} {
		for _, path := range []string{"/api/v1/files/list?root=", "/api/v1/files/stat?path=x&root=", "/api/v1/files/content?path=x&root=",
			"/api/v1/files/download?path=x&root=", "/api/v1/files/search?q=sql&root=", "/api/v1/files/trash?root="} {
			op.expect(op.do("GET", path+root, nil), 403, "auth.forbidden")
		}
		for _, c := range []struct {
			method, path string
			body         map[string]any
		}{
			{"PUT", "/api/v1/files/content", map[string]any{"root": root, "path": "x", "content": "y"}},
			{"POST", "/api/v1/files/mkdir", map[string]any{"root": root, "dir": "", "name": "x"}},
			{"POST", "/api/v1/files/copy", map[string]any{"root": root, "paths": []string{"a"}, "dest": "b"}},
			{"POST", "/api/v1/files/extract", map[string]any{"root": root, "path": "a.zip", "dest": "b"}},
			{"POST", "/api/v1/files/trash/purge", map[string]any{"root": root, "olderThan": 1}},
			{"POST", "/api/v1/files/fetch", map[string]any{"root": root, "dir": "", "url": "https://example.com/x"}},
			{"POST", "/api/v1/files/favorites", map[string]any{"root": root, "path": ""}},
		} {
			op.expect(op.do(c.method, c.path, c.body), 403, "auth.forbidden")
		}
		if status, _ := op.tus("POST", "/api/v1/files/uploads", nil, map[string]string{"Upload-Length": "1",
			"Upload-Metadata": "root " + b64(root) + ",dir " + b64("") + ",filename " + b64("x")}); status != 403 {
			t.Fatalf("operator upload into %s: %d", root, status)
		}
	}
	// Administrators download from there after a second verification only.
	adm := e.client()
	adm.expect(adm.login("admin", pw), 200, "")
	adm.expect(adm.do("GET", "/api/v1/files/download?root=backups&path=db/shop.sql.zst", nil), 403, "auth.step_up_required")
	adm.expect(adm.do("GET", "/api/v1/files/content?root=system&path=etc/shadow", nil), 403, "auth.step_up_required")
	adm.expect(adm.do("GET", "/api/v1/files/search?root=system&q=secret&content=true", nil), 403, "auth.step_up_required")
	adm.expect(adm.do("GET", "/api/v1/files/search?root=system&q=secret", nil), 200, "")
	adm.expect(adm.do("GET", "/api/v1/files/list?root=backups", nil), 200, "")
	adm.elevate("")
	adm.expect(adm.do("GET", "/api/v1/files/content?root=system&path=etc/shadow", nil), 200, "")
	adm.expect(adm.do("GET", "/api/v1/files/search?root=system&q=secret&content=true", nil), 200, "")
	if res, _ := adm.raw("GET", "/api/v1/files/download?root=backups&path=db/shop.sql.zst", nil); res.StatusCode != 200 {
		t.Fatalf("elevated download: %d", res.StatusCode)
	}
}

func TestTerminalModule(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{RootTerminal: true, Users: []helperapi.TermUser{
		{Name: "cw-shop", Kind: "site", Home: "/srv/www/shop", Site: "shop"}, {Name: "root", Kind: "root", Home: "/root"}}})
	if u := admin.expect(admin.do("GET", "/api/v1/terminal/users", nil), 200, "").body["users"].([]any); len(u) != 2 {
		t.Fatalf("admin users %v", u)
	}
	// The other end of the socket pair plays the shell.
	var shell *os.File
	e.helper.respond(helperapi.ActionTermOpen, helperapi.TermSession{PID: 42, User: "cw-shop", Shell: "/bin/bash", Home: "/srv/www/shop"})
	e.helper.giveFile(helperapi.ActionTermOpen, func() (*os.File, error) {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		_ = syscall.SetNonblock(fds[0], true)
		shell = os.NewFile(uintptr(fds[1]), "shell")
		return os.NewFile(uintptr(fds[0]), "master"), nil
	})
	open := map[string]any{"user": "cw-shop", "cols": 80, "rows": 24}
	admin.expect(admin.do("POST", "/api/v1/terminal/sessions", open), 403, "auth.step_up_required")
	admin.elevate("")
	sess := admin.expect(admin.do("POST", "/api/v1/terminal/sessions", open), 201, "")
	id := sess.body["id"].(string)
	defer shell.Close()

	dial := func() *websocket.Conn {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		url := strings.Replace(e.srv.URL, "https://", "wss://", 1) + "/api/v1/terminal/sessions/" + id + "/ws"
		c, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: admin.hc})
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := dial()
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if n, err := shell.Read(buf); err != nil || string(buf[:n]) != "ls\n" {
		t.Fatalf("input reached the shell as %q, %v", buf[:n], err)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	if _, err := shell.WriteString("index.php\r\n"); err != nil {
		t.Fatal(err)
	}
	if typ, data, err := c.Read(ctx); err != nil || typ != websocket.MessageBinary || string(data) != "index.php\r\n" {
		t.Fatalf("output %v %q %v", typ, data, err)
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	// A reload attaches again and gets the recent output first.
	c = dial()
	if _, data, err := c.Read(ctx); err != nil || !strings.Contains(string(data), "index.php") {
		t.Fatalf("replay %q %v", data, err)
	}
	if l := admin.expect(admin.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any); len(l) != 1 {
		t.Fatalf("sessions %v", l)
	}
	// The shell ends: the browser is told.
	_ = shell.Close()
	if typ, data, err := c.Read(ctx); err != nil || typ != websocket.MessageText || !strings.Contains(string(data), "exit") {
		t.Fatalf("end %v %q %v", typ, data, err)
	}

	// Root terminals are for administrators.
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	op.elevate("")
	users := op.expect(op.do("GET", "/api/v1/terminal/users", nil), 200, "").body["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("operator users %v", users)
	}
	op.expect(op.do("POST", "/api/v1/terminal/sessions", map[string]any{"user": "root", "cols": 80, "rows": 24}), 403, "auth.forbidden")
	// Someone else's session is not found.
	op.expect(op.do("DELETE", "/api/v1/terminal/sessions/"+id, nil), 404, "terminal.not_found")
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'terminal.open'`).Scan(&n)
	if n != 1 {
		t.Fatalf("terminal audits: %d", n)
	}

	// Ordinary users can often sudo (the default user of cloud images):
	// operators open terminals as site owners only.
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{RootTerminal: true, Users: []helperapi.TermUser{
		{Name: "cw-shop", Kind: "site", Home: "/srv/www/shop", Site: "shop"}, {Name: "debian", Kind: "user", Home: "/home/debian"},
		{Name: "root", Kind: "root", Home: "/root"}}})
	if users := op.expect(op.do("GET", "/api/v1/terminal/users", nil), 200, "").body["users"].([]any); len(users) != 1 {
		t.Fatalf("operator users %v", users)
	}
	op.expect(op.do("POST", "/api/v1/terminal/sessions", map[string]any{"user": "debian", "cols": 80, "rows": 24}), 403, "auth.forbidden")
	op.expect(op.do("POST", "/api/v1/terminal/sessions", map[string]any{"user": "nobody", "cols": 80, "rows": 24}), 403, "auth.forbidden")
	opSess := op.expect(op.do("POST", "/api/v1/terminal/sessions", open), 201, "").body["id"].(string)
	admin.expect(admin.do("POST", "/api/v1/terminal/sessions", map[string]any{"user": "debian", "cols": 80, "rows": 24}), 201, "")

	// Changing an account ends its terminals: a shell is checked only when
	// it is opened and attached.
	accounts := admin.expect(admin.do("GET", "/api/v1/users", nil), 200, "").body["users"].([]any)
	var opID float64
	for _, u := range accounts {
		if u.(map[string]any)["username"] == "oper" {
			opID = u.(map[string]any)["id"].(float64)
		}
	}
	admin.expect(admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", int64(opID)), map[string]any{"role": "auditor"}), 200, "")
	admin.expect(admin.do("PATCH", fmt.Sprintf("/api/v1/users/%d", int64(opID)), map[string]any{"role": "operator"}), 200, "")
	if l := op.expect(op.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any); len(l) != 0 {
		t.Fatalf("sessions after a role change %v", l)
	}
	op.expect(op.do("DELETE", "/api/v1/terminal/sessions/"+opSess, nil), 404, "terminal.not_found")
	// Deleting it too, and its file manager rows go with it (the next
	// account may get the same ID).
	op.elevate("")
	op.expect(op.do("POST", "/api/v1/terminal/sessions", open), 201, "")
	op.expect(op.do("POST", "/api/v1/files/favorites", map[string]any{"root": "sites", "path": "shop"}), 204, "")
	admin.expect(admin.do("DELETE", fmt.Sprintf("/api/v1/users/%d", int64(opID)), nil), 204, "")
	for _, table := range []string{"file_favorites", "file_uploads"} {
		var left int
		_ = e.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE user_id = ?`, int64(opID)).Scan(&left)
		if left != 0 {
			t.Fatalf("%s rows survived the account: %d", table, left)
		}
	}
	admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "next", "password": pw, "role": "operator"}), 201, "")
	next := e.client()
	next.expect(next.login("next", pw), 200, "")
	if l := next.expect(next.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any); len(l) != 0 {
		t.Fatalf("a new account inherited terminals %v", l)
	}
}

// Every change on the backups and system roots takes a second verification,
// as reading there does: on the system root a write is root, and a copy or
// move into a site would carry out what a read guards. One verification
// covers them all while it lasts; the sites root asks for none.
func TestFilesChangesOutsideSitesNeedStepUp(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.opsSucceed(helperapi.ActionFileCopy, helperapi.ActionFileMove, helperapi.ActionFileDelete, helperapi.ActionFileChmod,
		helperapi.ActionFileCompress, helperapi.ActionFileExtract, helperapi.ActionTrashRestore, helperapi.ActionFilePlace)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "remote file") }))
	defer src.Close()
	e.files.HTTP = src.Client()

	type change struct {
		method, path string
		body         map[string]any
		action       string
		status       int
	}
	changes := func(root, dir string) []change {
		return []change{
			{"PUT", "/api/v1/files/content", map[string]any{"root": root, "path": dir + "/x.conf", "content": "y\n"}, helperapi.ActionFileWrite, 200},
			{"POST", "/api/v1/files/mkdir", map[string]any{"root": root, "dir": dir, "name": "x.d"}, helperapi.ActionFileMkdir, 200},
			{"POST", "/api/v1/files/touch", map[string]any{"root": root, "dir": dir, "name": "x"}, helperapi.ActionFileTouch, 200},
			{"POST", "/api/v1/files/rename", map[string]any{"root": root, "path": dir + "/a", "name": "b"}, helperapi.ActionFileRename, 200},
			{"POST", "/api/v1/files/copy", map[string]any{"root": root, "paths": []string{dir + "/a"}, "dest": dir + "/b"}, helperapi.ActionFileCopy, 202},
			{"POST", "/api/v1/files/move", map[string]any{"root": root, "paths": []string{dir + "/a"}, "dest": dir + "/b"}, helperapi.ActionFileMove, 202},
			{"POST", "/api/v1/files/delete", map[string]any{"root": root, "paths": []string{dir + "/a"}}, helperapi.ActionFileDelete, 202},
			{"POST", "/api/v1/files/chmod", map[string]any{"root": root, "paths": []string{dir + "/a"}, "mode": 420}, helperapi.ActionFileChmod, 202},
			{"POST", "/api/v1/files/compress", map[string]any{"root": root, "dir": dir, "names": []string{"a"}, "format": "zip", "name": "a.zip"},
				helperapi.ActionFileCompress, 202},
			{"POST", "/api/v1/files/extract", map[string]any{"root": root, "path": dir + "/a.zip", "dest": dir}, helperapi.ActionFileExtract, 202},
			{"POST", "/api/v1/files/trash/restore", map[string]any{"root": root, "ids": []string{"0123456789abcdef"}}, helperapi.ActionTrashRestore, 202},
			{"POST", "/api/v1/files/fetch", map[string]any{"root": root, "dir": dir, "url": src.URL + "/readme.txt"}, "", 202},
		}
	}
	upload := func(c *client, token, root, dir string) int {
		headers := map[string]string{"Upload-Length": "1", "Upload-Metadata": "root " + b64(root) + ",dir " + b64(dir) + ",filename " + b64("x")}
		if token == "" {
			status, _ := c.tus("POST", "/api/v1/files/uploads", nil, headers)
			return status
		}
		tc := e.client()
		tc.hc.Jar = nil
		headers["Authorization"] = "Bearer " + token
		status, _ := tc.tus("POST", "/api/v1/files/uploads", nil, headers)
		return status
	}

	// Signed in, not verified: refused before the helper hears of it. That
	// includes copying from the system root into a site.
	cross := change{"POST", "/api/v1/files/copy", map[string]any{"root": "system", "paths": []string{"etc/shadow"}, "dest": "srv/www/shop"},
		helperapi.ActionFileCopy, 202}
	for _, root := range []string{"system", "backups"} {
		for _, c := range append(changes(root, "etc"), cross) {
			before := len(e.helper.callsOf())
			admin.expect(admin.do(c.method, c.path, c.body), 403, "auth.step_up_required")
			if calls := e.helper.callsOf()[before:]; len(calls) != 0 {
				t.Fatalf("%s %s on %s reached the helper: %v", c.method, c.path, root, calls)
			}
		}
		if status := upload(admin, "", root, "etc"); status != 403 {
			t.Fatalf("unverified upload into %s: %d", root, status)
		}
	}

	// The sites root is as before: no verification, also for a copy or a
	// move within it. (The upload goes first: it makes the uploads
	// directory, whose free space a fetch checks.)
	if status := upload(admin, "", "sites", "shop/app"); status != 201 {
		t.Fatalf("upload into sites: %d", status)
	}
	for _, c := range changes("sites", "shop/app") {
		r := admin.expect(admin.do(c.method, c.path, c.body), c.status, "")
		if c.status == 202 {
			admin.runJob(r)
		}
	}

	// API tokens cannot verify, so they lose these changes; they still list.
	admin.elevate("")
	tok := admin.expect(admin.do("POST", "/api/v1/me/tokens", map[string]any{"name": "files", "expiresInDays": 1,
		"permissions": []string{"files.read", "files.manage", "files.admin"}}), 201, "").body["token"].(string)
	for _, root := range []string{"system", "backups"} {
		for _, c := range append(changes(root, "etc"), cross) {
			if r := e.bearer(tok, c.method, c.path, c.body); r.status != 403 || r.code() != "auth.step_up_required" {
				t.Fatalf("token %s %s on %s: %d %s", c.method, c.path, root, r.status, r.raw)
			}
		}
		if status := upload(nil, tok, root, "etc"); status != 403 {
			t.Fatalf("token upload into %s: %d", root, status)
		}
		if r := e.bearer(tok, "GET", "/api/v1/files/list?root="+root, nil); r.status != 200 {
			t.Fatalf("token list of %s: %d %s", root, r.status, r.raw)
		}
	}
	if r := e.bearer(tok, "PUT", "/api/v1/files/content", map[string]any{"root": "sites", "path": "shop/x", "content": "y"}); r.status != 200 {
		t.Fatalf("token save in sites: %d %s", r.status, r.raw)
	}

	// Verified once: every change goes through while it lasts.
	for _, root := range []string{"system", "backups"} {
		for _, c := range append(changes(root, "etc"), cross) {
			r := admin.expect(admin.do(c.method, c.path, c.body), c.status, "")
			if c.status == 202 {
				admin.runJob(r)
			}
			if c.action != "" {
				if q := sentAs[map[string]any](t, e, c.action); q["root"] != c.body["root"] {
					t.Fatalf("%s sent %v", c.action, q)
				}
			}
		}
		if status := upload(admin, "", root, "etc"); status != 201 {
			t.Fatalf("verified upload into %s: %d", root, status)
		}
	}
}

func TestFavoritesLimit(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	// Concurrent requests cannot pass the limit together.
	var wg sync.WaitGroup
	statuses := make([]int, 140)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i] = admin.do("POST", "/api/v1/files/favorites", map[string]any{"root": "sites", "path": fmt.Sprintf("d%d", i)}).status
		}()
	}
	wg.Wait()
	added := 0
	for _, st := range statuses {
		switch st {
		case 204:
			added++
		case 409:
		default:
			t.Fatalf("status %d", st)
		}
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM file_favorites`).Scan(&n)
	if added != 100 || n != 100 {
		t.Fatalf("%d added, %d stored, want 100", added, n)
	}
	admin.expect(admin.do("POST", "/api/v1/files/favorites", map[string]any{"root": "sites", "path": "one-more"}), 409, "file.too_many_favorites")
	// One already there is fine.
	var existing string
	_ = e.db.QueryRow(`SELECT path FROM file_favorites LIMIT 1`).Scan(&existing)
	admin.expect(admin.do("POST", "/api/v1/files/favorites", map[string]any{"root": "sites", "path": existing}), 204, "")
}

// A terminal ends with the browser session that opened it: signed out,
// revoked from another browser, or dropped by a password change (also by
// the cairn command, which the sweep covers).
func TestTerminalEndsWithItsSession(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	e.helper.respond(helperapi.ActionTermUsers, helperapi.TermUsers{Users: []helperapi.TermUser{{Name: "cw-shop", Kind: "site", Home: "/srv/www/shop", Site: "shop"}}})
	e.helper.respond(helperapi.ActionTermOpen, helperapi.TermSession{PID: 42, User: "cw-shop", Shell: "/bin/bash", Home: "/srv/www/shop"})
	var shells []*os.File
	defer func() {
		for _, f := range shells {
			_ = f.Close()
		}
	}()
	e.helper.giveFile(helperapi.ActionTermOpen, func() (*os.File, error) {
		fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
		if err != nil {
			return nil, err
		}
		_ = syscall.SetNonblock(fds[0], true)
		shells = append(shells, os.NewFile(uintptr(fds[1]), "shell"))
		return os.NewFile(uintptr(fds[0]), "master"), nil
	})
	open := func(c *client) string {
		c.elevate("")
		return c.expect(c.do("POST", "/api/v1/terminal/sessions", map[string]any{"user": "cw-shop", "cols": 80, "rows": 24}), 201, "").body["id"].(string)
	}
	laptop := e.client()
	laptop.expect(laptop.login("admin", pw), 200, "")
	id := open(laptop)
	// Revoked from another browser: attaching to it finds it gone.
	admin.expect(admin.do("DELETE", "/api/v1/me/sessions", nil), 200, "")
	admin.expect(admin.do("GET", "/api/v1/terminal/sessions/"+id+"/ws", nil), 404, "terminal.not_found")
	// The sweep ends the rest, also those nobody attaches to again.
	laptop = e.client()
	laptop.expect(laptop.login("admin", pw), 200, "")
	open(laptop)
	mine := open(admin)
	if l := admin.expect(admin.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any); len(l) != 2 {
		t.Fatalf("sessions %v", l)
	}
	admin.expect(admin.do("PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": pw}), 204, "")
	e.terminal.Sweep(context.Background())
	l := admin.expect(admin.do("GET", "/api/v1/terminal/sessions", nil), 200, "").body["sessions"].([]any)
	if len(l) != 1 || l[0].(map[string]any)["id"] != mine {
		t.Fatalf("after the password change %v", l)
	}
}
