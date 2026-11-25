// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"os"
	"path/filepath"
	"testing"
)

// An ID wider than the kernel's must not be taken and wrap later: 2^32 is
// not root for any check on the number, and is root as a 32-bit value.
func TestParseID(t *testing.T) {
	for s, want := range map[string]int{"0": 0, "33": 33, "65534": 65534, "2147483647": 2147483647} {
		if got, err := parseID(s); err != nil || got != want {
			t.Errorf("parseID(%q) = %d, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "-1", "+5", " 7", "0x10", "1e3", "abc", "2147483648", "4294967295", "4294967296", "4294967297", "18446744073709551616"} {
		if got, err := parseID(s); err == nil {
			t.Errorf("parseID(%q) = %d, accepted", s, got)
		}
	}
}

// The terminal's own reading of the passwd file leaves out a line whose IDs
// are not IDs, so that no shell is ever started with them.
func TestPasswdLeavesOutBadIDs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	passwd := "root:x:0:0:root:/root:/bin/bash\n" +
		"deploy:x:1000:1000::/home/deploy:/bin/bash\n" +
		"wraps:x:4294967296:4294967296::/home/wraps:/bin/bash\n" +
		"minus:x:-1:1000::/home/minus:/bin/bash\n" +
		"group:x:1001:4294967297::/home/group:/bin/bash\n"
	if err := os.WriteFile(filepath.Join(root, "etc/passwd"), []byte(passwd), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &Files{Root: root}
	var names []string
	for _, e := range f.passwd() {
		names = append(names, e.name)
		if e.uid < 0 || e.uid > 1<<31-1 || e.gid < 0 || e.gid > 1<<31-1 {
			t.Errorf("%s: %d:%d", e.name, e.uid, e.gid)
		}
	}
	if len(names) != 2 || names[0] != "root" || names[1] != "deploy" {
		t.Fatalf("passwd entries: %v", names)
	}
}
