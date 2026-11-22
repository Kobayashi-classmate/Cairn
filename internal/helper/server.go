// SPDX-License-Identifier: AGPL-3.0-only

// Package helper implements the root helper: a small server on a Unix socket
// that executes a fixed, compile-time table of typed actions on behalf of the
// unprivileged web process. Peers are authenticated with SO_PEERCRED and every
// action is written to the audit log (journald).
package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

const maxBody = 1 << 20

type handler func(ctx context.Context, caller helperapi.Caller, body []byte) (any, error)

// Registry maps action names to handlers. It is filled once at startup.
type Registry struct {
	handlers map[string]handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{handlers: map[string]handler{}} }

// validator is implemented by request types that check their own fields.
type validator interface{ Validate() error }

// Register adds a typed action. Request bodies are decoded strictly (unknown
// fields are rejected) and validated before fn runs.
func Register[Req, Resp any](r *Registry, action string, fn func(context.Context, helperapi.Caller, Req) (Resp, error)) {
	if _, dup := r.handlers[action]; dup {
		panic("helper: duplicate action " + action)
	}
	r.handlers[action] = func(ctx context.Context, caller helperapi.Caller, body []byte) (any, error) {
		var req Req
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return nil, &helperapi.Error{Code: helperapi.CodeInvalidRequest}
		}
		if v, ok := any(req).(validator); ok {
			if err := v.Validate(); err != nil {
				return nil, err
			}
		}
		return fn(ctx, caller, req)
	}
}

// withFile is the result of an action registered with RegisterFD.
type withFile struct {
	resp any
	file *os.File
}

// RegisterFD adds a typed action that answers with an open file besides
// its result (see helperapi.Client.CallFD). The server sends the file and
// closes its own copy.
func RegisterFD[Req, Resp any](r *Registry, action string, fn func(context.Context, helperapi.Caller, Req) (Resp, *os.File, error)) {
	Register(r, action, func(ctx context.Context, c helperapi.Caller, req Req) (any, error) {
		resp, f, err := fn(ctx, c, req)
		if err != nil {
			if f != nil {
				_ = f.Close()
			}
			return nil, err
		}
		return &withFile{resp: resp, file: f}, nil
	})
}

// Actions returns the registered action names (for diagnostics and tests).
func (r *Registry) Actions() []string {
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	return names
}

// Server serves a Registry to authorized peers.
type Server struct {
	Registry *Registry
	// Allowed reports whether a peer uid may call actions.
	Allowed func(uid uint32) bool
	Log     *slog.Logger
}

type credKey struct{}

// Serve accepts connections on l until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/{action}", s.handle)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if pc, ok := c.(*peerConn); ok {
				return context.WithValue(ctx, credKey{}, pc.cred)
			}
			return ctx
		},
	}
	go func() {
		<-ctx.Done()
		// Shutdown needs a context that outlives the cancelled ctx.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	err := srv.Serve(&peerListener{Listener: l, allowed: s.Allowed, log: s.Log})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	action := r.PathValue("action")
	cred, _ := r.Context().Value(credKey{}).(*unix.Ucred)

	caller, err := helperapi.DecodeCaller(r.Header.Get(helperapi.CallerHeader))
	if err != nil {
		writeErr(w, http.StatusBadRequest, &helperapi.Error{Code: helperapi.CodeInvalidRequest})
		return
	}
	// Who the peer is comes from the kernel, not from what it says.
	caller.PeerRoot = cred != nil && cred.Uid == 0

	h, ok := s.Registry.handlers[action]
	if !ok {
		s.audit(action, caller, cred, helperapi.CodeUnknownAction, start)
		writeErr(w, http.StatusNotFound, &helperapi.Error{Code: helperapi.CodeUnknownAction})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		writeErr(w, http.StatusBadRequest, &helperapi.Error{Code: helperapi.CodeInvalidRequest})
		return
	}

	resp, err := h(r.Context(), caller, body)
	if err != nil {
		var he *helperapi.Error
		if errors.As(err, &he) {
			s.audit(action, caller, cred, he.Code, start)
			writeErr(w, http.StatusUnprocessableEntity, he)
			return
		}
		s.Log.Error("action failed", "action", action, "err", err)
		s.audit(action, caller, cred, helperapi.CodeInternal, start)
		writeErr(w, http.StatusInternalServerError, &helperapi.Error{Code: helperapi.CodeInternal})
		return
	}
	s.audit(action, caller, cred, "ok", start)
	if wf, ok := resp.(*withFile); ok {
		s.sendFile(w, action, wf)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// sendFile writes the response by hand on the raw connection, with the
// file's descriptor attached as SCM_RIGHTS, and closes the connection.
func (s *Server) sendFile(w http.ResponseWriter, action string, wf *withFile) {
	defer func() { _ = wf.file.Close() }()
	body, err := json.Marshal(wf.resp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, &helperapi.Error{Code: helperapi.CodeInternal})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, http.StatusInternalServerError, &helperapi.Error{Code: helperapi.CodeInternal})
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		s.Log.Error("hijack failed", "action", action, "err", err)
		return
	}
	defer func() { _ = conn.Close() }()
	pc, ok := conn.(*peerConn)
	if !ok {
		s.Log.Error("file response on a non-unix connection", "action", action)
		return
	}
	head := fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n%s: 1\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		helperapi.FDHeader, len(body))
	msg := append([]byte(head), body...)
	n, _, err := pc.WriteMsgUnix(msg, unix.UnixRights(int(wf.file.Fd())), nil)
	if err == nil && n < len(msg) {
		_, err = pc.Write(msg[n:])
	}
	if err != nil {
		s.Log.Error("sending a file failed", "action", action, "err", err)
	}
}

// audit records every action in journald. Request parameters are not logged
// here: actions log the specific fields they consider safe themselves.
func (s *Server) audit(action string, c helperapi.Caller, cred *unix.Ucred, result string, start time.Time) {
	var uid uint32
	if cred != nil {
		uid = cred.Uid
	}
	s.Log.Info("audit",
		"action", action, "result", result,
		"user", c.Username, "user_id", c.UserID, "source", c.Source, "remote", c.Remote,
		"peer_uid", uid, "duration_ms", time.Since(start).Milliseconds())
}

func writeErr(w http.ResponseWriter, status int, e *helperapi.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// peerListener drops connections whose peer is not allowed, before any byte
// of the request is read.
type peerListener struct {
	net.Listener
	allowed func(uid uint32) bool
	log     *slog.Logger
}

type peerConn struct {
	*net.UnixConn
	cred *unix.Ucred
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uc, ok := c.(*net.UnixConn)
		if !ok {
			_ = c.Close()
			continue
		}
		cred, err := peerCred(uc)
		if err != nil || !l.allowed(cred.Uid) {
			if cred != nil {
				l.log.Warn("rejected helper peer", "uid", cred.Uid, "pid", cred.Pid)
			} else {
				l.log.Warn("rejected helper peer", "err", err)
			}
			_ = uc.Close()
			continue
		}
		return &peerConn{UnixConn: uc, cred: cred}, nil
	}
}

func peerCred(c *net.UnixConn) (*unix.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return cred, credErr
}
