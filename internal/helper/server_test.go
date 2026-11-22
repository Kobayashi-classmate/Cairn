// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/config"
	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

func startServer(t *testing.T, allowed func(uint32) bool, deps Deps) *helperapi.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "helper.sock")
	l, err := Listen(sock, -1)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if deps.Log == nil {
		deps.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	RegisterCore(reg, deps)
	srv := &Server{Registry: reg, Allowed: allowed, Log: deps.Log}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return helperapi.NewClient(sock)
}

func callCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPingAndUnknownAction(t *testing.T) {
	c := startServer(t, func(uint32) bool { return true }, Deps{})
	var pong helperapi.PingResponse
	if err := c.Call(callCtx(t), helperapi.ActionPing, helperapi.Caller{Source: "test"}, nil, &pong); err != nil {
		t.Fatal(err)
	}
	if pong.PID != os.Getpid() {
		t.Fatalf("unexpected ping response %+v", pong)
	}
	err := c.Call(callCtx(t), "system.exec_anything", helperapi.Caller{Source: "test"}, nil, nil)
	if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeUnknownAction {
		t.Fatalf("want unknown action, got %v", err)
	}
}

func TestStrictDecodingAndValidation(t *testing.T) {
	c := startServer(t, func(uint32) bool { return true }, Deps{})
	err := c.Call(callCtx(t), helperapi.ActionSetDefaultLocale, helperapi.Caller{Source: "test"},
		map[string]any{"locale": "en", "extra": true}, nil)
	if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeInvalidRequest {
		t.Fatalf("unknown field should be rejected, got %v", err)
	}
	err = c.Call(callCtx(t), helperapi.ActionSetDefaultLocale, helperapi.Caller{Source: "test"},
		helperapi.SetDefaultLocaleRequest{Locale: "xx-YY"}, nil)
	if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeLocaleUnsupport {
		t.Fatalf("unsupported locale should be rejected, got %v", err)
	}
}

func TestSetDefaultLocaleWritesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := config.Save(cfgPath, config.Default(), 0o640, -1, -1); err != nil {
		t.Fatal(err)
	}
	c := startServer(t, func(uint32) bool { return true }, Deps{ConfigPath: cfgPath, ConfigGID: -1})
	if err := c.Call(callCtx(t), helperapi.ActionSetDefaultLocale, helperapi.Caller{Source: "test"},
		helperapi.SetDefaultLocaleRequest{Locale: "zh-CN"}, nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil || cfg.UI.DefaultLocale != "zh-CN" {
		t.Fatalf("config not updated: %+v %v", cfg.UI, err)
	}
}

func TestDisallowedPeerIsDropped(t *testing.T) {
	c := startServer(t, func(uint32) bool { return false }, Deps{})
	err := c.Call(callCtx(t), helperapi.ActionPing, helperapi.Caller{Source: "test"}, nil, nil)
	if he, ok := helperapi.AsError(err); !ok || he.Code != helperapi.CodeUnavailable {
		t.Fatalf("disallowed peer should not get an answer, got %v", err)
	}
}

func TestPeerRootComesFromCredentials(t *testing.T) {
	// A caller header cannot claim it.
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"src":"cli","PeerRoot":true,"peerRoot":true}`))
	if c, err := helperapi.DecodeCaller(forged); err != nil || c.PeerRoot {
		t.Fatalf("forged: %+v %v", c, err)
	}
	if c, _ := helperapi.DecodeCaller(helperapi.Caller{PeerRoot: true}.Encode()); c.PeerRoot {
		t.Fatal("PeerRoot travels in the header")
	}
	// The server sets it from the peer's uid.
	sock := filepath.Join(t.TempDir(), "helper.sock")
	l, err := Listen(sock, -1)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	Register(reg, "test.peer", func(_ context.Context, c helperapi.Caller, _ helperapi.Empty) (map[string]bool, error) {
		return map[string]bool{"root": c.PeerRoot}, nil
	})
	srv := &Server{Registry: reg, Allowed: func(uint32) bool { return true }, Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	var got map[string]bool
	if err := helperapi.NewClient(sock).Call(callCtx(t), "test.peer", helperapi.Caller{Source: "web"}, nil, &got); err != nil {
		t.Fatal(err)
	}
	if got["root"] != (os.Getuid() == 0) {
		t.Fatalf("peer root %v for uid %d", got["root"], os.Getuid())
	}
}
