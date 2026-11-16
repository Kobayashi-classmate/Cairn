// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

type fdReq struct {
	Name string `json:"name"`
	Fail bool   `json:"fail"`
}

type fdResp struct {
	Size int64 `json:"size"`
}

func TestFileDescriptorResponses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("passed over the socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "helper.sock")
	l, err := Listen(sock, -1)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	RegisterFD(reg, "test.open", func(_ context.Context, _ helperapi.Caller, req fdReq) (fdResp, *os.File, error) {
		if req.Fail {
			return fdResp{}, nil, &helperapi.Error{Code: "test.refused"}
		}
		f, err := os.Open(filepath.Join(dir, req.Name))
		if err != nil {
			return fdResp{}, nil, err
		}
		fi, _ := f.Stat()
		return fdResp{Size: fi.Size()}, f, nil
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{Registry: reg, Allowed: func(uint32) bool { return true }, Log: log}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, l); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	c := helperapi.NewClient(sock)

	var resp fdResp
	f, err := c.CallFD(callCtx(t), "test.open", helperapi.Caller{Source: "test"}, fdReq{Name: "a.txt"}, &resp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "passed over the socket" || resp.Size != int64(len(data)) {
		t.Fatalf("got %q (%d) %v", data, resp.Size, err)
	}
	if f, err := c.CallFD(callCtx(t), "test.open", helperapi.Caller{Source: "test"}, fdReq{Fail: true}, &resp); f != nil {
		t.Fatal("a failed action must not pass a file")
	} else if he, ok := helperapi.AsError(err); !ok || he.Code != "test.refused" {
		t.Fatalf("error %v", err)
	}
	// Ordinary calls on the same socket keep working.
	if _, err := c.CallFD(callCtx(t), "test.open", helperapi.Caller{Source: "test"}, map[string]any{"bogus": 1}, nil); err == nil {
		t.Fatal("strict decoding applies to file actions too")
	}
}
