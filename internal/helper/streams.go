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
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/netx"
)

// Four-layer proxies use nginx's stream module. The stream block lives in
// the directory of module files that the distribution's nginx.conf
// includes at the top level, so the main configuration stays untouched:
// modules-enabled/ on Debian, /usr/share/nginx/modules on EL (read in name
// order, after mod-stream.conf). nginx.org's nginx.conf includes no such
// directory: the block goes into the panel's own, which nginx.conf then
// includes (nginxconf.go).
const (
	streamTopDebian = "/etc/nginx/modules-enabled/99-cairn-stream.conf"
	streamTopEL     = "/usr/share/nginx/modules/zz-cairn-stream.conf"
	streamTopOwn    = nginxMainDir + "/stream.conf"
	streamDir       = "/etc/nginx/cairn/stream"
	streamStateDir  = "/var/lib/cairn-helper/streams"
)

const streamTop = `# ` + siteManaged + `
# Four-layer proxies (` + streamDir + `).
stream {
    log_format cairn_stream '$remote_addr [$time_iso8601] $protocol $status $bytes_sent $bytes_received $session_time "$upstream_addr"';
    include ` + streamDir + `/*.conf;
}
`

var streamTemplate = template.Must(template.New("stream").Parse(`# {{.Managed}}
# Stream {{.Code}} ({{.Protocol}} {{.Port}})
upstream cairn_stream_{{.Code}} {
{{- if eq .Balance "hash"}}
    hash $remote_addr consistent;
{{- end}}
{{- range .Upstreams}}
    server {{.}};
{{- end}}
}

server {
    listen {{.Port}}{{if .UDP}} udp{{end}};
    listen [::]:{{.Port}}{{if .UDP}} udp{{end}};
    proxy_pass cairn_stream_{{.Code}};
    proxy_connect_timeout 10s;
    access_log {{.LogDir}}/stream-{{.Code}}.log cairn_stream;
}
`))

func streamConfPath(code string) string { return path.Join(streamDir, code+".conf") }

// hostAddrs returns this host's interface addresses (replaced in tests).
var hostAddrs = netx.HostAddrs

// streamStates loads what was applied for every stream.
func (st *Sites) streamStates() (map[string]helperapi.StreamSpec, error) {
	out := map[string]helperapi.StreamSpec{}
	entries, err := os.ReadDir(st.path(streamStateDir))
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
		data, err := os.ReadFile(st.path(path.Join(streamStateDir, e.Name()))) //nolint:gosec // G304: inside the helper's state directory.
		var s helperapi.StreamSpec
		if err == nil && json.Unmarshal(data, &s) == nil && s.Code == code {
			out[code] = s
		}
	}
	return out, nil
}

// streamModule reports whether nginx has its stream module: built in, or
// loaded by nginx.conf or a module file it includes at the top level.
func (st *Sites) streamModule() bool {
	if st.streamBuiltIn() {
		return true
	}
	globs := []string{filepath.Join(filepath.Dir(streamTopDebian), "*.conf")}
	if st.Family == "el" {
		globs = []string{filepath.Join(filepath.Dir(streamTopEL), "*.conf")}
	}
	if m, ok := st.readNginxMain(); ok {
		for _, l := range m.loads {
			if strings.Contains(l, "ngx_stream_module") {
				return true
			}
		}
		globs = m.includes
	}
	for _, g := range globs {
		if !filepath.IsAbs(g) {
			continue
		}
		files, _ := filepath.Glob(st.path(g))
		for _, f := range files {
			if data, err := os.ReadFile(f); err == nil && bytes.Contains(data, []byte("ngx_stream_module")) { //nolint:gosec // G304: nginx module files.
				return true
			}
		}
	}
	return false
}

// applyStream implements stream.apply.
func (st *Sites) applyStream(ctx context.Context, _ helperapi.Caller, spec helperapi.StreamSpec, log io.Writer) (any, error) {
	if st.Unsupported != "" {
		return nil, siteErr(helperapi.CodeSiteUnsupported, map[string]any{"reason": st.Unsupported})
	}
	if _, err := os.Stat(st.path(nginxBinary)); err != nil {
		return nil, siteErr(helperapi.CodeSiteNoNginx, nil)
	}
	if !st.streamModule() {
		return nil, siteErr(helperapi.CodeStreamModuleMissing, nil)
	}
	// The web process checked the upstreams of a caller without the
	// firewall permission; the helper does not take its word for them, and
	// sees this host's addresses as they are now.
	if spec.Enabled && !spec.AnyUpstream {
		host, err := hostAddrs()
		if err != nil {
			return nil, err
		}
		if err := helperapi.CheckUpstreams(spec.Upstreams, host); err != nil {
			return nil, err
		}
	}
	streams, err := st.streamStates()
	if err != nil {
		return nil, err
	}
	for code, s := range streams {
		if code != spec.Code && s.Port == spec.Port && s.Protocol == spec.Protocol {
			return nil, siteErr(helperapi.CodeStreamPortTaken, map[string]any{"port": spec.Port, "stream": code})
		}
	}
	if spec.Port == st.PanelPort {
		return nil, siteErr(helperapi.CodeStreamPortTaken, map[string]any{"port": spec.Port, "panel": true})
	}
	prev, hadPrev := streams[spec.Code]
	if spec.Enabled {
		if err := st.checkStreamPort(spec, prev, hadPrev); err != nil {
			return nil, err
		}
	}
	changes := st.streamTopChanges()
	if spec.Enabled {
		var b bytes.Buffer
		if err := streamTemplate.Execute(&b, map[string]any{"Managed": siteManaged, "Code": spec.Code, "Protocol": spec.Protocol,
			"Port": spec.Port, "UDP": spec.Protocol == helperapi.StreamUDP, "Upstreams": spec.Upstreams, "Balance": spec.Balance,
			"LogDir": nginxLogDir}); err != nil {
			return nil, err
		}
		changes = append(changes, fileChange{path: streamConfPath(spec.Code), data: b.Bytes()})
	} else {
		// A stopped stream does not listen at all.
		changes = append(changes, fileChange{path: streamConfPath(spec.Code)})
	}
	if err := st.ensureLogDir(); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(log, "Applying stream %s: %s %d -> %s\n", spec.Code, spec.Protocol, spec.Port, strings.Join(spec.Upstreams, " "))
	var sePorts []sePort
	if spec.Enabled {
		sePorts = streamPorts(spec)
	}
	if err := st.seSetPorts(ctx, log, "stream:"+spec.Code, sePorts); err != nil {
		return nil, err
	}
	if _, err := st.transact(ctx, log, changes, []configCheck{st.nginxCheck()}, []reloader{st.nginxReload()}); err != nil {
		// The labels follow the configuration back.
		var prevPorts []sePort
		if hadPrev && prev.Enabled {
			prevPorts = streamPorts(prev)
		}
		if serr := st.seSetPorts(ctx, io.Discard, "stream:"+spec.Code, prevPorts); serr != nil {
			_, _ = fmt.Fprintf(log, "Warning: the SELinux port labels could not be put back: %v\n", serr)
		}
		return nil, err
	}
	data, _ := json.Marshal(spec)
	if err := writePrivate(st.path(path.Join(streamStateDir, spec.Code+".json")), data); err != nil {
		return nil, err
	}
	var ports []string
	if spec.Enabled {
		ports = []string{strconv.Itoa(spec.Port)}
	}
	st.streamFirewall(ctx, log, spec.Code, spec.Protocol, ports)
	return spec, nil
}

// checkStreamPort refuses a port something else listens on: nginx would
// fail to bind it on reload and keep its old configuration from then on
// (sshd, a database, an app, nginx's own sites). The stream's own port
// while it is applied is nginx's, and fine.
func (st *Sites) checkStreamPort(spec, prev helperapi.StreamSpec, hadPrev bool) error {
	udp := spec.Protocol == helperapi.StreamUDP
	if !udp {
		states, err := st.states()
		if err != nil {
			return err
		}
		for code, s := range states {
			if s.Spec.App != nil && s.Spec.App.Port == spec.Port {
				return siteErr(helperapi.CodeStreamPortTaken, map[string]any{"port": spec.Port, "site": code})
			}
		}
	}
	if hadPrev && prev.Enabled && prev.Port == spec.Port && prev.Protocol == spec.Protocol {
		return nil
	}
	if procPorts(st.Root, udp)[spec.Port] {
		return siteErr(helperapi.CodeStreamPortTaken, map[string]any{"port": spec.Port, "listening": true})
	}
	return nil
}

// streamFirewall opens (or with nil ports closes) a stream's port.
func (st *Sites) streamFirewall(ctx context.Context, log io.Writer, code, protocol string, ports []string) {
	if st.Firewall == nil {
		return
	}
	if err := st.Firewall.SetManaged(ctx, log, helperapi.OwnerStreamPrefix+code, protocol, ports); err != nil {
		_, _ = fmt.Fprintf(log, "Warning: the firewall could not be updated: %v\n", err)
	}
}

// removeStream implements stream.remove.
func (st *Sites) removeStream(ctx context.Context, _ helperapi.Caller, req helperapi.StreamRef, log io.Writer) (any, error) {
	streams, err := st.streamStates()
	if err != nil {
		return nil, err
	}
	if _, ok := streams[req.Code]; !ok {
		return nil, siteErr(helperapi.CodeStreamNotFound, map[string]any{"stream": req.Code})
	}
	if _, err := st.transact(ctx, log, []fileChange{{path: streamConfPath(req.Code)}}, []configCheck{st.nginxCheck()}, []reloader{st.nginxReload()}); err != nil {
		return nil, err
	}
	if err := os.Remove(st.path(path.Join(streamStateDir, req.Code+".json"))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	_ = os.Remove(st.path(path.Join(nginxLogDir, "stream-"+req.Code+".log")))
	st.streamFirewall(ctx, log, req.Code, "", nil)
	if err := st.seSetPorts(ctx, log, "stream:"+req.Code, nil); err != nil {
		_, _ = fmt.Fprintf(log, "Warning: the SELinux port labels could not be removed: %v\n", err)
	}
	_, _ = fmt.Fprintf(log, "Stream %s removed.\n", req.Code)
	return req, nil
}

// monotonicNow is the time since boot (systemd's monotonic timestamps).
func monotonicNow() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0
	}
	return time.Duration(ts.Nano())
}
