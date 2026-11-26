// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// fakeRedis answers the commands the helper sends, from memory.
type fakeRedis struct {
	mu       sync.Mutex
	password string
	config   map[string]string
	strs     map[string]string
	hashes   map[string]map[string]string
	ttl      map[string]int64
	cmds     []string
	// oldBind refuses CONFIG SET bind, like Redis 6.
	oldBind bool
	// failGet makes CONFIG GET of this parameter fail.
	failGet  string
	saves    int
	dataFile string
	// stream, when set, answers a command itself (writing to the
	// connection) when it returns true: for replies too large to build.
	stream func(w io.Writer, args []string) bool
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{
		config: map[string]string{"maxmemory": "0", "maxmemory-policy": "noeviction", "save": "3600 1", "appendonly": "no",
			"bind": "127.0.0.1 -::1", "dir": "/var/lib/redis", "dbfilename": "dump.rdb"},
		strs:   map[string]string{"user:1": "alice", "user:2": strings.Repeat("x", helperapi.RedisValueMax+10)},
		hashes: map[string]map[string]string{"session:9": {"id": "9", "who": "alice"}},
		ttl:    map[string]int64{"user:1": 60},
	}
}

func (f *fakeRedis) use(t *testing.T, e *dbEnv) {
	old := redisDial
	t.Cleanup(func() { redisDial = old })
	redisDial = func(_ context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != "127.0.0.1:6379" {
			return nil, fmt.Errorf("dial %s %s", network, addr)
		}
		server, client := net.Pipe()
		go f.serve(server, e)
		return client, nil
	}
}

func (f *fakeRedis) serve(c net.Conn, e *dbEnv) {
	defer func() { _ = c.Close() }()
	rc := &redisConn{c: c, r: bufio.NewReader(c)}
	authed := f.password == ""
	for {
		v, err := rc.read()
		if err != nil {
			return
		}
		var args []string
		for _, a := range asList(v) {
			args = append(args, asString(a))
		}
		if f.stream != nil && f.stream(c, args) {
			continue
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, strings.Join(args, " "))
		var reply string
		if !authed && !strings.EqualFold(args[0], "AUTH") {
			reply = "-NOAUTH Authentication required.\r\n"
		} else {
			reply = f.answer(args, &authed, e)
		}
		f.mu.Unlock()
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

func bulk(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func array(items ...string) string {
	return fmt.Sprintf("*%d\r\n%s", len(items), strings.Join(items, ""))
}

func bulks(items ...string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = bulk(s)
	}
	return array(out...)
}

func (f *fakeRedis) typeOf(k string) string {
	if _, ok := f.strs[k]; ok {
		return "string"
	}
	if _, ok := f.hashes[k]; ok {
		return "hash"
	}
	return "none"
}

func (f *fakeRedis) answer(args []string, authed *bool, e *dbEnv) string {
	switch strings.ToUpper(args[0]) {
	case "AUTH":
		if args[1] != f.password {
			return "-WRONGPASS invalid username-password pair\r\n"
		}
		*authed = true
		return "+OK\r\n"
	case "SELECT":
		return "+OK\r\n"
	case "INFO":
		switch args[1] {
		case "server":
			return bulk("# Server\r\nredis_version:8.0.2\r\n")
		case "memory":
			return bulk("used_memory:1048576\r\n")
		case "keyspace":
			return bulk(fmt.Sprintf("# Keyspace\r\ndb0:keys=%d,expires=1,avg_ttl=0\r\n", len(f.strs)+len(f.hashes)))
		case "persistence":
			return bulk(fmt.Sprintf("rdb_bgsave_in_progress:0\r\nrdb_last_save_time:%d\r\nrdb_last_bgsave_status:ok\r\n", 1000+f.saves))
		}
	case "CONFIG":
		switch strings.ToUpper(args[1]) {
		case "GET":
			if args[2] == f.failGet {
				return "-ERR unsupported CONFIG parameter\r\n"
			}
			return bulks(args[2], f.config[args[2]])
		case "SET":
			if args[2] == "bind" && f.oldBind {
				return "-ERR CONFIG SET failed (possibly related to argument 'bind') - can't set immutable config\r\n"
			}
			f.config[args[2]] = args[3]
			if mb, ok := strings.CutSuffix(args[3], "mb"); ok && args[2] == "maxmemory" {
				n, _ := strconv.Atoi(mb)
				f.config[args[2]] = strconv.Itoa(n << 20) // the server reports bytes
			}
			if args[2] == "requirepass" {
				f.password = args[3]
			}
			return "+OK\r\n"
		case "REWRITE":
			conf := ""
			if f.password != "" {
				conf += "requirepass " + f.password + "\n"
			}
			conf += "bind " + f.config["bind"] + "\nport 6379\n"
			writeSystemFile(e.t, e.d.Root, "/etc/redis/redis.conf", conf)
			return "+OK\r\n"
		}
	case "SCAN":
		var keys []string
		for k := range f.strs {
			keys = append(keys, k)
		}
		for k := range f.hashes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			if ok, _ := path.Match(args[3], k); ok {
				out = append(out, k)
			}
		}
		return array(bulk("0"), bulks(out...))
	case "TYPE":
		return "+" + f.typeOf(args[1]) + "\r\n"
	case "TTL":
		if f.typeOf(args[1]) == "none" {
			return ":-2\r\n"
		}
		if t, ok := f.ttl[args[1]]; ok {
			return ":" + strconv.FormatInt(t, 10) + "\r\n"
		}
		return ":-1\r\n"
	case "STRLEN":
		return fmt.Sprintf(":%d\r\n", len(f.strs[args[1]]))
	case "GETRANGE":
		end, _ := strconv.Atoi(args[3])
		s := f.strs[args[1]]
		if end+1 < len(s) {
			s = s[:end+1]
		}
		return bulk(s)
	case "HLEN":
		return fmt.Sprintf(":%d\r\n", len(f.hashes[args[1]]))
	case "HSCAN":
		var items []string
		var fields []string
		for k := range f.hashes[args[1]] {
			fields = append(fields, k)
		}
		sort.Strings(fields)
		for _, k := range fields {
			items = append(items, k, f.hashes[args[1]][k])
		}
		return array(bulk("0"), bulks(items...))
	case "UNLINK":
		n := 0
		for _, k := range args[1:] {
			if f.typeOf(k) != "none" {
				n++
			}
			delete(f.strs, k)
			delete(f.hashes, k)
		}
		return fmt.Sprintf(":%d\r\n", n)
	case "EXPIRE":
		if f.typeOf(args[1]) == "none" {
			return ":0\r\n"
		}
		f.ttl[args[1]], _ = strconv.ParseInt(args[2], 10, 64)
		return ":1\r\n"
	case "PERSIST":
		delete(f.ttl, args[1])
		return ":1\r\n"
	case "BGSAVE":
		f.saves++
		writeSystemFile(e.t, e.d.Root, f.dataFile, "REDIS0011-snapshot")
		return "+Background saving started\r\n"
	}
	return "-ERR unknown command\r\n"
}

func newRedisEnv(t *testing.T) (*dbEnv, *fakeRedis) {
	e := newDBEnv(t, map[string]string{"redis-server": "5:8.0.2-3"})
	writeSystemFile(t, e.d.Root, "/etc/redis/redis.conf", "bind 127.0.0.1 -::1\nprotected-mode yes\nport 6379\n")
	f := newFakeRedis()
	f.dataFile = "/var/lib/redis/dump.rdb"
	f.use(t, e)
	return e, f
}

func TestRedisStatusAndConfig(t *testing.T) {
	e, f := newRedisEnv(t)
	s := e.status()
	if len(s.Engines) != 1 {
		t.Fatalf("engines %+v", s.Engines)
	}
	r := s.Engines[0]
	if r.ID != "redis" || r.Service != "redis-server" || !r.Running || r.Remote || r.Version != "8.0.2" || r.Redis == nil ||
		r.Redis.PasswordSet || r.Redis.Keys != 3 || r.Redis.Persistence != "rdb" || r.Redis.MaxMemoryPolicy != "noeviction" {
		t.Fatalf("engine %+v %+v", r, r.Redis)
	}

	// Remote access needs a password first.
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "redis", Enabled: true}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeRedisNeedsPass {
		t.Fatalf("remote without password: %+v", op)
	}
	pw, mem, aof := "S3cret-pass", 256, "both"
	op, log := e.run(helperapi.ActionRedisConfig, helperapi.RedisConfigRequest{Password: &pw, MaxMemoryMB: &mem, Persistence: &aof})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("config: %+v\n%s", op, log)
	}
	if f.config["maxmemory"] != "268435456" || f.config["appendonly"] != "yes" || f.config["save"] != redisSaveRDB || f.password != pw {
		t.Fatalf("config %+v", f.config)
	}
	if strings.Contains(log, pw) {
		t.Fatalf("the log shows the password:\n%s", log)
	}
	// The helper now reads the password from the rewritten file.
	if r := e.status().Engines[0]; r.Error != nil || !r.Redis.PasswordSet || r.Redis.MaxMemoryMB != 256 || r.Redis.Persistence != "both" {
		t.Fatalf("after config %+v %+v", r, r.Redis)
	}
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "redis", Enabled: true}); op.State != helperapi.OpSucceeded || f.config["bind"] != "* -::*" {
		t.Fatalf("remote: %+v %q\n%s", op, f.config["bind"], log)
	}
	if !e.status().Engines[0].Remote {
		t.Fatal("remote not reported")
	}
	// Taking the password away while reachable remotely is refused.
	empty := ""
	if op, _ := e.run(helperapi.ActionRedisConfig, helperapi.RedisConfigRequest{Password: &empty}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeRedisNeedsPass {
		t.Fatalf("password removal: %+v", op)
	}

	// Redis 6 cannot change bind at run time: the file changes and the
	// service restarts, which needs systemd.
	f.oldBind = true
	if op, _ := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "redis", Enabled: false}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBSystemd {
		t.Fatalf("old server without systemd: %+v", op)
	}
	e.f.systemd = true
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "redis", Enabled: false}); op.State != helperapi.OpSucceeded || !e.f.ran("systemctl restart redis-server") {
		t.Fatalf("old server: %+v\n%s", op, log)
	}
	if conf, _ := os.ReadFile(filepath.Join(e.d.Root, "/etc/redis/redis.conf")); !strings.Contains(string(conf), "\nbind 127.0.0.1\n") {
		t.Fatalf("config file %q", conf)
	}
}

func TestRedisPasswordRemoval(t *testing.T) {
	e, f := newRedisEnv(t)
	empty := ""
	// Bound to loopback addresses only, the instance may go without one.
	if op, log := e.run(helperapi.ActionRedisConfig, helperapi.RedisConfigRequest{Password: &empty}); op.State != helperapi.OpSucceeded {
		t.Fatalf("removal on loopback: %+v\n%s", op, log)
	}
	// No bind is every interface, as the status says; an unknown bind may
	// be one too.
	for _, tc := range []struct{ bind, failGet string }{{"", ""}, {"127.0.0.1", "bind"}} {
		f.mu.Lock()
		f.config["bind"], f.failGet = tc.bind, tc.failGet
		f.mu.Unlock()
		if op, _ := e.run(helperapi.ActionRedisConfig, helperapi.RedisConfigRequest{Password: &empty}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeRedisNeedsPass {
			t.Fatalf("removal with bind %q (failing %q): %+v", tc.bind, tc.failGet, op)
		}
	}
}

func TestRedisKeyBrowser(t *testing.T) {
	e, f := newRedisEnv(t)
	scan, err := call[helperapi.RedisScanResponse](t, e.r, helperapi.ActionRedisScan, helperapi.RedisScanRequest{Pattern: "user:*"})
	if err != nil || scan.Cursor != "0" || len(scan.Keys) != 2 || scan.Keys[0] != (helperapi.RedisKey{Key: "user:1", Type: "string", TTL: 60}) {
		t.Fatalf("scan %+v %v", scan, err)
	}
	v, err := call[helperapi.RedisValue](t, e.r, helperapi.ActionRedisGet, helperapi.RedisKeyRequest{Key: "user:2"})
	if err != nil || !v.Truncated || len(v.Value) != helperapi.RedisValueMax || v.Length != helperapi.RedisValueMax+10 {
		t.Fatalf("big string: len %d truncated %v %v", len(v.Value), v.Truncated, err)
	}
	v, err = call[helperapi.RedisValue](t, e.r, helperapi.ActionRedisGet, helperapi.RedisKeyRequest{Key: "session:9"})
	if err != nil || v.Type != "hash" || strings.Join(v.Items, ",") != "id,9,who,alice" || v.Truncated || v.Length != 2 {
		t.Fatalf("hash %+v %v", v, err)
	}
	if _, err := call[helperapi.RedisValue](t, e.r, helperapi.ActionRedisGet, helperapi.RedisKeyRequest{Key: "gone"}); err == nil || asError(err).Code != helperapi.CodeRedisNoKey {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := call[helperapi.Empty](t, e.r, helperapi.ActionRedisSetTTL, helperapi.RedisTTLRequest{Key: "session:9", Seconds: 300}); err != nil || f.ttl["session:9"] != 300 {
		t.Fatalf("ttl %v %v", f.ttl, err)
	}
	del, err := call[helperapi.RedisDeleteResponse](t, e.r, helperapi.ActionRedisDelete, helperapi.RedisDeleteRequest{Keys: []string{"user:1", "gone"}})
	if err != nil || del.Deleted != 1 {
		t.Fatalf("delete %+v %v", del, err)
	}
}

func TestRedisSnapshot(t *testing.T) {
	e, f := newRedisEnv(t)
	op, log := e.run(helperapi.ActionDBDump, helperapi.DBDumpRequest{Engine: "redis"})
	if op.State != helperapi.OpSucceeded {
		t.Fatalf("dump: %+v\n%s", op, log)
	}
	res, _ := e.o.Get(op.ID, 0)
	var info helperapi.DumpInfo
	decode(t, res.Op.Result, &info)
	data, err := os.ReadFile(filepath.Join(e.d.Root, "/var/lib/cairn/backups/db/redis", info.File))
	if err != nil || string(data) != "REDIS0011-snapshot" || !strings.HasPrefix(info.File, "redis_") {
		t.Fatalf("snapshot %+v %q %v", info, data, err)
	}
	// A snapshot moved elsewhere by the instance's configuration is not
	// copied.
	f.mu.Lock()
	f.config["dir"] = "/etc"
	f.mu.Unlock()
	if op, _ := e.run(helperapi.ActionDBDump, helperapi.DBDumpRequest{Engine: "redis"}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeRedisCommand {
		t.Fatalf("snapshot elsewhere: %+v", op)
	}
}

// The redis account owns /etc/redis: its configuration file replaced with
// a link is neither read (the password the helper logs in with comes
// from it) nor rewritten.
func TestRedisRefusesLinkedConfig(t *testing.T) {
	e, f := newRedisEnv(t)
	v := newConfTarget(t)
	conf := filepath.Join(e.d.Root, "/etc/redis/redis.conf")
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v.path, conf); err != nil {
		t.Fatal(err)
	}
	e.f.systemd = true
	f.oldBind = true
	if op, log := e.run(helperapi.ActionDBRemote, helperapi.DBRemoteRequest{Engine: "redis", Enabled: false}); op.State != helperapi.OpFailed || op.Error.Code != helperapi.CodeDBConfigUnsafe {
		t.Fatalf("remote through a linked redis.conf: %+v\n%s", op, log)
	}
	v.untouched(t)
	if e.f.ran("systemctl restart redis-server") {
		t.Fatal("restarted")
	}
}

// A reply nesting arrays deeper than any the helper reads is refused
// before it exhausts the stack.
func TestRedisReadRefusesDeepNesting(t *testing.T) {
	payload := strings.Repeat("*1\r\n", 200000) + ":1\r\n"
	rc := &redisConn{r: bufio.NewReader(strings.NewReader(payload))}
	if _, err := rc.read(); err == nil {
		t.Fatalf("parsed %d nested arrays", 200000)
	}
	// A stream entry (an id, then its fields) inside a reply reads.
	entry := array(array(bulk("1-0"), bulks("who", "alice")))
	rc = &redisConn{r: bufio.NewReader(strings.NewReader(array(bulk("0"), entry)))}
	v, err := rc.read()
	if l := asList(v); err != nil || len(l) != 2 || asString(asList(asList(asList(l[1])[0])[1])[1]) != "alice" {
		t.Fatalf("stream reply %v %v", v, err)
	}
}

// What one reply may make the helper hold is bounded, and so is a line.
func TestRedisReadBoundsAReply(t *testing.T) {
	item := bulk(strings.Repeat("x", 1<<20))
	big := "*40\r\n" + strings.Repeat(item, 40)
	rc := &redisConn{r: bufio.NewReader(strings.NewReader(big))}
	if _, err := rc.read(); err == nil {
		t.Fatal("parsed a 40 MiB reply")
	}
	// The same for the replies of one pipeline together.
	rc = &redisConn{r: bufio.NewReader(strings.NewReader(strings.Repeat(item, 40)))}
	if _, err := rc.replies(40); err == nil {
		t.Fatal("parsed 40 MiB of replies to one pipeline")
	}
	rc = &redisConn{r: bufio.NewReader(strings.NewReader("+" + strings.Repeat("a", 1<<20) + "\r\n"))}
	if _, err := rc.read(); err == nil {
		t.Fatal("parsed a 1 MiB status line")
	}
	// A large reply within the bound reads.
	rc = &redisConn{r: bufio.NewReader(strings.NewReader("*8\r\n" + strings.Repeat(item, 8)))}
	if v, err := rc.read(); err != nil || len(asList(v)) != 8 || len(asString(asList(v)[7])) != 1<<20 {
		t.Fatalf("8 MiB reply: %v", err)
	}
}

// A preview reads what it does not show and drops it: a list of large
// items is never held whole in memory.
func TestRedisPreviewDropsWhatItDoesNotShow(t *testing.T) {
	e, f := newRedisEnv(t)
	const items, size = 200, 1 << 20
	chunk := []byte(strings.Repeat("v", size))
	f.stream = func(w io.Writer, args []string) bool {
		if len(args) < 2 || args[1] != "big:list" {
			return false
		}
		switch strings.ToUpper(args[0]) {
		case "TYPE":
			_, _ = io.WriteString(w, "+list\r\n")
		case "TTL":
			_, _ = io.WriteString(w, ":-1\r\n")
		case "LLEN":
			_, _ = fmt.Fprintf(w, ":%d\r\n", items)
		case "LRANGE":
			_, _ = fmt.Fprintf(w, "*%d\r\n", items)
			for range items {
				_, _ = fmt.Fprintf(w, "$%d\r\n", size)
				_, _ = w.Write(chunk)
				_, _ = io.WriteString(w, "\r\n")
			}
		default:
			return false
		}
		return true
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	v, err := call[helperapi.RedisValue](t, e.r, helperapi.ActionRedisGet, helperapi.RedisKeyRequest{Key: "big:list"})
	runtime.ReadMemStats(&after)
	if err != nil || v.Type != "list" || v.Length != items || len(v.Items) != items || !v.Truncated {
		t.Fatalf("preview %v: %s %d %d %v", err, v.Type, v.Length, len(v.Items), v.Truncated)
	}
	for _, it := range v.Items {
		if len(it) != redisItemMax {
			t.Fatalf("item of %d bytes", len(it))
		}
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("the preview of %d MiB allocated %d MiB", items*size>>20, alloc>>20)
	}
}
