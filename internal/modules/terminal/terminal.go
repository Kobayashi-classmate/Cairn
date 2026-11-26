// SPDX-License-Identifier: AGPL-3.0-only

// Package terminal is the web terminal. The root helper starts a login
// shell on a new pseudo-terminal, as a site owner or an ordinary user
// (root only when an administrator switched it on), and hands over the
// terminal's master side; this module keeps it as a session and joins it
// to the browser over a WebSocket. A session outlives a closed tab for a
// few minutes, so a reload picks it up again with its recent output.
package terminal

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/authz"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
	"github.com/Kobayashi-classmate/Cairn/internal/jobs"
	"github.com/Kobayashi-classmate/Cairn/internal/module"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Permissions of this module.
const (
	Use authz.Permission = "terminal.use"
	// Admin opens terminals as root and as users that are not site owners
	// (on cloud images the default user can sudo without a password), and
	// switches root terminals on (administrators).
	Admin authz.Permission = "terminal.admin"
)

const (
	maxPerUser   = 8
	detachedTTL  = 5 * time.Minute
	scrollback   = 256 << 10
	readLimit    = 64 << 10
	reapInterval = 30 * time.Second
)

// Helper is what the module needs from the root helper.
type Helper interface {
	jobs.HelperClient
	CallFD(ctx context.Context, action string, caller helperapi.Caller, req, resp any) (*os.File, error)
}

// Module implements module.Module.
type Module struct {
	Helper Helper
	DB     *sql.DB
	Log    *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session
	// opening counts each account's sessions being opened, so the limit
	// holds while the helper starts them.
	opening map[int64]int
	// revoked counts RevokeUser calls per account: a session whose opening
	// saw an earlier count is not kept.
	revoked map[int64]uint64
}

// ID implements module.Module.
func (m *Module) ID() string { return "terminal" }

// Info implements module.Describer.
func (m *Module) Info() module.Info {
	return module.Info{
		Optional: true,
		Permissions: []module.Permission{
			{Name: Use, Roles: []authz.Role{authz.Operator}},
			{Name: Admin},
		},
		Nav: []module.NavItem{
			{Key: "terminal", LabelKey: "common.nav.terminal", Icon: "terminal", Route: "terminal", Permission: Use, Group: module.GroupOps, Order: 20},
		},
	}
}

// Routes implements module.Module.
func (m *Module) Routes(r *api.Router) {
	doc := func(s string) api.RouteOption { return api.Doc("terminal", s) }
	r.Handle("GET /api/v1/terminal/users", m.users, api.Require(Use), doc("Who a terminal can run as"), api.Returns(200, helperapi.TermUsers{}))
	r.Handle("PUT /api/v1/terminal/settings", m.settings, api.Require(Admin), api.StepUp(), api.Body(settingsRequest{}),
		doc("Allow root terminals or not"), api.Returns(200, helperapi.AccessSettings{}))
	r.Handle("GET /api/v1/terminal/sessions", m.list, api.Require(Use), doc("Your terminal sessions"), api.Returns(200, sessionsResponse{}))
	r.Handle("POST /api/v1/terminal/sessions", m.open, api.Require(Use), api.StepUp(), api.Body(helperapi.TermOpenRequest{}),
		doc("Open a terminal (attach to it with the WebSocket)"), api.Returns(201, sessionView{}))
	r.Handle("DELETE /api/v1/terminal/sessions/{id}", m.close, api.Require(Use), doc("Close a terminal"), api.Returns(204, nil))
	// A browser's WebSocket handshake cannot carry the session key: it
	// brings a one-time ticket.
	r.Handle("GET /api/v1/terminal/sessions/{id}/ws", m.attach, api.Require(Use), api.Ticket(),
		doc("Attach to a terminal: binary messages carry input and output, text messages {\"type\":\"resize\",\"cols\",\"rows\"}"))
	m.snippetRoutes(r, doc)
}

type settingsRequest struct {
	RootTerminal bool `json:"rootTerminal"`
}

type sessionView struct {
	ID        string `json:"id"`
	User      string `json:"user"`
	Shell     string `json:"shell"`
	Home      string `json:"home"`
	CreatedAt int64  `json:"createdAt"`
	Attached  bool   `json:"attached"`
}

type sessionsResponse struct {
	Sessions []sessionView `json:"sessions"`
}

func callerOf(r *http.Request) helperapi.Caller {
	who := modutil.ActorOf(r)
	return helperapi.Caller{UserID: who.UserID, Username: who.Username, Remote: api.ClientIP(r), Source: who.Source}
}

func helperError(err error) error {
	if he, ok := helperapi.AsError(err); ok {
		status := http.StatusConflict
		if he.Code == helperapi.CodeInvalidRequest || he.Code == "validation.invalid" {
			status = http.StatusBadRequest
		}
		return api.NewError(status, he.Code, he.Params)
	}
	return err
}

func (m *Module) termUsers(r *http.Request) (helperapi.TermUsers, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var resp helperapi.TermUsers
	if err := m.Helper.Call(ctx, helperapi.ActionTermUsers, callerOf(r), nil, &resp); err != nil {
		return resp, helperError(err)
	}
	return resp, nil
}

// privileged reports whether a terminal of this kind needs Admin: anything
// but a site owner, which has no sudo. Unknown kinds count as privileged.
func privileged(kind string) bool { return kind != helperapi.TermUserSite }

func (m *Module) users(w http.ResponseWriter, r *http.Request) error {
	resp, err := m.termUsers(r)
	if err != nil {
		return err
	}
	// Others than site owners are for administrators.
	if p, ok := api.PrincipalFrom(r.Context()); !ok || !p.Can(Admin) {
		users := resp.Users[:0]
		for _, u := range resp.Users {
			if !privileged(u.Kind) {
				users = append(users, u)
			}
		}
		resp.Users = users
	}
	if resp.Users == nil {
		resp.Users = []helperapi.TermUser{}
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) settings(w http.ResponseWriter, r *http.Request) error {
	var req settingsRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var cur helperapi.FileRoots
	if err := m.Helper.Call(ctx, helperapi.ActionFileRoots, callerOf(r), nil, &cur); err != nil {
		return helperError(err)
	}
	s := cur.Settings
	s.RootTerminal = req.RootTerminal
	if err := m.Helper.Call(ctx, helperapi.ActionAccessSet, callerOf(r), s, &s); err != nil {
		return helperError(err)
	}
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "terminal.root", "ok", map[string]any{"enabled": req.RootTerminal})
	return api.WriteJSON(w, http.StatusOK, s)
}

// session is an open terminal.
type session struct {
	id, user, shell, home string
	// kind is who the shell runs as (helperapi.TermUser*).
	kind string
	// authSession is the browser session that opened it; the terminal ends
	// with it (signed out, revoked, expired, or dropped by a password
	// change, also through the cairn command).
	authSession string
	owner       int64
	created     time.Time
	master      *os.File

	mu       sync.Mutex
	buf      []byte // the last output, replayed on attach
	clients  map[chan []byte]struct{}
	detached time.Time
	done     chan struct{}
	once     sync.Once
}

func (s *session) view() sessionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionView{ID: s.id, User: s.user, Shell: s.shell, Home: s.home, CreatedAt: s.created.Unix(), Attached: len(s.clients) > 0}
}

// pump reads the terminal until the shell ends.
func (s *session) pump() {
	b := make([]byte, 32<<10)
	for {
		n, err := s.master.Read(b)
		if n > 0 {
			chunk := append([]byte(nil), b[:n]...)
			s.mu.Lock()
			s.buf = trimBacklog(append(s.buf, chunk...), scrollback)
			for c := range s.clients {
				select {
				case c <- chunk:
				default: // a client too slow to keep up is dropped
					delete(s.clients, c)
					close(c)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			s.end()
			return
		}
	}
}

// end closes the terminal (hanging up the shell) and its clients.
func (s *session) end() {
	s.once.Do(func() {
		_ = s.master.Close()
		s.mu.Lock()
		for c := range s.clients {
			delete(s.clients, c)
			close(c)
		}
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *session) resize(cols, rows int) error {
	if cols < 10 || cols > 1000 || rows < 2 || rows > 500 {
		return errors.New("bad size")
	}
	raw, err := s.master.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := raw.Control(func(fd uintptr) {
		ierr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)}) //nolint:gosec // G115: checked above.
	}); err != nil {
		return err
	}
	return ierr
}

func (s *session) attached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients) > 0
}

// drop ends a terminal and forgets it.
func (m *Module) drop(s *session) {
	s.end()
	m.mu.Lock()
	if m.sessions[s.id] == s {
		delete(m.sessions, s.id)
	}
	m.mu.Unlock()
}

// Sweep ends the terminals whose browser session is gone; Run calls it
// every reapInterval. A terminal in use counts as activity of its session,
// so the session does not time out idle while someone types in it.
func (m *Module) Sweep(ctx context.Context) {
	m.mu.Lock()
	list := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()
	for _, s := range list {
		alive, err := auth.SessionAlive(ctx, m.DB, s.authSession)
		if err != nil {
			m.Log.Warn("terminal: checking its session failed", "err", err)
			continue
		}
		if !alive {
			m.drop(s)
			continue
		}
		if s.attached() {
			if err := auth.TouchSession(ctx, m.DB, s.authSession); err != nil {
				m.Log.Warn("terminal: touching its session failed", "err", err)
			}
		}
	}
}

// Run closes sessions nobody attached to for a while, and those whose
// browser session is gone.
func (m *Module) Run(ctx context.Context) {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, s := range m.sessions {
				s.end()
			}
			m.mu.Unlock()
			return
		case <-t.C:
			m.reap()
			m.Sweep(ctx)
		}
	}
}

func (m *Module) reap() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := len(s.clients) == 0 && time.Since(s.detached) > detachedTTL
		s.mu.Unlock()
		select {
		case <-s.done:
			delete(m.sessions, id)
			continue
		default:
		}
		if idle {
			s.end()
			delete(m.sessions, id)
		}
	}
}

func (m *Module) find(r *http.Request) (*session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[r.PathValue("id")]
	if !ok || s.owner != modutil.ActorOf(r).UserID {
		return nil, api.NewError(http.StatusNotFound, "terminal.not_found", nil)
	}
	return s, nil
}

// RevokeUser ends every terminal of an account. The users module calls it
// when the account is disabled, deleted, changes role or gets a new
// password: a shell is checked only when it is opened and attached, and an
// attached one would otherwise outlive the account's access (and a deleted
// account's ID can be given to the next one created).
func (m *Module) RevokeUser(userID int64) {
	m.mu.Lock()
	if m.revoked == nil {
		m.revoked = map[int64]uint64{}
	}
	m.revoked[userID]++
	var ended []*session
	for id, s := range m.sessions {
		if s.owner == userID {
			ended = append(ended, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range ended {
		s.end()
	}
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) error {
	me := modutil.ActorOf(r).UserID
	resp := sessionsResponse{Sessions: []sessionView{}}
	m.mu.Lock()
	for _, s := range m.sessions {
		select {
		case <-s.done:
			continue
		default:
		}
		if s.owner == me {
			resp.Sessions = append(resp.Sessions, s.view())
		}
	}
	m.mu.Unlock()
	sort.Slice(resp.Sessions, func(i, j int) bool { return resp.Sessions[i].CreatedAt < resp.Sessions[j].CreatedAt })
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) open(w http.ResponseWriter, r *http.Request) error {
	var req helperapi.TermOpenRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.Validate(); err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			return api.NewError(http.StatusBadRequest, he.Code, he.Params)
		}
		return err
	}
	p, ok := api.PrincipalFrom(r.Context())
	if !ok {
		return api.ErrForbidden
	}
	// Who the user is, as the helper lists it: only site owners are open to
	// operators. The helper checks the name against the same list again.
	kind := helperapi.TermUserRoot
	if req.User != helperapi.TermUserRoot {
		users, err := m.termUsers(r)
		if err != nil {
			return err
		}
		kind = ""
		for _, u := range users.Users {
			if u.Name == req.User {
				kind = u.Kind
			}
		}
	}
	if privileged(kind) && !p.Can(Admin) {
		return api.ErrForbidden
	}
	me := p.UserID
	// The slot is taken before the helper is asked, so concurrent requests
	// cannot pass the limit together.
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = map[string]*session{}
	}
	if m.opening == nil {
		m.opening = map[int64]int{}
	}
	mine := m.opening[me]
	for _, s := range m.sessions {
		if s.owner == me {
			mine++
		}
	}
	if mine >= maxPerUser {
		m.mu.Unlock()
		return api.NewError(http.StatusConflict, helperapi.CodeTermTooMany, map[string]any{"max": maxPerUser})
	}
	m.opening[me]++
	generation := m.revoked[me]
	m.mu.Unlock()
	release := func() {
		if m.opening[me]--; m.opening[me] <= 0 {
			delete(m.opening, me)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var info helperapi.TermSession
	master, err := m.Helper.CallFD(ctx, helperapi.ActionTermOpen, callerOf(r), req, &info)
	if err != nil {
		m.mu.Lock()
		release()
		m.mu.Unlock()
		return helperError(err)
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := &session{id: hex.EncodeToString(b), user: info.User, kind: kind, authSession: p.SessionID, shell: info.Shell, home: info.Home, owner: me, created: time.Now(),
		master: master, clients: map[chan []byte]struct{}{}, detached: time.Now(), done: make(chan struct{})}
	m.mu.Lock()
	release()
	if m.revoked[me] != generation {
		// The account changed while the helper opened it.
		m.mu.Unlock()
		s.end()
		return api.ErrForbidden
	}
	m.sessions[s.id] = s
	m.mu.Unlock()
	go s.pump()
	modutil.Audit(r, m.DB, m.Log, modutil.ActorOf(r), "terminal.open", "ok", map[string]any{"user": info.User, "session": s.id})
	return api.WriteJSON(w, http.StatusCreated, s.view())
}

func (m *Module) close(w http.ResponseWriter, r *http.Request) error {
	s, err := m.find(r)
	if err != nil {
		return err
	}
	s.end()
	m.mu.Lock()
	delete(m.sessions, s.id)
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// attach joins a WebSocket to a session. The WebSocket must come from the
// panel's own origin (the library refuses others), and a browser's brings
// a ticket only the panel's page could get (see api.Ticket).
func (m *Module) attach(w http.ResponseWriter, r *http.Request) error {
	s, err := m.find(r)
	if err != nil {
		return err
	}
	// The session was opened with a second verification; joining it again
	// checks the caller still may run such a shell. An API token never
	// reaches a root (or sudo-capable) shell, whatever its scopes.
	if p, ok := api.PrincipalFrom(r.Context()); !ok || privileged(s.kind) && (!p.Can(Admin) || p.IsToken()) {
		return api.ErrForbidden
	}
	// A terminal whose browser session is gone ends now, not at the next
	// sweep.
	if alive, err := auth.SessionAlive(r.Context(), m.DB, s.authSession); err != nil {
		return err
	} else if !alive {
		m.drop(s)
		return api.NewError(http.StatusNotFound, "terminal.not_found", nil)
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil //nolint:nilerr // Accept has answered the request
	}
	c.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := make(chan []byte, 256)
	s.mu.Lock()
	backlog := append([]byte(nil), s.buf...)
	select {
	case <-s.done:
		s.mu.Unlock()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"exit"}`))
		_ = c.Close(websocket.StatusNormalClosure, "ended")
		return nil
	default:
	}
	s.clients[out] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, ok := s.clients[out]; ok {
			delete(s.clients, out)
			close(out)
		}
		s.detached = time.Now()
		s.mu.Unlock()
	}()
	if len(backlog) > 0 {
		if err := c.Write(ctx, websocket.MessageBinary, backlog); err != nil {
			return nil //nolint:nilerr // the browser went away
		}
	}
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := s.master.Write(data); err != nil {
					return
				}
				continue
			}
			var ctl control
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" {
				_ = s.resize(ctl.Cols, ctl.Rows)
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			_ = c.Close(websocket.StatusNormalClosure, "")
			return nil
		case chunk, ok := <-out:
			if !ok {
				select {
				case <-s.done:
					_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"exit"}`))
					_ = c.Close(websocket.StatusNormalClosure, "ended")
					return nil
				default:
					_ = c.Close(websocket.StatusPolicyViolation, "too slow")
					return nil
				}
			}
			if err := c.Write(ctx, websocket.MessageBinary, chunk); err != nil {
				return nil //nolint:nilerr // the browser went away
			}
		}
	}
}
