// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// Redis and Valkey: the system instance, reached on its local port (or
// socket) with the password from its configuration file.

// redisDial connects to the instance. Tests replace it.
var redisDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
	var dl net.Dialer
	return dl.DialContext(ctx, network, addr)
}

const (
	redisTimeout  = 30 * time.Second
	redisBulkMax  = 16 << 20
	redisItemMax  = 4 << 10
	redisSaveRDB  = "3600 1 300 100 60 10000"
	redisSaveWait = 10 * time.Minute
)

// redisError is an error reply.
type redisError string

func (e redisError) Error() string { return string(e) }

type redisConn struct {
	c net.Conn
	r *bufio.Reader
}

func (rc *redisConn) Close() error { return rc.c.Close() }

func encodeCommand(b *strings.Builder, args []string) {
	_, _ = fmt.Fprintf(b, "*%d\r\n", len(args))
	for _, a := range args {
		_, _ = fmt.Fprintf(b, "$%d\r\n%s\r\n", len(a), a)
	}
}

func (rc *redisConn) send(args ...string) error {
	var b strings.Builder
	encodeCommand(&b, args)
	_, err := io.WriteString(rc.c, b.String())
	return err
}

// do sends one command and reads its reply; an error reply is a
// redisError.
func (rc *redisConn) do(args ...string) (any, error) {
	if err := rc.send(args...); err != nil {
		return nil, err
	}
	return rc.read()
}

// pipeline sends several commands and reads every reply (error replies
// stay in the list).
func (rc *redisConn) pipeline(cmds [][]string) ([]any, error) {
	var b strings.Builder
	for _, c := range cmds {
		encodeCommand(&b, c)
	}
	// Replies are read while the commands are still being written, so
	// neither side waits on a full buffer.
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(rc.c, b.String())
		written <- err
	}()
	out, err := rc.replies(len(cmds))
	if err != nil {
		_ = rc.c.Close() // unblocks the writer
	}
	if werr := <-written; err == nil && werr != nil {
		err = werr
	}
	return out, err
}

func (rc *redisConn) replies(n int) ([]any, error) {
	out := make([]any, n)
	for i := range out {
		v, err := rc.read()
		var re redisError
		if err != nil && !errors.As(err, &re) {
			return nil, err
		}
		if err != nil {
			v = re
		}
		out[i] = v
	}
	return out, nil
}

// read parses one RESP2 reply: string, redisError, int64, nil or []any.
func (rc *redisConn) read() (any, error) {
	line, err := rc.r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, redisError(body)
	case ':':
		return strconv.ParseInt(body, 10, 64)
	case '$':
		n, err := strconv.Atoi(body)
		if err != nil || n > redisBulkMax {
			return nil, fmt.Errorf("redis: bad bulk length %q", body)
		}
		if n < 0 {
			return nil, nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(rc.r, buf); err != nil {
			return nil, err
		}
		return string(buf[:n]), nil
	case '*':
		n, err := strconv.Atoi(body)
		if err != nil || n > 1<<20 {
			return nil, fmt.Errorf("redis: bad array length %q", body)
		}
		if n < 0 {
			return nil, nil
		}
		out := make([]any, n)
		for i := range out {
			v, err := rc.read()
			var re redisError
			if err != nil && !errors.As(err, &re) {
				return nil, err
			}
			if err != nil {
				v = re
			}
			out[i] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("redis: unexpected reply %q", line[:1])
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return ""
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// redisConf is what the helper reads from the configuration file.
type redisConf struct {
	file, dataDir    string
	port             int
	socket, password string
}

func redisFiles(e dbEngine) (string, string) {
	if e.software == "valkey" {
		return "/etc/valkey/valkey.conf", "/var/lib/valkey"
	}
	return "/etc/redis/redis.conf", "/var/lib/redis"
}

// redisWord unquotes a configuration value.
func redisWord(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		inner := v[1 : len(v)-1]
		if v[0] == '"' {
			inner = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner)
		}
		return inner
	}
	return v
}

func (d *DBs) redisConf(e dbEngine) (redisConf, error) {
	file, dataDir := redisFiles(e)
	conf := redisConf{file: file, dataDir: dataDir, port: 6379}
	data, err := os.ReadFile(d.path(file)) //nolint:gosec // G304: the instance's own file.
	if err != nil {
		return conf, dbErr(helperapi.CodeDBEngineMissing, map[string]any{"engine": e.id})
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToLower(key) {
		case "port":
			conf.port, _ = strconv.Atoi(strings.TrimSpace(val))
		case "unixsocket":
			conf.socket = redisWord(val)
		case "requirepass":
			conf.password = redisWord(val)
		}
	}
	return conf, nil
}

// redisConnect opens an authenticated connection.
func (d *DBs) redisConnect(ctx context.Context, e dbEngine) (*redisConn, redisConf, error) {
	conf, err := d.redisConf(e)
	if err != nil {
		return nil, conf, err
	}
	network, addr := "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(conf.port))
	if conf.port == 0 && conf.socket != "" {
		network, addr = "unix", d.path(conf.socket)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := redisDial(ctx, network, addr)
	if err != nil {
		return nil, conf, dbErr(helperapi.CodeDBNotRunning, map[string]any{"engine": e.id})
	}
	_ = c.SetDeadline(time.Now().Add(redisTimeout))
	rc := &redisConn{c: c, r: bufio.NewReader(c)}
	if conf.password != "" {
		if _, err := rc.do("AUTH", conf.password); err != nil {
			_ = rc.Close()
			return nil, conf, dbErr(helperapi.CodeDBAccessDenied, map[string]any{"engine": e.id, "detail": err.Error()})
		}
	}
	return rc, conf, nil
}

func redisCmdErr(err error) error {
	var re redisError
	if errors.As(err, &re) {
		return dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": string(re)})
	}
	return err
}

// infoField reads a field of an INFO reply.
func infoField(info, name string) string {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+":"); ok {
			return v
		}
	}
	return ""
}

// configValue is the value of a CONFIG GET reply.
func configValue(v any) string {
	if l := asList(v); len(l) >= 2 {
		return asString(l[1])
	}
	return ""
}

func (d *DBs) redisStatus(ctx context.Context, e dbEngine, info *helperapi.DBEngineInfo) error {
	rc, conf, err := d.redisConnect(ctx, e)
	if err != nil {
		if asError(err).Code == helperapi.CodeDBNotRunning {
			info.Port = conf.port
			return nil
		}
		return err
	}
	defer func() { _ = rc.Close() }()
	info.Running, info.Port = true, conf.port
	r, err := rc.pipeline([][]string{{"INFO", "server"}, {"INFO", "memory"}, {"INFO", "keyspace"},
		{"CONFIG", "GET", "maxmemory"}, {"CONFIG", "GET", "maxmemory-policy"}, {"CONFIG", "GET", "save"},
		{"CONFIG", "GET", "appendonly"}, {"CONFIG", "GET", "bind"}})
	if err != nil {
		return err
	}
	for _, v := range r {
		if re, ok := v.(redisError); ok {
			return dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": string(re)})
		}
	}
	server := asString(r[0])
	if v := infoField(server, "valkey_version"); v != "" {
		info.Version = v
	} else {
		info.Version = infoField(server, "redis_version")
	}
	ri := &helperapi.RedisInfo{PasswordSet: conf.password != ""}
	ri.UsedMemory, _ = strconv.ParseInt(infoField(asString(r[1]), "used_memory"), 10, 64)
	for _, line := range strings.Split(asString(r[2]), "\n") {
		if _, stats, ok := strings.Cut(strings.TrimSpace(line), ":keys="); ok && strings.HasPrefix(line, "db") {
			n, _ := strconv.ParseInt(strings.SplitN(stats, ",", 2)[0], 10, 64)
			ri.Keys += n
		}
	}
	maxBytes, _ := strconv.ParseInt(configValue(r[3]), 10, 64)
	ri.MaxMemoryMB = int(maxBytes >> 20)
	ri.MaxMemoryPolicy = configValue(r[4])
	rdb, aof := configValue(r[5]) != "", configValue(r[6]) == "yes"
	switch {
	case rdb && aof:
		ri.Persistence = "both"
	case rdb:
		ri.Persistence = "rdb"
	case aof:
		ri.Persistence = "aof"
	default:
		ri.Persistence = "none"
	}
	bind := strings.Fields(configValue(r[7]))
	info.Remote = len(bind) == 0
	for _, a := range bind {
		if !isLoopback(a) {
			info.Remote = true
		}
	}
	info.Redis = ri
	return nil
}

func (d *DBs) redisEngine(ctx context.Context) (dbEngine, error) {
	return d.engine(ctx, helperapi.DBRedis)
}

func (d *DBs) redisConfig(ctx context.Context, _ helperapi.Caller, req helperapi.RedisConfigRequest, log io.Writer) (any, error) {
	e, err := d.redisEngine(ctx)
	if err != nil {
		return nil, err
	}
	rc, conf, err := d.redisConnect(ctx, e)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	set := func(name, value string) error {
		shown := value
		if name == "requirepass" {
			shown = "********"
		}
		_, _ = fmt.Fprintf(log, "CONFIG SET %s %q\n", name, shown)
		_, err := rc.do("CONFIG", "SET", name, value)
		return redisCmdErr(err)
	}
	if req.Password != nil && *req.Password == "" {
		// Without a password the instance must not be reachable remotely.
		if bind, err := rc.do("CONFIG", "GET", "bind"); err == nil {
			for _, a := range strings.Fields(configValue(bind)) {
				if !isLoopback(a) {
					return nil, dbErr(helperapi.CodeRedisNeedsPass, nil)
				}
			}
		}
	}
	if req.MaxMemoryMB != nil {
		if err := set("maxmemory", strconv.Itoa(*req.MaxMemoryMB)+"mb"); err != nil {
			return nil, err
		}
	}
	if req.MaxMemoryPolicy != nil {
		if err := set("maxmemory-policy", *req.MaxMemoryPolicy); err != nil {
			return nil, err
		}
	}
	if req.Persistence != nil {
		save, aof := "", "no"
		switch *req.Persistence {
		case "rdb":
			save = redisSaveRDB
		case "aof":
			aof = "yes"
		case "both":
			save, aof = redisSaveRDB, "yes"
		}
		if err := set("save", save); err != nil {
			return nil, err
		}
		if err := set("appendonly", aof); err != nil {
			return nil, err
		}
	}
	if req.Password != nil {
		if err := set("requirepass", *req.Password); err != nil {
			return nil, err
		}
	}
	_, _ = fmt.Fprintln(log, "CONFIG REWRITE")
	if _, err := rc.do("CONFIG", "REWRITE"); err != nil {
		// The helper finds the password in the file: the instance must not
		// keep one the file does not have.
		if req.Password != nil {
			_, _ = rc.do("CONFIG", "SET", "requirepass", conf.password)
		}
		return nil, redisCmdErr(err)
	}
	return nil, nil
}

// redisRemote makes the instance listen on every address or locally
// only: at run time where the server allows it (7.0 and later), else in
// its configuration file and with a restart.
func (d *DBs) redisRemote(ctx context.Context, log io.Writer, e dbEngine, enabled bool) error {
	rc, conf, err := d.redisConnect(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	if enabled && conf.password == "" {
		return dbErr(helperapi.CodeRedisNeedsPass, nil)
	}
	bind := "127.0.0.1 -::1"
	if enabled {
		bind = "* -::*"
	}
	_, _ = fmt.Fprintf(log, "CONFIG SET bind %q\n", bind)
	if _, err := rc.do("CONFIG", "SET", "bind", bind); err == nil {
		_, _ = fmt.Fprintln(log, "CONFIG REWRITE")
		_, err := rc.do("CONFIG", "REWRITE")
		return redisCmdErr(err)
	}
	if !systemdRunning() {
		return dbErr(helperapi.CodeDBSystemd, map[string]any{"engine": e.id})
	}
	_, _ = fmt.Fprintln(log, "this server cannot change bind at run time; restarting with a new configuration")
	bind = "127.0.0.1"
	if enabled {
		bind = "0.0.0.0"
	}
	data, err := os.ReadFile(d.path(conf.file)) //nolint:gosec // G304: the instance's own file.
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		if key, _, _ := strings.Cut(strings.TrimSpace(line), " "); strings.EqualFold(key, "bind") {
			if replaced {
				lines[i] = "# " + line
				continue
			}
			lines[i], replaced = "bind "+bind, true
		}
	}
	if !replaced {
		lines = append(lines, "bind "+bind)
	}
	return d.reconfigure(ctx, log, e, conf.file, []byte(strings.Join(lines, "\n")),
		func() error { return d.systemctl(ctx, log, e, helperapi.ServiceRestart) })
}

// redisSelect connects and selects a database for the key browser.
func (d *DBs) redisSelect(ctx context.Context, db int) (*redisConn, error) {
	e, err := d.redisEngine(ctx)
	if err != nil {
		return nil, err
	}
	rc, _, err := d.redisConnect(ctx, e)
	if err != nil {
		return nil, err
	}
	if _, err := rc.do("SELECT", strconv.Itoa(db)); err != nil {
		_ = rc.Close()
		return nil, redisCmdErr(err)
	}
	return rc, nil
}

func (d *DBs) redisScan(ctx context.Context, _ helperapi.Caller, req helperapi.RedisScanRequest) (helperapi.RedisScanResponse, error) {
	rc, err := d.redisSelect(ctx, req.DB)
	if err != nil {
		return helperapi.RedisScanResponse{}, err
	}
	defer func() { _ = rc.Close() }()
	cursor, pattern, count := req.Cursor, req.Pattern, req.Count
	if cursor == "" {
		cursor = "0"
	}
	if pattern == "" {
		pattern = "*"
	}
	if count == 0 {
		count = 100
	}
	out := helperapi.RedisScanResponse{Keys: []helperapi.RedisKey{}}
	var keys []string
	// SCAN may return few keys per call; collect up to count.
	for {
		v, err := rc.do("SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(count))
		if err != nil {
			return out, redisCmdErr(err)
		}
		l := asList(v)
		if len(l) != 2 {
			return out, errors.New("redis: bad SCAN reply")
		}
		cursor = asString(l[0])
		for _, k := range asList(l[1]) {
			keys = append(keys, asString(k))
		}
		if cursor == "0" || len(keys) >= count {
			break
		}
	}
	out.Cursor = cursor
	cmds := make([][]string, 0, 2*len(keys))
	for _, k := range keys {
		cmds = append(cmds, []string{"TYPE", k}, []string{"TTL", k})
	}
	r, err := rc.pipeline(cmds)
	if err != nil {
		return out, err
	}
	for i, k := range keys {
		out.Keys = append(out.Keys, helperapi.RedisKey{Key: k, Type: asString(r[2*i]), TTL: asInt(r[2*i+1])})
	}
	return out, nil
}

func clip(s string) string {
	if len(s) > redisItemMax {
		return s[:redisItemMax]
	}
	return s
}

func (d *DBs) redisGet(ctx context.Context, _ helperapi.Caller, req helperapi.RedisKeyRequest) (helperapi.RedisValue, error) {
	rc, err := d.redisSelect(ctx, req.DB)
	if err != nil {
		return helperapi.RedisValue{}, err
	}
	defer func() { _ = rc.Close() }()
	r, err := rc.pipeline([][]string{{"TYPE", req.Key}, {"TTL", req.Key}})
	if err != nil {
		return helperapi.RedisValue{}, err
	}
	v := helperapi.RedisValue{Key: req.Key, Type: asString(r[0]), TTL: asInt(r[1])}
	k := req.Key
	maxItems := strconv.Itoa(helperapi.RedisItemsMax - 1)
	var items []any
	switch v.Type {
	case "none":
		return v, dbErr(helperapi.CodeRedisNoKey, map[string]any{"key": req.Key})
	case "string":
		r, err := rc.pipeline([][]string{{"STRLEN", k}, {"GETRANGE", k, "0", strconv.Itoa(helperapi.RedisValueMax - 1)}})
		if err != nil {
			return v, err
		}
		v.Length, v.Value = asInt(r[0]), asString(r[1])
		v.Truncated = v.Length > int64(len(v.Value))
		return v, nil
	case "list":
		r, err := rc.pipeline([][]string{{"LLEN", k}, {"LRANGE", k, "0", maxItems}})
		if err != nil {
			return v, err
		}
		v.Length, items = asInt(r[0]), asList(r[1])
	case "zset":
		r, err := rc.pipeline([][]string{{"ZCARD", k}, {"ZRANGE", k, "0", maxItems, "WITHSCORES"}})
		if err != nil {
			return v, err
		}
		v.Length, items = asInt(r[0]), asList(r[1])
	case "set", "hash":
		lenCmd, scanCmd := "SCARD", "SSCAN"
		if v.Type == "hash" {
			lenCmd, scanCmd = "HLEN", "HSCAN"
		}
		n, err := rc.do(lenCmd, k)
		if err != nil {
			return v, redisCmdErr(err)
		}
		v.Length = asInt(n)
		limit := helperapi.RedisItemsMax
		if v.Type == "hash" {
			limit *= 2 // field and value
		}
		for cursor := "0"; ; {
			r, err := rc.do(scanCmd, k, cursor, "COUNT", "100")
			if err != nil {
				return v, redisCmdErr(err)
			}
			l := asList(r)
			if len(l) != 2 {
				return v, errors.New("redis: bad scan reply")
			}
			items = append(items, asList(l[1])...)
			if cursor = asString(l[0]); cursor == "0" || len(items) >= limit {
				break
			}
		}
		if len(items) > limit {
			items = items[:limit]
		}
	case "stream":
		r, err := rc.pipeline([][]string{{"XLEN", k}, {"XREVRANGE", k, "+", "-", "COUNT", "100"}})
		if err != nil {
			return v, err
		}
		v.Length = asInt(r[0])
		for _, entry := range asList(r[1]) {
			l := asList(entry)
			if len(l) != 2 {
				continue
			}
			var b strings.Builder
			b.WriteString(asString(l[0]))
			fields := asList(l[1])
			for i := 0; i+1 < len(fields); i += 2 {
				_, _ = fmt.Fprintf(&b, " %s=%s", asString(fields[i]), asString(fields[i+1]))
			}
			items = append(items, b.String())
		}
	default:
		return v, nil
	}
	v.Items = make([]string, 0, len(items))
	for _, it := range items {
		s := asString(it)
		if len(s) > redisItemMax {
			v.Truncated = true
		}
		v.Items = append(v.Items, clip(s))
	}
	count := int64(len(v.Items))
	if v.Type == "hash" || v.Type == "zset" {
		count /= 2
	}
	if count < v.Length {
		v.Truncated = true
	}
	return v, nil
}

func (d *DBs) redisDelete(ctx context.Context, _ helperapi.Caller, req helperapi.RedisDeleteRequest) (helperapi.RedisDeleteResponse, error) {
	rc, err := d.redisSelect(ctx, req.DB)
	if err != nil {
		return helperapi.RedisDeleteResponse{}, err
	}
	defer func() { _ = rc.Close() }()
	v, err := rc.do(append([]string{"UNLINK"}, req.Keys...)...)
	if err != nil {
		return helperapi.RedisDeleteResponse{}, redisCmdErr(err)
	}
	return helperapi.RedisDeleteResponse{Deleted: asInt(v)}, nil
}

func (d *DBs) redisTTL(ctx context.Context, _ helperapi.Caller, req helperapi.RedisTTLRequest) (helperapi.Empty, error) {
	rc, err := d.redisSelect(ctx, req.DB)
	if err != nil {
		return helperapi.Empty{}, err
	}
	defer func() { _ = rc.Close() }()
	var v any
	if req.Seconds == 0 {
		v, err = rc.do("PERSIST", req.Key)
	} else {
		v, err = rc.do("EXPIRE", req.Key, strconv.FormatInt(req.Seconds, 10))
	}
	if err != nil {
		return helperapi.Empty{}, redisCmdErr(err)
	}
	if req.Seconds != 0 && asInt(v) == 0 {
		return helperapi.Empty{}, dbErr(helperapi.CodeRedisNoKey, map[string]any{"key": req.Key})
	}
	return helperapi.Empty{}, nil
}

var rdbName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}\.rdb$`)

// redisDump takes a snapshot (BGSAVE) and copies it to the dumps. The
// file is read only from the package's own data directory, whatever the
// instance's configuration says.
func (d *DBs) redisDump(ctx context.Context, log io.Writer, e dbEngine) (helperapi.DumpInfo, error) {
	rc, conf, err := d.redisConnect(ctx, e)
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	defer func() { _ = rc.Close() }()
	r, err := rc.pipeline([][]string{{"CONFIG", "GET", "dir"}, {"CONFIG", "GET", "dbfilename"}, {"INFO", "persistence"}})
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	dir, name := filepath.Clean(configValue(r[0])), configValue(r[1])
	if dir != conf.dataDir || !rdbName.MatchString(name) {
		return helperapi.DumpInfo{}, dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": "snapshot location " + path.Join(dir, name) + " is not the package's"})
	}
	before := infoField(asString(r[2]), "rdb_last_save_time")
	_, _ = fmt.Fprintln(log, "BGSAVE")
	if _, err := rc.do("BGSAVE"); err != nil && !strings.Contains(err.Error(), "in progress") {
		return helperapi.DumpInfo{}, redisCmdErr(err)
	}
	deadline := time.Now().Add(redisSaveWait)
	for {
		_ = rc.c.SetDeadline(time.Now().Add(redisTimeout))
		v, err := rc.do("INFO", "persistence")
		if err != nil {
			return helperapi.DumpInfo{}, redisCmdErr(err)
		}
		info := asString(v)
		if infoField(info, "rdb_bgsave_in_progress") == "0" && infoField(info, "rdb_last_save_time") != before {
			if s := infoField(info, "rdb_last_bgsave_status"); s != "ok" {
				return helperapi.DumpInfo{}, dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": "BGSAVE " + s})
			}
			break
		}
		if time.Now().After(deadline) {
			return helperapi.DumpInfo{}, dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": "BGSAVE did not finish"})
		}
		select {
		case <-ctx.Done():
			return helperapi.DumpInfo{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	src := d.path(path.Join(conf.dataDir, name))
	fi, err := os.Lstat(src)
	if err != nil || !fi.Mode().IsRegular() {
		return helperapi.DumpInfo{}, dbErr(helperapi.CodeRedisCommand, map[string]any{"detail": "snapshot file missing"})
	}
	f, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: the package's data directory, checked above.
	if err != nil {
		return helperapi.DumpInfo{}, err
	}
	defer func() { _ = f.Close() }()
	return d.writeDump(e.id, "", ".rdb", false, func(w io.Writer) error {
		_, err := io.Copy(w, f)
		return err
	})
}
