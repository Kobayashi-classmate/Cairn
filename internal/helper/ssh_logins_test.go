// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// journalLine is one line of journalctl --output=json.
func journalLine(t *testing.T, fields map[string]string) string {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

// The login log takes only what sshd wrote: the identifier is the
// writer's to choose, the unit and the user are journald's.
func TestSSHLoginsComeFromSSHD(t *testing.T) {
	s, _ := newTestSSH(t)
	writeSystemFile(t, s.Root, "usr/bin/journalctl", "")
	writeSystemFile(t, s.Root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\nsshd:x:105:65534::/run/sshd:/usr/sbin/nologin\n"+
		"deploy:x:1000:1000::/home/deploy:/bin/bash\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\n")
	line := func(ts, unit, uid, ident, msg string) string {
		f := map[string]string{"__REALTIME_TIMESTAMP": ts, "SYSLOG_IDENTIFIER": ident, "_UID": uid, "MESSAGE": msg}
		if unit != "" {
			f["_SYSTEMD_UNIT"] = unit
		}
		return journalLine(t, f)
	}
	journal := line("1700000001000000", "", "33", "sshd", "Accepted publickey for root from 203.0.113.66 port 4242 ssh2") + // logger -t sshd, from a website
		line("1700000002000000", "php8.4-fpm.service", "33", "sshd-session", "Accepted password for root from 203.0.113.67 port 4242 ssh2") +
		line("1700000003000000", "ssh.service", "1000", "sshd", "Accepted password for root from 203.0.113.68 port 4242 ssh2") + // a shell left in sshd's unit
		line("1700000004000000", "ssh.service", "0", "sshd-session", "Accepted publickey for root from 198.51.100.1 port 51234 ssh2: ED25519 SHA256:x") +
		line("1700000005000000", "sshd.service", "0", "sshd", "Failed password for deploy from 198.51.100.2 port 22 ssh2") +
		line("1700000006000000", "ssh@3-192.0.2.1:22-198.51.100.3:5555.service", "0", "sshd", "Accepted password for deploy from 198.51.100.3 port 5555 ssh2") +
		line("1700000007000000", "ssh.service", "105", "sshd-auth", "Invalid user oracle from 198.51.100.4 port 4444") +
		line("1700000008000000", "ssh.service", "0", "sshd-session", "Invalid user x from 203.0.113.69 port 1 from 198.51.100.5 port 4445") +
		line("1700000009000000", "ssh.service", "0", "sshd-session", "Failed password for invalid user y from 203.0.113.70 port 1 ssh2 from 198.51.100.6 port 4446 ssh2") +
		line("1700000010000000", "ssh.service", "0", "sshd-session", "Accepted password for root from host.example port 22 ssh2")
	var args []string
	queryCommand = func(_ context.Context, _ string, a ...string) (string, error) {
		args = a
		return journal, nil
	}
	out, err := s.logins(context.Background(), helperapi.Caller{}, helperapi.SSHLoginsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range out.Entries {
		got = append(got, l.Result+" "+l.User+" "+l.IP)
	}
	want := []string{"accepted root 198.51.100.1", "failed deploy 198.51.100.2", "accepted deploy 198.51.100.3",
		"invalid_user oracle 198.51.100.4", "invalid_user x from 203.0.113.69 port 1 198.51.100.5",
		"invalid_user y from 203.0.113.70 port 1 ssh2 198.51.100.6"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// journalctl itself keeps to sshd's units.
	joined := strings.Join(args, " ")
	for _, u := range []string{"ssh.service", "sshd.service", "ssh@*.service", "sshd@*.service"} {
		if !strings.Contains(joined, "--unit="+u) {
			t.Errorf("journalctl %s: no --unit=%s", joined, u)
		}
	}
}
