// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Debian's nginx.conf and nginx.org's, shortened.
const (
	debianNginxConf = `user www-data;
worker_processes auto;
pid /run/nginx.pid;
include /etc/nginx/modules-enabled/*.conf;

events {
	worker_connections 768;
}

http {
	include /etc/nginx/mime.types;
	include /etc/nginx/conf.d/*.conf;
	include /etc/nginx/sites-enabled/*;
}
`
	nginxOrgConf = `
user  nginx;
worker_processes  auto;

error_log  /var/log/nginx/error.log notice;
pid        /run/nginx.pid;

events {
    worker_connections  1024;
}

http {
    include       /etc/nginx/mime.types;
    # include "/etc/nginx/ignored.conf";
    include /etc/nginx/conf.d/*.conf;
}
`
	nginxOrgBuild = "nginx version: nginx/1.30.8\nbuilt by gcc 14.2.0\nconfigure arguments: --prefix=/etc/nginx --user=nginx --group=nginx --with-stream --with-stream_realip_module --with-stream_ssl_module\n"
)

func TestParseNginxMain(t *testing.T) {
	m := parseNginxMain([]byte(debianNginxConf))
	if m.user != "www-data" || !slices.Equal(m.includes, []string{"/etc/nginx/modules-enabled/*.conf"}) {
		t.Fatalf("debian: %+v", m)
	}
	m = parseNginxMain([]byte(nginxOrgConf + "load_module \"modules/ngx_stream_module.so\"; # loaded\n"))
	if m.user != "nginx" || len(m.includes) != 0 || !slices.Equal(m.loads, []string{"modules/ngx_stream_module.so"}) {
		t.Fatalf("nginx.org: %+v", m)
	}
	if v, has := buildFlag(nginxOrgBuild, "--user"); v != "nginx" || !has {
		t.Fatalf("user: %q %v", v, has)
	}
	if _, has := buildFlag("configure arguments: --with-stream=dynamic --with-stream_ssl_module", "--with-stream"); !has {
		t.Fatal("dynamic stream not seen")
	}
	if _, has := buildFlag("configure arguments: --with-stream_ssl_module", "--with-stream"); has {
		t.Fatal("stream seen in another flag")
	}
}

// nginx.org's nginx: the panel's stream block goes into its own directory,
// which nginx.conf then includes; the block the distribution's nginx read
// is taken away; workers run as nginx, and the platform's web user loses
// its access to the sites.
func TestNginxOrgLayout(t *testing.T) {
	e := newSiteEnv(t)
	old := nginxBuildInfo
	t.Cleanup(func() { nginxBuildInfo = old })
	build := ""
	nginxBuildInfo = func(string) string { return build }

	// The distribution's layout: the module directory, its user.
	writeSystemFile(t, e.st.Root, nginxConf, debianNginxConf)
	writeSystemFile(t, e.st.Root, "/etc/nginx/modules-enabled/50-mod-stream.conf", "load_module modules/ngx_stream_module.so;\n")
	if top, own := e.st.streamTopFile(); top != streamTopDebian || own || e.st.nginxUser() != "www-data" || !e.st.streamModule() {
		t.Fatalf("debian layout: %s %v %s", top, own, e.st.nginxUser())
	}
	tcp := helperapi.StreamSpec{Code: "db", Protocol: helperapi.StreamTCP, Port: 3307, Upstreams: []string{"10.0.0.5:3306"}, Enabled: true}
	if op, log := e.run(helperapi.ActionStreamApply, tcp); op.State != helperapi.OpSucceeded {
		t.Fatalf("apply: %+v\n%s", op.Error, log)
	}

	// nginx.org's replaces it: no module directory, the module built in.
	writeSystemFile(t, e.st.Root, nginxConf, nginxOrgConf)
	_ = os.Remove(filepath.Join(e.st.Root, "etc/nginx/modules-enabled/50-mod-stream.conf"))
	if e.st.streamModule() {
		t.Fatal("stream module seen without a build or a module file")
	}
	build = nginxOrgBuild
	if top, own := e.st.streamTopFile(); top != streamTopOwn || !own || e.st.nginxUser() != "nginx" || !e.st.streamModule() {
		t.Fatalf("nginx.org layout: %s %v %s", top, own, e.st.nginxUser())
	}
	// The refresh after the switch moves the block and adds the include
	// once.
	e.st.refreshStreamTop(context.Background(), slog.New(slog.DiscardHandler))
	conf := e.read(nginxConf)
	if !strings.HasPrefix(conf, strings.TrimRight(nginxOrgConf, "\n")) || strings.Count(conf, "include "+nginxMainInclude+";") != 1 {
		t.Fatalf("nginx.conf:\n%s", conf)
	}
	if !strings.Contains(e.read(streamTopOwn), "include /etc/nginx/cairn/stream/*.conf;") {
		t.Fatal("stream block not in the panel's directory")
	}
	if _, err := os.Stat(filepath.Join(e.st.Root, streamTopDebian)); !os.IsNotExist(err) {
		t.Fatal("the old stream block is still read")
	}
	if top, own := e.st.streamTopFile(); top != streamTopOwn || !own || len(e.st.streamTopChanges()) != 1 {
		t.Fatalf("after the refresh: %s %v %+v", top, own, e.st.streamTopChanges())
	}
	// Applying a proxy again changes nothing else.
	tcp.Port = 3308
	if op, log := e.run(helperapi.ActionStreamApply, tcp); op.State != helperapi.OpSucceeded || strings.Count(e.read(nginxConf), nginxMainInclude) != 1 {
		t.Fatalf("apply on nginx.org: %+v\n%s", op.Error, log)
	}

	// The sites' groups follow the user.
	e.f.mu.Lock()
	e.f.cmds = []string{"usermod --append --groups cw-blog www-data"}
	e.f.mu.Unlock()
	if changed, err := e.st.nginxAccess(context.Background(), &strings.Builder{}, "blog", true); err != nil || !changed ||
		!e.f.ran("gpasswd --delete www-data cw-blog") || !e.f.ran("usermod --append --groups cw-blog nginx") {
		t.Fatalf("access: %v %v %v", changed, err, e.f.cmds)
	}
}
