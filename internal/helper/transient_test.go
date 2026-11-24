// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/execx"
)

func TestRunInUnit(t *testing.T) {
	oldRun, oldDir := dbRun, unitCredDir
	t.Cleanup(func() { dbRun, unitCredDir = oldRun, oldDir })
	unitCredDir = filepath.Join(t.TempDir(), "credentials")
	var calls []execx.Cmd
	var cred string
	fail := false
	dbRun = func(_ context.Context, c execx.Cmd) (execx.Result, error) {
		calls = append(calls, c)
		for _, a := range c.Args {
			if p, ok := strings.CutPrefix(a, "--property=LoadCredential=secret:"); ok {
				data, _ := os.ReadFile(p)
				info, _ := os.Stat(p)
				cred = string(data) + " " + info.Mode().Perm().String()
			}
		}
		if fail && c.Name == "systemd-run" {
			return execx.Result{ExitCode: 1}, errors.New("exit status 1")
		}
		return execx.Result{}, nil
	}
	u := unitRun{Cmd: execx.Cmd{Name: "/usr/bin/tool", Args: []string{"-x", "a b"}, Env: []string{"LANG=C"}, Dir: "/srv"},
		DynamicUser: true, Credentials: map[string][]byte{"secret": []byte("pw")}, CredentialEnv: map[string]string{"TOOL_PASSFILE": "secret"},
		Properties: []string{"ReadWritePaths=/srv/x"}}
	if _, err := runInUnit(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(calls[0].Args, "\n")
	for _, want := range []string{"--wait", "--pipe", "--collect", "--property=DynamicUser=yes", "--property=NoNewPrivileges=yes",
		"--property=ReadWritePaths=/srv/x", "--setenv=LANG=C", "--working-directory=/srv",
		// A dollar sign is written twice: systemd would replace a variable
		// in the command line of a service by the unit's environment.
		"--\n/bin/sh\n-c\nexport TOOL_PASSFILE=\"$$CREDENTIALS_DIRECTORY/secret\"; exec \"$$@\"\nsh\n/usr/bin/tool\n-x\na b"} {
		if !strings.Contains(args, want) {
			t.Errorf("argv lacks %q:\n%s", want, args)
		}
	}
	// The secret is in a root-only file while the unit starts, never in
	// the arguments; the file is gone afterwards.
	if cred != "pw -rw-------" || strings.Contains(args, "pw\n") {
		t.Fatalf("credential: %q\n%s", cred, args)
	}
	if entries, _ := os.ReadDir(unitCredDir); len(entries) != 0 {
		t.Fatalf("credential files left: %v", entries)
	}
	// Words from users reach the command as they are, whatever they hold.
	calls = nil
	if _, err := runInUnit(t.Context(), unitRun{Cmd: execx.Cmd{Name: "/usr/bin/unar", Args: []string{"-o", "/out/${HOME}", "$HOME", "5$ and 100%h.7z"}}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls[0].Args, "\n"); !strings.HasSuffix(got, "--\n/usr/bin/unar\n-o\n/out/$${HOME}\n$$HOME\n5$$ and 100%h.7z") {
		t.Fatalf("command line:\n%s", got)
	}
	// A failed or killed run stops its unit.
	fail, calls = true, nil
	if _, err := runInUnit(t.Context(), unitRun{Cmd: execx.Cmd{Name: "/usr/bin/tool"}}); err == nil {
		t.Fatal("failure not reported")
	}
	unit := strings.TrimPrefix(calls[0].Args[4], "--unit=")
	if len(calls) != 2 || calls[1].Name != "systemctl" || strings.Join(calls[1].Args, " ") != "stop "+unit+".service" {
		t.Fatalf("unit not stopped: %+v", calls)
	}
	// Names that could change the script are refused.
	if _, err := runInUnit(t.Context(), unitRun{Cmd: execx.Cmd{Name: "/usr/bin/tool"}, Credentials: map[string][]byte{"a": nil},
		CredentialEnv: map[string]string{"X;rm": "a"}}); err == nil {
		t.Fatal("bad variable name accepted")
	}
}
