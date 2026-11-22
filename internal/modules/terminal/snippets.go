// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/modules/modutil"
)

// Saved commands: each account keeps its own list, which the Terminal page
// pastes into or runs in the terminal in front. The panel only stores them;
// they reach a shell as typed input, under the identity of that terminal.
const (
	snippetsMax       = 100
	snippetNameMax    = 64
	snippetCommandMax = 8 << 10
)

type snippetRequest struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

type snippetView struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	UpdatedAt int64  `json:"updatedAt"`
}

type snippetsResponse struct {
	Snippets []snippetView `json:"snippets"`
}

var (
	errSnippetNotFound = api.NewError(http.StatusNotFound, "terminal.snippet_not_found", nil)
	errSnippetExists   = api.NewError(http.StatusConflict, "terminal.snippet_exists", nil)
)

func (m *Module) snippetRoutes(r *api.Router, doc func(string) api.RouteOption) {
	r.Handle("GET /api/v1/terminal/snippets", m.snippets, api.Require(Use), doc("Your saved commands"), api.Returns(200, snippetsResponse{}))
	r.Handle("POST /api/v1/terminal/snippets", m.addSnippet, api.Require(Use), api.Body(snippetRequest{}),
		doc("Save a command"), api.Returns(201, snippetView{}))
	r.Handle("PUT /api/v1/terminal/snippets/{id}", m.updateSnippet, api.Require(Use), api.Body(snippetRequest{}),
		doc("Change a saved command"), api.Returns(200, snippetView{}))
	r.Handle("DELETE /api/v1/terminal/snippets/{id}", m.removeSnippet, api.Require(Use),
		doc("Delete a saved command"), api.Returns(204, nil))
}

// validate trims the name and checks both fields. A command may span lines
// and hold tabs; other control characters (Ctrl-C, Ctrl-D, escape
// sequences) would act on the terminal rather than be typed, so they are
// refused.
func (s *snippetRequest) validate() error {
	s.Name = strings.TrimSpace(s.Name)
	switch n := utf8.RuneCountInString(s.Name); {
	case n == 0:
		return &auth.FieldError{Field: "name", Rule: "required"}
	case n > snippetNameMax:
		return &auth.FieldError{Field: "name", Rule: "max_length", Max: snippetNameMax}
	}
	if strings.ContainsFunc(s.Name, unicode.IsControl) || !utf8.ValidString(s.Name) {
		return &auth.FieldError{Field: "name", Rule: "pattern"}
	}
	if strings.TrimSpace(s.Command) == "" {
		return &auth.FieldError{Field: "command", Rule: "required"}
	}
	if len(s.Command) > snippetCommandMax {
		return &auth.FieldError{Field: "command", Rule: "max_length", Max: snippetCommandMax}
	}
	bad := func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }
	if !utf8.ValidString(s.Command) || strings.ContainsFunc(s.Command, bad) {
		return &auth.FieldError{Field: "command", Rule: "pattern"}
	}
	return nil
}

func snippetID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errSnippetNotFound
	}
	return id, nil
}

// decodeSnippet reads and checks the body. Line ends are stored as "\n";
// the page turns them into what the terminal expects.
func decodeSnippet(r *http.Request) (snippetRequest, error) {
	var req snippetRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		return req, err
	}
	req.Command = strings.ReplaceAll(req.Command, "\r\n", "\n")
	if err := req.validate(); err != nil {
		return req, modutil.MapError(err, nil)
	}
	return req, nil
}

func uniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (m *Module) snippets(w http.ResponseWriter, r *http.Request) error {
	rows, err := m.DB.QueryContext(r.Context(),
		`SELECT id, name, command, updated_at FROM terminal_snippets WHERE user_id = ? ORDER BY name COLLATE NOCASE, id`,
		modutil.ActorOf(r).UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	resp := snippetsResponse{Snippets: []snippetView{}}
	for rows.Next() {
		var s snippetView
		if err := rows.Scan(&s.ID, &s.Name, &s.Command, &s.UpdatedAt); err != nil {
			return err
		}
		resp.Snippets = append(resp.Snippets, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusOK, resp)
}

func (m *Module) addSnippet(w http.ResponseWriter, r *http.Request) error {
	req, err := decodeSnippet(r)
	if err != nil {
		return err
	}
	user := modutil.ActorOf(r).UserID
	tx, err := m.DB.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var count, same int
	if err := tx.QueryRowContext(r.Context(),
		`SELECT count(*), coalesce(sum(name = ?), 0) FROM terminal_snippets WHERE user_id = ?`, req.Name, user).Scan(&count, &same); err != nil {
		return err
	}
	if same > 0 {
		return errSnippetExists
	}
	if count >= snippetsMax {
		return api.NewError(http.StatusConflict, "terminal.too_many_snippets", map[string]any{"max": snippetsMax})
	}
	now := time.Now().Unix()
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO terminal_snippets (user_id, name, command, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		user, req.Name, req.Command, now, now)
	if uniqueViolation(err) {
		return errSnippetExists
	}
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return api.WriteJSON(w, http.StatusCreated, snippetView{ID: id, Name: req.Name, Command: req.Command, UpdatedAt: now})
}

func (m *Module) updateSnippet(w http.ResponseWriter, r *http.Request) error {
	id, err := snippetID(r)
	if err != nil {
		return err
	}
	req, err := decodeSnippet(r)
	if err != nil {
		return err
	}
	user := modutil.ActorOf(r).UserID
	var other int
	if err := m.DB.QueryRowContext(r.Context(),
		`SELECT count(*) FROM terminal_snippets WHERE user_id = ? AND name = ? AND id <> ?`, user, req.Name, id).Scan(&other); err != nil {
		return err
	}
	if other > 0 {
		return errSnippetExists
	}
	now := time.Now().Unix()
	res, err := m.DB.ExecContext(r.Context(),
		`UPDATE terminal_snippets SET name = ?, command = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		req.Name, req.Command, now, id, user)
	if uniqueViolation(err) {
		return errSnippetExists
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errSnippetNotFound
	}
	return api.WriteJSON(w, http.StatusOK, snippetView{ID: id, Name: req.Name, Command: req.Command, UpdatedAt: now})
}

func (m *Module) removeSnippet(w http.ResponseWriter, r *http.Request) error {
	id, err := snippetID(r)
	if err != nil {
		return err
	}
	res, err := m.DB.ExecContext(r.Context(), `DELETE FROM terminal_snippets WHERE id = ? AND user_id = ?`, id, modutil.ActorOf(r).UserID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return errSnippetNotFound
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
