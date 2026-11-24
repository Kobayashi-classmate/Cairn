// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// newPortChange is an SSH server in the middle of a port change: 22 and
// the new port 2222 both listen.
func newPortChange(t *testing.T) *SSH {
	t.Helper()
	s, _ := newTestSSH(t)
	oldPause, oldEvery, oldAddrs := sshProbePause, sshWatchEvery, localAddrs
	t.Cleanup(func() { sshProbePause, sshWatchEvery, localAddrs = oldPause, oldEvery, oldAddrs })
	sshProbePause, sshWatchEvery = time.Millisecond, 5*time.Millisecond
	localAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.0.2.10")} }
	if _, err := s.addPort(context.Background(), helperapi.Caller{}, helperapi.SSHPortRequest{Port: 2222, CloseAfterMinutes: 30}, io.Discard); err != nil {
		t.Fatal(err)
	}
	return s
}

func dropIn(t *testing.T, s *SSH) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.Root, sshDropIn))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// connections replaces the TCP sockets of the fake /proc.
func connections(t *testing.T, s *SSH, sockets ...fakeSocket) {
	t.Helper()
	writeProcNet(t, s.Root, "tcp", sockets...)
}

var (
	fromOutside  = fakeSocket{local: "192.0.2.10:2222", remote: "203.0.113.9:40000", state: tcpEstablished}
	fromLoopback = fakeSocket{local: "127.0.0.1:2222", remote: "127.0.0.1:40001", state: tcpEstablished}
	fromItself   = fakeSocket{local: "192.0.2.10:2222", remote: "192.0.2.10:40002", state: tcpEstablished}
)

// The old port is never closed while sshd does not answer on the new one,
// neither by the timer nor on request.
func TestSSHFinishPortNeedsTheNewPortToAnswer(t *testing.T) {
	ctx := context.Background()
	for _, unattended := range []bool{false, true} {
		s := newPortChange(t)
		connections(t, s, fromOutside)
		dials := 0
		sshdDial = func(context.Context, string) error {
			dials++
			return errors.New("connection refused")
		}
		err := s.FinishPort(ctx, io.Discard, unattended)
		if !hasCode(err, helperapi.CodeSSHNotListening) || dials == 0 {
			t.Fatalf("unattended %v: %v after %d tries", unattended, err, dials)
		}
		if d := dropIn(t, s); !strings.Contains(d, "Port 22\n") || !strings.Contains(d, "Port 2222\n") {
			t.Fatalf("unattended %v: the old port was closed although the new one does not answer:\n%s", unattended, d)
		}
		st := s.Status(ctx)
		if st.Pending == nil || !slices.Equal(st.Ports, []int{22, 2222}) {
			t.Fatalf("unattended %v: %+v", unattended, st)
		}
		// Only the timer's run is over; a person asking can ask again.
		if want := map[bool]string{false: "", true: helperapi.SSHHeldNotListening}[unattended]; st.Pending.Held != want {
			t.Fatalf("unattended %v: held %q", unattended, st.Pending.Held)
		}
	}
}

// The timer closes the old port only when a connection from another host
// has reached the new one: sshd answering on this host does not tell
// whether a security group or firewall in front of it lets anyone in.
func TestSSHFinishPortUnattendedNeedsAConnectionFromOutside(t *testing.T) {
	ctx := context.Background()
	t.Run("none: both ports stay", func(t *testing.T) {
		s := newPortChange(t)
		// Connections from this host itself prove nothing.
		connections(t, s, fromLoopback, fromItself)
		var log strings.Builder
		err := s.FinishPort(ctx, &log, true)
		if !hasCode(err, helperapi.CodeSSHPortUnreached) {
			t.Fatalf("timer: %v", err)
		}
		if d := dropIn(t, s); !strings.Contains(d, "Port 22\n") || !strings.Contains(d, "Port 2222\n") {
			t.Fatalf("drop-in:\n%s", d)
		}
		st := s.Status(ctx)
		if st.Pending == nil || st.Pending.Held != helperapi.SSHHeldUnreached || st.Pending.ReachedAt != 0 || !slices.Equal(st.Ports, []int{22, 2222}) {
			t.Fatalf("status: %+v", st)
		}
		if !strings.Contains(log.String(), "the old ports [22] stay open") {
			t.Fatalf("log: %s", log.String())
		}
		// A person can still close the old port, or cancel.
		if err := s.FinishPort(ctx, io.Discard, false); err != nil {
			t.Fatal(err)
		}
		if st := s.Status(ctx); st.Pending != nil || !slices.Equal(st.Ports, []int{2222}) {
			t.Fatalf("after closing by hand: %+v", st)
		}
	})
	t.Run("held, then cancelled", func(t *testing.T) {
		s := newPortChange(t)
		if err := s.FinishPort(ctx, io.Discard, true); !hasCode(err, helperapi.CodeSSHPortUnreached) {
			t.Fatalf("timer: %v", err)
		}
		if _, err := s.cancelPort(ctx, helperapi.Caller{}, helperapi.Empty{}, io.Discard); err != nil {
			t.Fatal(err)
		}
		if st := s.Status(ctx); st.Pending != nil || !slices.Equal(st.Ports, []int{22}) {
			t.Fatalf("after the cancel: %+v", st)
		}
	})
	t.Run("connected when the time is up", func(t *testing.T) {
		s := newPortChange(t)
		connections(t, s, fromLoopback, fromOutside)
		if err := s.FinishPort(ctx, io.Discard, true); err != nil {
			t.Fatal(err)
		}
		if d := dropIn(t, s); strings.Contains(d, "Port 22\n") || !strings.Contains(d, "Port 2222\n") {
			t.Fatalf("drop-in:\n%s", d)
		}
		if st := s.Status(ctx); st.Pending != nil || !slices.Equal(st.Ports, []int{2222}) {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("connected earlier, seen by the lookout", func(t *testing.T) {
		s := newPortChange(t)
		s.Resume()
		connections(t, s, fromOutside)
		deadline := time.Now().Add(10 * time.Second)
		for s.load().Pending.ReachedAt == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the connection from outside was not recorded")
			}
			time.Sleep(5 * time.Millisecond)
		}
		// The check login is over long before the time is up.
		connections(t, s)
		if st := s.Status(ctx); st.Pending == nil || st.Pending.ReachedAt == 0 || st.Pending.Held != "" {
			t.Fatalf("status: %+v", st.Pending)
		}
		if err := s.FinishPort(ctx, io.Discard, true); err != nil {
			t.Fatal(err)
		}
		if st := s.Status(ctx); st.Pending != nil || !slices.Equal(st.Ports, []int{2222}) {
			t.Fatalf("status: %+v", st)
		}
	})
}

// The SSH page reads the ports where the firewall reads them, also while
// sshd's configuration cannot be read.
func TestSSHStatusPortsFromDetection(t *testing.T) {
	s, _ := newTestSSH(t)
	ctx := context.Background()
	sshDetect = func(_ context.Context, root string) sshEndpoints {
		if root != s.Root {
			t.Errorf("detection under %q", root)
		}
		return sshEndpoints{Ports: []int{22, 2222}, Installed: true}
	}
	if st := s.Status(ctx); !slices.Equal(st.Ports, []int{22, 2222}) || st.PermitRootLogin == "" {
		t.Fatalf("status: %+v", st)
	}
	queryCommand = func(context.Context, string, ...string) (string, error) { return "", errors.New("exit status 255") }
	sshDetect = func(context.Context, string) sshEndpoints {
		return sshEndpoints{Ports: []int{2222}, Installed: true, Unread: "exit status 255"}
	}
	if st := s.Status(ctx); !slices.Equal(st.Ports, []int{2222}) || !st.Installed {
		t.Fatalf("status with the configuration unreadable: %+v", st)
	}
}
