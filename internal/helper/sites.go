// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"

	"github.com/Kobayashi-classmate/Cairn/catalog"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// LockWeb serializes changes to the web server and PHP-FPM configuration.
const LockWeb = "web"

// Replaceable in tests, which must not create system users.
var (
	lookupUser = func(name string) (int, int, error) {
		u, err := user.Lookup(name)
		if err != nil {
			return 0, 0, err
		}
		uid, err1 := strconv.Atoi(u.Uid)
		gid, err2 := strconv.Atoi(u.Gid)
		return uid, gid, errors.Join(err1, err2)
	}
	lookupGroup = func(name string) (int, error) {
		g, err := user.LookupGroup(name)
		if err != nil {
			return 0, err
		}
		return strconv.Atoi(g.Gid)
	}
	// processAlive reports whether the process in a pid file runs.
	processAlive = func(pidFile string) (int, bool) {
		data, err := os.ReadFile(pidFile) //nolint:gosec // G304: fixed service pid files.
		if err != nil {
			return 0, false
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 1 {
			return 0, false
		}
		return pid, syscall.Kill(pid, 0) == nil
	}
	signalProcess = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
)

// Sites manages web sites on this system: per site a directory, two users
// (the owner deploys the code; the runner runs PHP and may write only the
// directories declared writable), an nginx server block and, for PHP sites,
// a PHP-FPM pool of its own. Every change follows the same steps: write the
// files, let nginx (and php-fpm) check the whole configuration, reload, and
// put the previous files back if any step fails.
type Sites struct {
	Software *Software
	// Unsupported, when set, is why sites are not available here.
	Unsupported string
	// NginxUser is the user nginx workers run as (www-data, nginx).
	NginxUser string
	// Family is the platform family (debian, el): where nginx takes
	// module files.
	Family string
	// Root prefixes every path ("" means /; tests).
	Root string
	// PanelPort is the panel's own port, never given to apps or streams.
	PanelPort int
	// Firewall opens a stream's port while it is enabled (nil: leave the
	// firewall alone).
	Firewall *Firewall
}

// Fixed locations (Debian layout; other families come with their profiles).
const (
	sitesDir       = "/srv/www"
	nginxBinary    = "/usr/sbin/nginx"
	nginxPidFile   = "/run/nginx.pid"
	nginxConfD     = "/etc/nginx/conf.d"
	nginxCairnDir  = "/etc/nginx/cairn"
	nginxLogDir    = "/var/log/nginx/cairn"
	siteStateDir   = "/var/lib/cairn-helper/sites"
	logrotateFile  = "/etc/logrotate.d/cairn-nginx"
	siteManaged    = "Managed by Cairn. Change the site in the panel instead of editing this file."
	maxLogRead     = 256 << 10
	tailLogRead    = 64 << 10
	siteCheckLimit = 2 * time.Minute
)

// RegisterSites adds the site actions.
func RegisterSites(r *Registry, o *Ops, st *Sites) {
	web := func(any) []string { return []string{LockWeb} }
	RegisterOp(r, o, helperapi.ActionSiteApply, OpSpec[helperapi.SiteSpec]{
		Locks: func(s helperapi.SiteSpec) []string { return web(s) }, Run: st.apply})
	RegisterOp(r, o, helperapi.ActionSiteRemove, OpSpec[helperapi.SiteRemoveRequest]{
		Locks: func(s helperapi.SiteRemoveRequest) []string { return web(s) }, Run: st.remove})
	Register(r, helperapi.ActionSiteStatus, func(ctx context.Context, _ helperapi.Caller, _ helperapi.Empty) (helperapi.SiteStatus, error) {
		return st.Status(ctx), nil
	})
	Register(r, helperapi.ActionSiteLog, func(_ context.Context, _ helperapi.Caller, req helperapi.SiteLogRequest) (helperapi.SiteLogResponse, error) {
		return st.readLog(req)
	})
	Register(r, helperapi.ActionAppStatus, func(ctx context.Context, _ helperapi.Caller, req helperapi.AppRef) (helperapi.AppStatus, error) {
		return st.AppStatus(ctx, req.Code)
	})
	Register(r, helperapi.ActionAppLog, func(ctx context.Context, _ helperapi.Caller, req helperapi.AppLogRequest) (helperapi.AppLogResponse, error) {
		return st.AppLog(ctx, req)
	})
	RegisterOp(r, o, helperapi.ActionAppRestart, OpSpec[helperapi.AppRef]{
		Locks: func(s helperapi.AppRef) []string { return web(s) }, Run: st.restartApp})
	RegisterOp(r, o, helperapi.ActionStreamApply, OpSpec[helperapi.StreamSpec]{
		Locks: func(s helperapi.StreamSpec) []string { return web(s) }, Run: st.applyStream})
	RegisterOp(r, o, helperapi.ActionStreamRemove, OpSpec[helperapi.StreamRef]{
		Locks: func(s helperapi.StreamRef) []string { return web(s) }, Run: st.removeStream})
}

func (st *Sites) path(p string) string { return filepath.Join(st.Root, p) }

func siteErr(code string, params map[string]any) error {
	return &helperapi.Error{Code: code, Params: params}
}

func ownerOf(code string) string  { return "cw-" + code }
func runnerOf(code string) string { return "cw-" + code + "-run" }
func siteDirOf(code string) string {
	return path.Join(sitesDir, code)
}

// phpOffer returns the PHP line's offer when its FPM can host site pools.
func (st *Sites) phpOffer(line string) (catalog.Offer, bool) {
	sw, ok := st.Software.Catalog.Find("php")
	if !ok {
		return catalog.Offer{}, false
	}
	for _, o := range sw.Offers(st.Software.system()) {
		if o.Line == line && o.FPM != nil {
			_, err := os.Stat(st.path(o.FPM.Binary))
			return o, err == nil
		}
	}
	return catalog.Offer{}, false
}

// Status reports what sites can use on this system and what was applied.
func (st *Sites) Status(_ context.Context) helperapi.SiteStatus {
	var s helperapi.SiteStatus
	s.Supported, s.Reason = st.Unsupported == "", st.Unsupported
	s.PHP = []string{}
	s.Sites = map[string]helperapi.SiteState{}
	if _, err := os.Stat(st.path(nginxBinary)); err == nil {
		s.Nginx.Installed = true
	}
	_, s.Nginx.Running = processAlive(st.path(nginxPidFile))
	if sw, ok := st.Software.Catalog.Find("php"); ok {
		for _, o := range sw.Offers(st.Software.system()) {
			if _, ok := st.phpOffer(o.Line); ok {
				s.PHP = append(s.PHP, o.Line)
			}
		}
	}
	states, _ := st.states()
	for code, state := range states {
		s.Sites[code] = state
	}
	s.Streams, _ = st.streamStates()
	if s.Streams == nil {
		s.Streams = map[string]helperapi.StreamSpec{}
	}
	s.Systemd = systemdRunning()
	return s
}

// states loads what was applied for every site.
func (st *Sites) states() (map[string]helperapi.SiteState, error) { return siteStatesAt(st.Root) }

// siteStatesAt loads the applied sites of the system under root (prefix).
func siteStatesAt(root string) (map[string]helperapi.SiteState, error) {
	out := map[string]helperapi.SiteState{}
	entries, err := os.ReadDir(filepath.Join(root, siteStateDir))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		code, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !helperapi.ValidSiteCode(code) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, siteStateDir, e.Name())) //nolint:gosec // G304: inside the helper's state directory.
		var s helperapi.SiteState
		if err == nil && json.Unmarshal(data, &s) == nil && s.Spec.Code == code {
			out[code] = s
		}
	}
	return out, nil
}

// fileChange is one file of a configuration change; nil data removes it.
// mode 0 means 0644.
type fileChange struct {
	path string
	data []byte
	mode os.FileMode
}

// reloader reloads a service after its files changed.
type reloader struct {
	name    string // for the log and the result: nginx, php8.4-fpm
	unit    string
	pidFile string
	signal  syscall.Signal // without systemd: the reload signal
}

// configCheck validates the whole configuration of a service.
type configCheck struct {
	name string
	argv []string
}

func (st *Sites) nginxCheck() configCheck {
	return configCheck{name: "nginx", argv: []string{nginxBinary, "-t"}}
}

func (st *Sites) nginxReload() reloader {
	return reloader{name: "nginx", unit: "nginx", pidFile: nginxPidFile, signal: syscall.SIGHUP}
}

func fpmCheck(f *catalog.FPM) configCheck {
	return configCheck{name: f.Service, argv: []string{f.Binary, "-t"}}
}

func fpmReload(f *catalog.FPM) reloader {
	return reloader{name: f.Service, unit: f.Service, pidFile: f.PidFile, signal: syscall.SIGUSR2}
}

// transact writes the files, runs the checks and reloads the services; if
// a check or a reload fails, the previous files come back (and the services
// are reloaded with them).
func (st *Sites) transact(ctx context.Context, log io.Writer, changes []fileChange, checks []configCheck, reloads []reloader) ([]string, error) {
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(log, format, a...) }
	type backup struct {
		path   string
		data   []byte
		mode   os.FileMode
		exists bool
	}
	var backups []backup
	for _, c := range changes {
		p := st.path(c.path)
		data, err := os.ReadFile(p) //nolint:gosec // G304: paths built from validated site codes.
		b := backup{path: p, data: data, exists: err == nil, mode: 0o644}
		if info, err := os.Stat(p); err == nil {
			b.mode = info.Mode().Perm()
		}
		backups = append(backups, b)
	}
	restore := func() {
		for _, b := range backups {
			if b.exists {
				_ = writeFileMode(b.path, b.data, b.mode)
			} else {
				_ = os.Remove(b.path)
			}
		}
	}
	changed := false
	for i, c := range changes {
		b := backups[i]
		switch {
		case c.data == nil && !b.exists:
			continue
		case c.data != nil && b.exists && bytes.Equal(b.data, c.data):
			continue
		}
		changed = true
		if c.data == nil {
			logf("Removing %s\n", c.path)
			if err := os.Remove(b.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restore()
				return nil, err
			}
			continue
		}
		logf("Writing %s\n", c.path)
		mode := c.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileMode(b.path, c.data, mode); err != nil {
			restore()
			return nil, err
		}
	}
	if !changed {
		logf("The configuration is already up to date.\n")
		return nil, nil
	}
	for _, chk := range checks {
		var out bytes.Buffer
		cctx, cancel := context.WithTimeout(ctx, siteCheckLimit)
		err := runCommand(cctx, io.MultiWriter(log, &out), chk.argv[0], chk.argv[1:]...)
		cancel()
		if err != nil {
			restore()
			logf("The check failed; the previous configuration was put back. Nothing was changed.\n")
			return nil, siteErr(helperapi.CodeSiteConfigInvalid, map[string]any{"check": chk.name, "output": tailString(strings.TrimSpace(out.String()), 600)})
		}
	}
	var reloaded []string
	for _, r := range reloads {
		did, err := st.reload(ctx, log, r)
		if err != nil {
			restore()
			for _, again := range reloads {
				_, _ = st.reload(ctx, io.Discard, again)
			}
			logf("Reloading %s failed; the previous configuration was put back.\n", r.name)
			return nil, siteErr(helperapi.CodeSiteReloadFailed, map[string]any{"service": r.name})
		}
		if did {
			reloaded = append(reloaded, r.name)
		}
	}
	return reloaded, nil
}

// reload reloads a service through systemd, or by signal when the service
// runs without it (containers); a service that is not running is left
// alone: it reads the new files when it starts.
func (st *Sites) reload(ctx context.Context, log io.Writer, r reloader) (bool, error) {
	if systemdRunning() {
		if !st.unitActive(ctx, r.unit) {
			_, _ = fmt.Fprintf(log, "%s is not running; the change applies when it starts.\n", r.name)
			return false, nil
		}
		return true, runCommand(ctx, log, "systemctl", "reload", r.unit)
	}
	pid, ok := processAlive(st.path(r.pidFile))
	if !ok {
		_, _ = fmt.Fprintf(log, "%s is not running; the change applies when it starts.\n", r.name)
		return false, nil
	}
	_, _ = fmt.Fprintf(log, "Reloading %s (signal to %d).\n", r.name, pid)
	if err := signalProcess(pid, r.signal); err != nil {
		return false, err
	}
	time.Sleep(300 * time.Millisecond) // let it re-read before the next step
	return true, nil
}

// nginxVersion returns the installed nginx's version ("1.26.3"), or "".
var nginxVersion = func(root string) string {
	res, err := execx.Run(context.Background(), execx.Cmd{Name: filepath.Join(root, nginxBinary), Args: []string{"-v"}, Timeout: 10 * time.Second, MaxOutput: 4096})
	if err != nil {
		return ""
	}
	out := string(res.Stderr) + string(res.Stdout)
	_, v, ok := strings.Cut(out, "nginx/")
	if !ok {
		return ""
	}
	return strings.Fields(v + " ")[0]
}

// http2Directive reports whether nginx takes "http2 on;" (1.25.1 and
// later) rather than "listen ... http2", which it deprecates.
func (st *Sites) http2Directive() bool {
	parts := strings.SplitN(nginxVersion(st.Root), ".", 3)
	if len(parts) < 3 {
		return false
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(strings.TrimRightFunc(parts[2], func(r rune) bool { return r < '0' || r > '9' }))
	return major > 1 || major == 1 && (minor > 25 || minor == 25 && patch >= 1)
}

func (st *Sites) unitActive(ctx context.Context, unit string) bool {
	out, _ := queryCommand(ctx, "systemctl", "is-active", unit)
	return strings.TrimSpace(out) == "active"
}

// ensureUsers creates the site's group and two users, and lets nginx read
// the site's files.
func (st *Sites) ensureUsers(ctx context.Context, log io.Writer, code string) error {
	owner, runner, dir := ownerOf(code), runnerOf(code), siteDirOf(code)
	if _, err := lookupGroup(owner); err != nil {
		if err := runCommand(ctx, log, "groupadd", "--system", owner); err != nil {
			return err
		}
	}
	gid, err := lookupGroup(owner)
	if err != nil {
		return err
	}
	for _, u := range []string{owner, runner} {
		if _, ugid, err := lookupUser(u); err == nil {
			// A user of this name made for something else (another site,
			// or not by Cairn) is never taken over: the site's files would
			// be its files.
			if ugid != gid {
				return siteErr(helperapi.CodeSiteUserConflict, map[string]any{"user": u, "group": owner})
			}
			continue
		}
		if err := runCommand(ctx, log, "useradd", "--system", "--gid", owner, "--home-dir", dir, "--no-create-home",
			"--shell", "/usr/sbin/nologin", u); err != nil {
			return err
		}
	}
	if st.NginxUser != "" {
		if err := runCommand(ctx, log, "usermod", "--append", "--groups", owner, st.NginxUser); err != nil {
			return err
		}
	}
	return nil
}

// ensureDirs lays out the site directory:
//
//	/srv/www/<code>/        root:<group>   0750  (users cannot rename it)
//	/srv/www/<code>/app/    owner:<group>  2750  code, read-only to the runner
//	/srv/www/<code>/tmp/    runner:<group> 2770  sessions and uploads in progress
//	app/<writable>          runner:<group> 2770
//
// Existing content is not touched, and nothing follows symlinks: the owner
// controls everything under app/.
func (st *Sites) ensureDirs(log io.Writer, spec helperapi.SiteSpec) error {
	owner, runner := ownerOf(spec.Code), runnerOf(spec.Code)
	ouid, gid, err := lookupUser(owner)
	if err != nil {
		return err
	}
	ruid, _, err := lookupUser(runner)
	if err != nil {
		return err
	}
	base := st.path(siteDirOf(spec.Code))
	mk := func(p string, uid int, mode os.FileMode) error {
		info, err := os.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(p, mode.Perm()); err != nil {
				return err
			}
		case err != nil:
			return err
		case !info.IsDir():
			return fmt.Errorf("%s exists and is not a directory", p)
		}
		if err := os.Lchown(p, uid, gid); err != nil {
			return err
		}
		return os.Chmod(p, mode)
	}
	if err := os.MkdirAll(st.path(sitesDir), 0o755); err != nil { //nolint:gosec // G301: nginx traverses it.
		return err
	}
	if err := mk(base, 0, 0o750); err != nil {
		return err
	}
	app := filepath.Join(base, "app")
	_, appErr := os.Lstat(app)
	if err := mk(app, ouid, 0o750|os.ModeSetgid); err != nil {
		return err
	}
	if err := mk(filepath.Join(base, "tmp"), ruid, 0o770|os.ModeSetgid); err != nil {
		return err
	}
	// Everything in app/ is changed through it, so that no symlink planted
	// by the owner can make root change anything outside it.
	root, err := os.OpenRoot(app)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if errors.Is(appErr, fs.ErrNotExist) {
		_, _ = fmt.Fprintf(log, "Created %s\n", base)
		html := fmt.Sprintf("<!doctype html>\n<meta charset=\"utf-8\">\n<title>%s</title>\n<p>%s is ready. Put the site's files in %s.</p>\n",
			spec.Domains[0], spec.Domains[0], path.Join(siteDirOf(spec.Code), "app"))
		// The owner has had app/ for a moment: a new file only, never
		// one (or a link) put there meanwhile.
		page, err := root.OpenFile("index.html", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o640)
		switch {
		case errors.Is(err, fs.ErrExist):
		case err != nil:
			return err
		default:
			_, werr := page.Write([]byte(html))
			cerr := page.Chown(ouid, gid)
			if err := errors.Join(werr, cerr, page.Close()); err != nil {
				return err
			}
		}
	}
	// Writable directories.
	for _, w := range spec.Writable {
		// Missing parents are created for the owner, the directory itself
		// is the runner's.
		parts := strings.Split(w, "/")
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			info, err := root.Lstat(p)
			last := i == len(parts)-1
			switch {
			case errors.Is(err, fs.ErrNotExist):
				if err := root.Mkdir(p, 0o750); err != nil {
					return fmt.Errorf("writable directory %s: %w", w, err)
				}
				uid := ouid
				if last {
					uid = ruid
				}
				if err := root.Lchown(p, uid, gid); err != nil {
					return err
				}
				if err := root.Chmod(p, 0o750|os.ModeSetgid); err != nil {
					return err
				}
			case err != nil:
				return err
			case !info.IsDir():
				return fmt.Errorf("writable directory %s: %s is not a directory", w, p)
			}
			if last {
				if err := root.Lchown(p, ruid, gid); err != nil {
					return err
				}
				if err := root.Chmod(p, 0o770|os.ModeSetgid); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// site rendering ---------------------------------------------------------

// acmeLocation answers ACME HTTP-01 challenges from the shared webroot; it
// is in every plain-HTTP server block, before anything that could take the
// request.
const acmeLocation = `# ACME HTTP-01 challenges, answered from the panel's webroot.
location ^~ /.well-known/acme-challenge/ {
    root ` + acmeWebroot + `;
    default_type "text/plain";
    try_files $uri =404;
}`

var siteTemplate = template.Must(template.New("site").Funcs(template.FuncMap{"join": strings.Join}).Parse(`
{{- define "logs"}}
    server_name {{join .Domains " "}};
    access_log {{.LogDir}}/{{.Code}}.access.log cairn_json;
    error_log {{.LogDir}}/{{.Code}}.error.log warn;
{{- end}}
{{- define "body"}}
{{- if not .Enabled}}

    # The site is stopped.
    location / {
        return 503;
    }
{{- else if .Proxy}}

    location / {
        proxy_pass {{.Proxy.URL}};
        proxy_http_version 1.1;
        proxy_set_header Host {{if .Proxy.UpstreamHost}}$proxy_host{{else}}$host{{end}};
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
{{- if .Proxy.WebSocket}}
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $cairn_connection_upgrade;
        proxy_read_timeout 3600s;
{{- end}}
    }
{{- else}}
    root {{.WebRoot}};
    index {{join .Index " "}};
{{- if .AllowSymlinks}}
    # The site's links may point at files of other owners (allowSymlinks).
    disable_symlinks off;
{{- else}}
    # nginx is in every site's group: a link one site's code plants must
    # not make it serve another site's files.
    disable_symlinks if_not_owner from=$document_root;
{{- end}}

    # Hidden files (.git, .env) are never served.
    location ~ /\.(?!well-known/) {
        deny all;
    }
{{if .RewriteBody}}
    # Rewrite rule: {{.Rewrite}}
{{.RewriteBody}}
{{- else}}
    location / {
        try_files $uri $uri/ =404;
    }
{{- end}}
{{- if .Socket}}

    location ~ [^/]\.php(/|$) {
        fastcgi_split_path_info ^(.+?\.php)(/.*)$;
        try_files $fastcgi_script_name =404;
        set $path_info $fastcgi_path_info;
        include fastcgi_params;
        fastcgi_param PATH_INFO $path_info;
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        fastcgi_param DOCUMENT_ROOT $realpath_root;
        fastcgi_index index.php;
        fastcgi_pass unix:{{.Socket}};
    }
{{- end}}
{{- end}}
{{- end -}}
# {{.Managed}}
# Site {{.Code}} ({{.Type}})
server {
    listen 80;
    listen [::]:80;
{{- template "logs" .}}

{{.ACMELocation}}
{{- if and .TLS .TLS.Redirect}}

    location / {
        return 301 https://$host$request_uri;
    }
{{- else}}
{{- template "body" .}}
{{- end}}
}
{{- if .TLS}}

server {
    listen 443 ssl{{if not .HTTP2On}} http2{{end}};
    listen [::]:443 ssl{{if not .HTTP2On}} http2{{end}};
{{- if .HTTP2On}}
    http2 on;
{{- end}}
{{- template "logs" .}}
    ssl_certificate {{.CertDir}}/{{.TLS.Cert}}.crt;
    ssl_certificate_key {{.CertDir}}/{{.TLS.Cert}}.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:cairn_tls:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;
{{- if .TLS.HSTS}}
    add_header Strict-Transport-Security "max-age=31536000" always;
{{- end}}
{{- template "body" .}}
}
{{- end}}
`))

var poolTemplate = template.Must(template.New("pool").Parse(`; {{.Managed}}
; Site {{.Code}}
[cairn-{{.Code}}]
user = {{.Runner}}
group = {{.Group}}
listen = {{.Socket}}
listen.owner = {{.NginxUser}}
listen.group = {{.NginxUser}}
listen.mode = 0660
pm = ondemand
pm.max_children = 10
pm.process_idle_timeout = 60s
pm.max_requests = 500
chdir = {{.AppDir}}
catch_workers_output = yes
security.limit_extensions = .php
php_admin_value[open_basedir] = {{.SiteDir}}/
php_admin_value[upload_tmp_dir] = {{.SiteDir}}/tmp
php_admin_value[sys_temp_dir] = {{.SiteDir}}/tmp
php_admin_value[session.save_path] = {{.SiteDir}}/tmp
php_admin_value[session.gc_probability] = 1
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen,pcntl_exec
`))

const globalNginx = `# ` + siteManaged + `
# Panel-wide settings and the sites (` + nginxCairnDir + `/sites).
log_format cairn_json escape=json '{"time":"$time_iso8601","remote":"$remote_addr","host":"$host",'
    '"method":"$request_method","uri":"$request_uri","status":$status,"bytes":$body_bytes_sent,'
    '"referer":"$http_referer","agent":"$http_user_agent","duration":$request_time,'
    '"upstream":"$upstream_response_time"}';
map $http_upgrade $cairn_connection_upgrade {
    default upgrade;
    ''      close;
}
include ` + nginxCairnDir + `/sites/*.conf;
`

const logrotateConf = `# ` + siteManaged + `
` + nginxLogDir + `/*.log {
    daily
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    sharedscripts
    postrotate
        [ -s ` + nginxPidFile + ` ] && kill -USR1 "$(cat ` + nginxPidFile + `)" || true
    endscript
}
`

func indent(body string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.Trim(body, "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("    " + l + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func siteConfPath(code string) string { return path.Join(nginxCairnDir, "sites", code+".conf") }
func poolPath(f *catalog.FPM, code string) string {
	return path.Join(f.PoolDir, "cairn-"+code+".conf")
}
func socketPath(f *catalog.FPM, code string) string {
	return path.Join(f.SocketDir, "cairn-"+code+".sock")
}

// render returns the site's nginx server block and, for a PHP site, its
// pool.
func (st *Sites) render(spec helperapi.SiteSpec, fpm *catalog.FPM) ([]byte, []byte, error) {
	dir := siteDirOf(spec.Code)
	index := spec.Index
	if len(index) == 0 {
		index = []string{"index.html", "index.htm"}
		if spec.Type == helperapi.SitePHP {
			index = append([]string{"index.php"}, index...)
		}
	}
	data := map[string]any{
		"Managed": siteManaged, "Code": spec.Code, "Type": spec.Type, "Domains": spec.Domains, "LogDir": nginxLogDir,
		"Enabled": spec.Enabled, "WebRoot": path.Join(dir, "app", spec.Root), "Index": index, "Rewrite": spec.Rewrite,
		"RewriteBody": "", "Socket": "", "Proxy": nil, "TLS": spec.TLS, "HTTP2On": st.http2Directive(),
		"CertDir": nginxCertDir, "ACMELocation": indent(acmeLocation), "AllowSymlinks": spec.AllowSymlinks,
	}
	switch spec.Type {
	case helperapi.SiteProxy:
		data["Proxy"] = spec.Proxy
	case helperapi.SiteApp:
		data["Proxy"] = &helperapi.ProxySpec{URL: fmt.Sprintf("http://127.0.0.1:%d", spec.App.Port), WebSocket: spec.App.WebSocket}
	}
	if spec.Rewrite != "" {
		rule, ok := st.Software.Catalog.Rewrite(spec.Rewrite)
		if !ok || !contains(rule.Types, spec.Type) {
			return nil, nil, siteErr(helperapi.CodeSiteRewrite, map[string]any{"rewrite": spec.Rewrite})
		}
		data["RewriteBody"] = indent(rule.Body)
	}
	var pool []byte
	if spec.Type == helperapi.SitePHP {
		data["Socket"] = socketPath(fpm, spec.Code)
		var b bytes.Buffer
		if err := poolTemplate.Execute(&b, map[string]any{
			"Managed": siteManaged, "Code": spec.Code, "Runner": runnerOf(spec.Code), "Group": ownerOf(spec.Code),
			"Socket": socketPath(fpm, spec.Code), "NginxUser": st.NginxUser, "AppDir": path.Join(dir, "app"), "SiteDir": dir,
		}); err != nil {
			return nil, nil, err
		}
		pool = b.Bytes()
	}
	var b bytes.Buffer
	if err := siteTemplate.Execute(&b, data); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), pool, nil
}

// apply implements site.apply: it creates or updates a site so that the
// system matches spec.
func (st *Sites) apply(ctx context.Context, _ helperapi.Caller, spec helperapi.SiteSpec, log io.Writer) (any, error) {
	if st.Unsupported != "" {
		return nil, siteErr(helperapi.CodeSiteUnsupported, map[string]any{"reason": st.Unsupported})
	}
	if _, err := os.Stat(st.path(nginxBinary)); err != nil {
		return nil, siteErr(helperapi.CodeSiteNoNginx, nil)
	}
	states, err := st.states()
	if err != nil {
		return nil, err
	}
	if _, exists := states[spec.Code]; !exists && !helperapi.ValidNewSiteCode(spec.Code) {
		return nil, helperapi.ErrNewSiteCode()
	}
	for code, s := range states {
		if code == spec.Code {
			continue
		}
		for _, d := range s.Spec.Domains {
			if contains(spec.Domains, d) {
				return nil, siteErr(helperapi.CodeSiteDomainTaken, map[string]any{"domain": d, "site": code})
			}
		}
	}
	var fpm *catalog.FPM
	if spec.Type == helperapi.SitePHP {
		o, ok := st.phpOffer(spec.PHP)
		if !ok {
			return nil, siteErr(helperapi.CodeSitePHPMissing, map[string]any{"line": spec.PHP})
		}
		fpm = o.FPM
	}
	var certFiles []fileChange
	if spec.TLS != nil {
		info, ok := readCertInfo(st.Root, spec.TLS.Cert)
		if !ok {
			return nil, siteErr(helperapi.CodeSiteCertMissing, map[string]any{"cert": spec.TLS.Cert})
		}
		if miss := uncovered(spec.Domains, info.Domains); len(miss) > 0 {
			return nil, siteErr(helperapi.CodeSiteCertCoverage, map[string]any{"cert": spec.TLS.Cert, "domains": strings.Join(miss, " ")})
		}
		certPEM, keyPEM, err := readCertFiles(st.Root, spec.TLS.Cert)
		if err != nil {
			return nil, siteErr(helperapi.CodeSiteCertMissing, map[string]any{"cert": spec.TLS.Cert})
		}
		certFiles = []fileChange{
			{path: path.Join(nginxCertDir, spec.TLS.Cert+".crt"), data: certPEM},
			{path: path.Join(nginxCertDir, spec.TLS.Cert+".key"), data: keyPEM, mode: 0o600},
		}
	}
	if len(certFiles) > 0 {
		if err := privateDir(st.path(nginxCertDir)); err != nil {
			return nil, err
		}
	}
	if spec.Type == helperapi.SiteApp {
		if !systemdRunning() {
			return nil, siteErr(helperapi.CodeAppNoSystemd, nil)
		}
		if a := spec.App; a.Runtime != "" && (st.Software.Runtimes == nil || st.Software.Runtimes.Installed(a.Runtime, a.Line) == "") {
			return nil, siteErr(helperapi.CodeAppRuntimeMissing, map[string]any{"software": a.Runtime, "line": a.Line})
		}
		if err := st.checkAppPort(spec); err != nil {
			return nil, err
		}
	}
	conf, pool, err := st.render(spec, fpm)
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Applying site %s (%s): %s\n", spec.Code, spec.Type, strings.Join(spec.Domains, " "))
	if err := st.ensureUsers(ctx, log, spec.Code); err != nil {
		return nil, err
	}
	if err := st.ensureDirs(log, spec); err != nil {
		return nil, err
	}
	// SELinux: the directories' types and the ports nginx connects to.
	if err := st.seSite(ctx, log, spec); err != nil {
		return nil, err
	}
	if err := st.seSetPorts(ctx, log, "site:"+spec.Code, sitePorts(spec)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(st.path(nginxLogDir), 0o750); err != nil {
		return nil, err
	}
	if err := writeIfChanged(log, st.path(logrotateFile), logrotateConf); err != nil {
		return nil, err
	}

	changes := append(certFiles,
		fileChange{path: path.Join(nginxConfD, "cairn.conf"), data: []byte(globalNginx)},
		fileChange{path: siteConfPath(spec.Code), data: conf})
	checks := []configCheck{}
	reloads := []reloader{}
	// The pool of a previous PHP line goes away when the line changes.
	prev, hadPrev := states[spec.Code]
	if hadPrev && prev.Spec.Type == helperapi.SitePHP && (spec.Type != helperapi.SitePHP || prev.Spec.PHP != spec.PHP) {
		if o, ok := st.phpOffer(prev.Spec.PHP); ok {
			changes = append(changes, fileChange{path: poolPath(o.FPM, spec.Code)})
			checks = append(checks, fpmCheck(o.FPM))
			reloads = append(reloads, fpmReload(o.FPM))
		}
	}
	if fpm != nil {
		changes = append(changes, fileChange{path: poolPath(fpm, spec.Code), data: pool})
		checks = append(checks, fpmCheck(fpm))
		// The pool must listen before nginx sends requests to it.
		reloads = append([]reloader{fpmReload(fpm)}, reloads...)
	}
	appChanged := false
	if spec.Type == helperapi.SiteApp {
		files, appChecks, err := st.appChanges(spec, false)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if old, err := os.ReadFile(st.path(f.path)); err != nil || !bytes.Equal(old, f.data) { //nolint:gosec // G304: built from a validated site code.
				appChanged = true
			}
		}
		changes = append(changes, files...)
		checks = append(checks, appChecks...)
	}
	stoppedApp := false
	if spec.Type != helperapi.SiteApp && hadPrev && prev.Spec.Type == helperapi.SiteApp {
		// No longer an app: its unit stops before its files go.
		st.stopApp(ctx, log, spec.Code)
		stoppedApp = true
		files, _, _ := st.appChanges(prev.Spec, true)
		changes = append(changes, files...)
	}
	checks = append(checks, st.nginxCheck())
	reloads = append(reloads, st.nginxReload())
	reloaded, err := st.transact(ctx, log, changes, checks, reloads)
	if err != nil {
		if stoppedApp {
			// The previous files are back: so is the app.
			st.restoreApp(ctx, log, prev.Spec)
		}
		return nil, err
	}
	// The files are in place: the state says so even if the unit does not
	// start, so that the next change (or a removal) starts from them.
	state, _ := json.Marshal(helperapi.SiteState{Spec: spec, AppliedAt: time.Now().Unix()})
	if err := writePrivate(st.path(path.Join(siteStateDir, spec.Code+".json")), state); err != nil {
		return nil, err
	}
	if spec.Type == helperapi.SiteApp {
		if err := st.startApp(ctx, log, spec, appChanged); err != nil {
			return nil, err
		}
	} else if stoppedApp {
		_ = runCommand(ctx, log, "systemctl", "daemon-reload")
	}
	_, _ = fmt.Fprintf(log, "Site %s is %s.\n", spec.Code, map[bool]string{true: "serving", false: "stopped (503)"}[spec.Enabled])
	return helperapi.SiteResult{Code: spec.Code, Dir: siteDirOf(spec.Code), Owner: ownerOf(spec.Code), Runner: runnerOf(spec.Code), Reloaded: reloaded}, nil
}

// remove implements site.remove.
func (st *Sites) remove(ctx context.Context, _ helperapi.Caller, req helperapi.SiteRemoveRequest, log io.Writer) (any, error) {
	if st.Unsupported != "" {
		return nil, siteErr(helperapi.CodeSiteUnsupported, map[string]any{"reason": st.Unsupported})
	}
	states, err := st.states()
	if err != nil {
		return nil, err
	}
	prev, ok := states[req.Code]
	if !ok {
		return nil, siteErr(helperapi.CodeSiteNotFound, map[string]any{"site": req.Code})
	}
	changes := []fileChange{{path: siteConfPath(req.Code)}}
	checks := []configCheck{st.nginxCheck()}
	reloads := []reloader{st.nginxReload()}
	if prev.Spec.Type == helperapi.SiteApp {
		st.stopApp(ctx, log, req.Code)
		files, _, _ := st.appChanges(prev.Spec, true)
		changes = append(changes, files...)
	}
	if prev.Spec.Type == helperapi.SitePHP {
		if o, ok := st.phpOffer(prev.Spec.PHP); ok {
			changes = append(changes, fileChange{path: poolPath(o.FPM, req.Code)})
			checks = append(checks, fpmCheck(o.FPM))
			// nginx stops sending requests before the pool goes away.
			reloads = append(reloads, fpmReload(o.FPM))
		}
	}
	reloaded, err := st.transact(ctx, log, changes, checks, reloads)
	if err != nil {
		if prev.Spec.Type == helperapi.SiteApp {
			st.restoreApp(ctx, log, prev.Spec)
		}
		return nil, err
	}
	if prev.Spec.Type == helperapi.SiteApp && systemdRunning() {
		_ = runCommand(ctx, log, "systemctl", "daemon-reload")
	}
	if err := os.Remove(st.path(path.Join(siteStateDir, req.Code+".json"))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	st.seForget(ctx, log, req.Code)
	res := helperapi.SiteResult{Code: req.Code, Dir: siteDirOf(req.Code), Owner: ownerOf(req.Code), Runner: runnerOf(req.Code), Reloaded: reloaded}
	if !req.DeleteFiles {
		_, _ = fmt.Fprintf(log, "Site %s removed; its files and users are kept in %s.\n", req.Code, res.Dir)
		return res, nil
	}
	_, _ = fmt.Fprintf(log, "Deleting %s and the users %s, %s.\n", res.Dir, res.Owner, res.Runner)
	if err := os.RemoveAll(st.path(res.Dir)); err != nil {
		return nil, err
	}
	if st.NginxUser != "" {
		_ = runCommand(ctx, log, "gpasswd", "--delete", st.NginxUser, res.Owner)
	}
	for _, u := range []string{res.Runner, res.Owner} {
		if _, _, err := lookupUser(u); err == nil {
			if err := runCommand(ctx, log, "userdel", u); err != nil {
				return nil, err
			}
		}
	}
	if _, err := lookupGroup(res.Owner); err == nil {
		if err := runCommand(ctx, log, "groupdel", res.Owner); err != nil {
			return nil, err
		}
	}
	_ = os.Remove(st.path(path.Join(nginxLogDir, req.Code+".access.log")))
	_ = os.Remove(st.path(path.Join(nginxLogDir, req.Code+".error.log")))
	return res, nil
}

// readLog implements site.log.
func (st *Sites) readLog(req helperapi.SiteLogRequest) (helperapi.SiteLogResponse, error) {
	p := st.path(path.Join(nginxLogDir, req.Code+"."+req.Kind+".log"))
	f, err := os.Open(p) //nolint:gosec // G304: built from a validated site code and kind.
	if errors.Is(err, fs.ErrNotExist) {
		return helperapi.SiteLogResponse{}, nil
	}
	if err != nil {
		return helperapi.SiteLogResponse{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return helperapi.SiteLogResponse{}, err
	}
	size := info.Size()
	start := req.Offset
	if start < 0 {
		start = max(0, size-tailLogRead)
	}
	if start > size { // rotated
		start = 0
	}
	buf := make([]byte, min(maxLogRead, size-start))
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return helperapi.SiteLogResponse{}, err
	}
	buf = buf[:n]
	if req.Offset < 0 && start > 0 { // start at a line boundary when tailing
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf, start = buf[i+1:], start+int64(i+1)
		}
	}
	// End at a line boundary unless at the end of the file.
	if end := start + int64(len(buf)); end < size {
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			buf = buf[:i+1]
		}
	}
	return helperapi.SiteLogResponse{Data: string(buf), Offset: start + int64(len(buf)), Size: size}, nil
}

// writePrivate writes a root-only file atomically.
func writePrivate(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return writeFileAs(p, data, 0o600)
}

// privateDir makes a root-only directory (or takes an existing one back
// to root-only): where nginx's keys live, which its master reads as root.
func privateDir(p string) error {
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", p)
	}
	return os.Chmod(p, 0o700) //nolint:gosec // G302: a directory, root-only.
}

// SiteCodes returns the applied site codes, sorted (for tests and status).
func (st *Sites) SiteCodes() []string {
	states, _ := st.states()
	codes := make([]string, 0, len(states))
	for c := range states {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	return codes
}
