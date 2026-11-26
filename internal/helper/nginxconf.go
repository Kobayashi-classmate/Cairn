// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
)

// The installed nginx may be the distribution's or nginx.org's, which lay
// out their configuration differently: Debian's nginx.conf includes the
// module files of /etc/nginx/modules-enabled at the top level and runs the
// workers as www-data, EL's includes /usr/share/nginx/modules, and
// nginx.org's includes no top-level files at all, has the stream module
// built in and runs as nginx. What the panel needs from it is read from the
// configuration and the binary each time, so that it follows a switch
// between the two (or an administrator's edit).
const (
	nginxConf = "/etc/nginx/nginx.conf"
	// Where the panel's own top-level files go when nginx.conf includes no
	// directory of them; nginx.conf then gets an include of it.
	nginxMainDir     = "/etc/nginx/cairn/main"
	nginxMainInclude = nginxMainDir + "/*.conf"
)

// nginxMain is what the top level of nginx.conf says.
type nginxMain struct {
	raw []byte
	// includes are the arguments of the top-level include directives.
	includes []string
	// user is the user of the top-level user directive, "" without one.
	user string
	// loads are the arguments of the load_module directives.
	loads []string
}

// parseNginxMain reads the directives at the top level of an nginx
// configuration: outside every block, without comments, quotes removed.
func parseNginxMain(data []byte) nginxMain {
	m := nginxMain{raw: data}
	var words []string
	var word strings.Builder
	var quote byte
	depth := 0
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				word.WriteByte(c)
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			flush()
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == ';':
			flush()
			if depth == 0 && len(words) >= 2 {
				switch words[0] {
				case "include":
					m.includes = append(m.includes, words[1])
				case "user":
					m.user = words[1]
				case "load_module":
					m.loads = append(m.loads, words[1])
				}
			}
			words = words[:0]
		case c == '{':
			flush()
			depth++
			words = words[:0]
		case c == '}':
			flush()
			if depth > 0 {
				depth--
			}
			words = words[:0]
		default:
			word.WriteByte(c)
		}
	}
	return m
}

// readNginxMain reads the installed nginx.conf; false when there is none.
func (st *Sites) readNginxMain() (nginxMain, bool) {
	data, err := os.ReadFile(st.path(nginxConf))
	if err != nil {
		return nginxMain{}, false
	}
	return parseNginxMain(data), true
}

// nginxBuildInfo returns what "nginx -V" prints (its configure arguments),
// "" when it cannot be run. Replaceable in tests, which never run nginx.
var nginxBuildInfo = func(root string) string {
	if root != "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := execx.Run(ctx, execx.Cmd{Name: nginxBinary, Args: []string{"-V"}, Timeout: 10 * time.Second, MaxOutput: 64 << 10})
	if err != nil {
		return ""
	}
	return string(res.Stdout) + string(res.Stderr)
}

// buildFlag returns the value of a configure argument of nginx -V output
// ("--user=nginx" -> "nginx"); has is true when the argument is there at
// all, with or without a value.
func buildFlag(info, name string) (value string, has bool) {
	for _, f := range strings.Fields(info) {
		if f == name {
			return "", true
		}
		if v, ok := strings.CutPrefix(f, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// nginxUser is the user nginx's workers run as: the user directive of
// nginx.conf, else the one nginx was built with, else the platform's.
func (st *Sites) nginxUser() string {
	if m, ok := st.readNginxMain(); ok && m.user != "" {
		return m.user
	}
	if u, _ := buildFlag(nginxBuildInfo(st.Root), "--user"); u != "" {
		return u
	}
	return st.NginxUser
}

// streamBuiltIn reports whether nginx was built with the stream module in
// the binary (nginx.org) rather than as a dynamic module.
func (st *Sites) streamBuiltIn() bool {
	v, has := buildFlag(nginxBuildInfo(st.Root), "--with-stream")
	return has && v == ""
}

// streamTopFile is where this nginx takes the panel's stream block: the
// module directory its nginx.conf includes at the top level, else the
// panel's own directory (own is true; nginx.conf must include it). Without
// an nginx.conf to read, the distribution's place.
func (st *Sites) streamTopFile() (file string, own bool) {
	m, ok := st.readNginxMain()
	if !ok {
		if st.Family == "el" {
			return streamTopEL, false
		}
		return streamTopDebian, false
	}
	for _, inc := range m.includes {
		switch inc {
		case filepath.Dir(streamTopDebian) + "/*.conf":
			return streamTopDebian, false
		case filepath.Dir(streamTopEL) + "/*.conf":
			return streamTopEL, false
		}
	}
	return streamTopOwn, true
}

// streamTopChanges are the file changes that put the stream block where
// this nginx reads it: the block itself, the include of the panel's own
// directory added to nginx.conf when it is the place and nginx.conf lacks
// it, and the removal of the block from the other places (left there by
// the nginx this one replaced, it would be read twice or not at all).
func (st *Sites) streamTopChanges() []fileChange {
	top, own := st.streamTopFile()
	changes := []fileChange{{path: top, data: []byte(streamTop)}}
	if m, ok := st.readNginxMain(); ok && own && !slices.Contains(m.includes, nginxMainInclude) {
		conf := bytes.TrimRight(m.raw, "\n")
		conf = append(conf, []byte("\n\n# Added by Cairn: the panel's top-level configuration (four-layer proxies).\ninclude "+nginxMainInclude+";\n")...)
		changes = append(changes, fileChange{path: nginxConf, data: conf})
	}
	for _, other := range []string{streamTopDebian, streamTopEL, streamTopOwn} {
		if other == top {
			continue
		}
		if _, err := os.Lstat(st.path(other)); err == nil {
			changes = append(changes, fileChange{path: other})
		}
	}
	return changes
}
