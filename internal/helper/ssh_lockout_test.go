// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/helperapi"
)

// onlyRoot leaves root as the only account that may log in, as on many
// VPS images.
func onlyRoot(t *testing.T, s *SSH) {
	t.Helper()
	writeSystemFile(t, s.Root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534::/:/usr/sbin/nologin\n")
}

// A usable password hash and a locked one, for /etc/shadow.
const (
	shadowHash   = "$y$j9T$saltsaltsalt$hashhashhashhashhashhashhashhashhashhash"
	shadowLocked = "!" + shadowHash
)

func shadowLine(user, hash string) string { return user + ":" + hash + ":19000:0:99999:7:::\n" }

// The login settings refuse to leave nobody a way in, also while passwords
// stay on: root may be the only account.
func TestSSHSettingsLeaveAWayIn(t *testing.T) {
	ctx := context.Background()
	rootKey := func(t *testing.T, s *SSH) {
		writeSystemFile(t, s.Root, "root/.ssh/authorized_keys", newKey(t, "root@laptop")+"\n")
	}
	rootShadow := func(hash string) func(*testing.T, *SSH) {
		return func(t *testing.T, s *SSH) { writeSystemFile(t, s.Root, "etc/shadow", shadowLine("root", hash)) }
	}
	for _, c := range []struct {
		name     string
		prepare  func(*testing.T, *SSH)
		password bool
		root     string
		ok       bool
	}{
		{"root login off", rootShadow(shadowHash), true, helperapi.RootLoginNo, false},
		{"root with keys only, without a key", rootShadow(shadowHash), true, helperapi.RootLoginKeysOnly, false},
		{"root with keys only, with a key", rootKey, true, helperapi.RootLoginKeysOnly, true},
		{"root with its password", rootShadow(shadowHash), true, helperapi.RootLoginYes, true},
		{"root whose password is locked", rootShadow(shadowLocked), true, helperapi.RootLoginYes, false},
		{"root without a password", rootShadow("*"), true, helperapi.RootLoginYes, false},
		{"root with an empty password", rootShadow(""), true, helperapi.RootLoginYes, false},
		{"root with a key, passwords off", rootKey, false, helperapi.RootLoginYes, true},
		{"root with a password, passwords off", rootShadow(shadowHash), false, helperapi.RootLoginYes, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestSSH(t)
			onlyRoot(t, s)
			c.prepare(t, s)
			_, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PasswordAuthentication: c.password, PermitRootLogin: c.root}, io.Discard)
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("nobody could log in, and the settings were saved")
			}
			_, statErr := os.Stat(filepath.Join(s.Root, sshDropIn))
			if !c.ok && statErr == nil {
				t.Fatal("the drop-in was written")
			}
		})
	}

	// Another account with a password lets root's login go.
	s, _ := newTestSSH(t)
	writeSystemFile(t, s.Root, "etc/shadow", shadowLine("root", shadowHash)+shadowLine("deploy", shadowHash))
	if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PasswordAuthentication: true, PermitRootLogin: helperapi.RootLoginNo}, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Not when that account is locked, or has expired.
	for _, line := range []string{shadowLine("deploy", shadowLocked), "deploy:" + shadowHash + ":19000:0:99999:7::1:\n"} {
		writeSystemFile(t, s.Root, "etc/shadow", line)
		if _, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PasswordAuthentication: true, PermitRootLogin: helperapi.RootLoginNo}, io.Discard); err == nil {
			t.Fatalf("root login off with deploy as %q", line)
		}
	}
}

// Deleting a key keeps someone a way in, also while sshd takes passwords:
// root may log in with keys only.
func TestSSHKeyDeletionLeavesAWayIn(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("writing keys for another user needs root (make test runs as root in a container)")
	}
	ctx := context.Background()
	for _, c := range []struct {
		name, dropIn, shadow string
		ok                   bool
	}{
		{"root with keys only", "PasswordAuthentication yes\nPermitRootLogin prohibit-password\n", shadowLine("root", shadowHash), false},
		{"root with a locked password", "PasswordAuthentication yes\nPermitRootLogin yes\n", shadowLine("root", shadowLocked), false},
		{"root with its password", "PasswordAuthentication yes\nPermitRootLogin yes\n", shadowLine("root", shadowHash), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTestSSH(t)
			onlyRoot(t, s)
			writeSystemFile(t, s.Files.StateDir, accessFile, `{"rootTerminal":true}`)
			writeSystemFile(t, s.Root, sshDropIn, c.dropIn)
			writeSystemFile(t, s.Root, "etc/shadow", c.shadow)
			writeSystemFile(t, s.Root, "root/.ssh/authorized_keys", newKey(t, "root@laptop")+"\n")
			k, err := s.keys("root")
			if err != nil || len(k.Keys) != 1 {
				t.Fatalf("keys %+v %v", k, err)
			}
			_, err = s.deleteKey(ctx, helperapi.Caller{}, helperapi.SSHKeyDeleteRequest{User: "root", Fingerprint: k.Keys[0].Fingerprint})
			if c.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("root's last key deleted although root could then not log in")
			}
			want := 1
			if c.ok {
				want = 0
			}
			if left, _ := s.keys("root"); len(left.Keys) != want {
				t.Fatalf("%d keys left", len(left.Keys))
			}
		})
	}
}

// A key counts as a way in only where sshd takes it: with public keys on,
// from the file the panel writes, for a user sshd lets in, and when the key
// gives a shell.
func TestSSHKeysCountWhereSSHDTakesThem(t *testing.T) {
	ctx := context.Background()
	key := newKey(t, "deploy@laptop")
	for _, c := range []struct {
		name, config, line string
		ok                 bool
	}{
		{"public keys off", "PubkeyAuthentication no\n", key, false},
		{"keys read from elsewhere", "AuthorizedKeysFile /etc/ssh/authorized_keys/%u\n", key, false},
		{"keys read from the home", "AuthorizedKeysFile %h/.ssh/authorized_keys\n", key, true},
		{"keys read from the home, relative", "AuthorizedKeysFile .ssh/authorized_keys2 .ssh/authorized_keys\n", key, true},
		{"user not allowed", "AllowUsers nobody\n", key, false},
		{"user allowed from one network", "AllowUsers deploy@203.0.113.0/24\n", key, true},
		{"user allowed by a pattern", "AllowUsers dep*\n", key, true},
		{"user denied", "DenyUsers deploy\n", key, false},
		{"user denied from one network only", "DenyUsers deploy@203.0.113.9\n", key, true},
		{"group not allowed", "AllowGroups wheel\n", key, false},
		{"member of an allowed group", "AllowGroups staff\n", key, true},
		{"primary group denied", "DenyGroups deploy\n", key, false},
		{"a forced command", "", `command="/usr/bin/false" ` + key, false},
		{"expired", "", `expiry-time="20200101" ` + key, false},
		{"not yet expired", "", `expiry-time="29991231" ` + key, true},
		{"limited to an address", "", `from="203.0.113.1" ` + key, true},
		{"keys come from a command", "PubkeyAuthentication yes\nAuthorizedKeysCommand /usr/local/bin/keys %u\n", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newTestSSH(t)
			writeSystemFile(t, s.Root, "etc/group", "root:x:0:\ndeploy:x:1000:\nstaff:x:50:deploy\nwheel:x:10:\n")
			writeSystemFile(t, s.Root, "home/deploy/.ssh/authorized_keys", c.line+"\n")
			writeSystemFile(t, s.Root, "etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\n"+c.config+"PasswordAuthentication yes\n")
			_, err := s.settings(ctx, helperapi.Caller{}, helperapi.SSHSettingsRequest{PasswordAuthentication: false, PermitRootLogin: helperapi.RootLoginNo}, io.Discard)
			if c.ok && err != nil {
				t.Fatalf("refused: %v\nsshd -T:\n%s", err, f.effective())
			}
			if !c.ok && err == nil {
				t.Fatalf("passwords off although no key can be used\nsshd -T:\n%s", f.effective())
			}
		})
	}
}
