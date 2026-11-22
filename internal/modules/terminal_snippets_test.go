// SPDX-License-Identifier: AGPL-3.0-only

package modules_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestTerminalSnippets(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin("admin")
	admin.expect(admin.do("GET", "/api/v1/terminal/snippets", nil), 200, "")

	// Saved with the name trimmed and Windows line ends turned into "\n".
	add := admin.expect(admin.do("POST", "/api/v1/terminal/snippets",
		map[string]any{"name": "  Disk usage ", "command": "df -h\r\ndu -sh /srv/www/*"}), 201, "")
	id := int64(add.body["id"].(float64))
	if add.body["name"] != "Disk usage" || add.body["command"] != "df -h\ndu -sh /srv/www/*" {
		t.Fatalf("saved %v", add.body)
	}
	admin.expect(admin.do("POST", "/api/v1/terminal/snippets", map[string]any{"name": "Disk usage", "command": "df"}), 409, "terminal.snippet_exists")
	admin.expect(admin.do("POST", "/api/v1/terminal/snippets", map[string]any{"name": "Logs", "command": "journalctl -f"}), 201, "")

	for _, c := range []struct {
		body        map[string]any
		field, rule string
	}{
		{map[string]any{"name": " ", "command": "ls"}, "name", "required"},
		{map[string]any{"name": strings.Repeat("n", 65), "command": "ls"}, "name", "max_length"},
		{map[string]any{"name": "a\tb", "command": "ls"}, "name", "pattern"},
		{map[string]any{"name": "Empty", "command": " \n "}, "command", "required"},
		{map[string]any{"name": "Big", "command": strings.Repeat("x", 8<<10+1)}, "command", "max_length"},
		// Ctrl-C and an escape sequence would act on the terminal.
		{map[string]any{"name": "Break", "command": "sleep 9\x03"}, "command", "pattern"},
		{map[string]any{"name": "Escape", "command": "printf \x1b[2J"}, "command", "pattern"},
		{map[string]any{"name": "Return", "command": "ls\rrm"}, "command", "pattern"},
	} {
		r := admin.expect(admin.do("POST", "/api/v1/terminal/snippets", c.body), 400, "validation.invalid")
		if r.params()["field"] != c.field || r.params()["rule"] != c.rule {
			t.Fatalf("%v: %v", c.body["name"], r.params())
		}
	}

	path := fmt.Sprintf("/api/v1/terminal/snippets/%d", id)
	admin.expect(admin.do("PUT", path, map[string]any{"name": "Logs", "command": "df"}), 409, "terminal.snippet_exists")
	admin.expect(admin.do("PUT", path, map[string]any{"name": "Disk usage", "command": "df -h"}), 200, "")
	list := admin.expect(admin.do("GET", "/api/v1/terminal/snippets", nil), 200, "").body["snippets"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["command"] != "df -h" || list[1].(map[string]any)["name"] != "Logs" {
		t.Fatalf("list %v", list)
	}
	admin.expect(admin.do("PUT", "/api/v1/terminal/snippets/x", map[string]any{"name": "a", "command": "b"}), 404, "terminal.snippet_not_found")

	// Each account sees and changes only its own.
	admin.elevate("")
	created := admin.expect(admin.do("POST", "/api/v1/users", map[string]any{"username": "oper", "password": pw, "role": "operator"}), 201, "")
	operID := int64(created.obj("user")["id"].(float64))
	op := e.client()
	op.expect(op.login("oper", pw), 200, "")
	if l := op.expect(op.do("GET", "/api/v1/terminal/snippets", nil), 200, "").body["snippets"].([]any); len(l) != 0 {
		t.Fatalf("operator sees %v", l)
	}
	op.expect(op.do("PUT", path, map[string]any{"name": "Mine", "command": "id"}), 404, "terminal.snippet_not_found")
	op.expect(op.do("DELETE", path, nil), 404, "terminal.snippet_not_found")
	op.expect(op.do("POST", "/api/v1/terminal/snippets", map[string]any{"name": "Disk usage", "command": "df"}), 201, "")
	for i := 1; i < 100; i++ {
		op.expect(op.do("POST", "/api/v1/terminal/snippets", map[string]any{"name": fmt.Sprintf("n%d", i), "command": "true"}), 201, "")
	}
	r := op.expect(op.do("POST", "/api/v1/terminal/snippets", map[string]any{"name": "one more", "command": "true"}), 409, "terminal.too_many_snippets")
	if r.params()["max"] != float64(100) {
		t.Fatalf("limit %v", r.params())
	}

	admin.expect(admin.do("DELETE", path, nil), 204, "")
	admin.expect(admin.do("DELETE", path, nil), 404, "terminal.snippet_not_found")

	// Deleting an account deletes its commands.
	admin.expect(admin.do("DELETE", fmt.Sprintf("/api/v1/users/%d", operID), nil), 204, "")
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM terminal_snippets WHERE user_id = ?`, operID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("left after deletion: %d %v", n, err)
	}
}
