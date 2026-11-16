// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
}
