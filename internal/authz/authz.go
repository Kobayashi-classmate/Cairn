// SPDX-License-Identifier: AGPL-3.0-only

// Package authz decides what a role may do. Every non-public API route
// declares the permission it needs; a route without one cannot be registered
// (default deny). P0 ships three fixed roles; custom roles come later.
package authz

import "sort"

// Permission is a capability checked on the server for every request.
type Permission string

// Permissions known in P0. Modules add their own as they land.
const (
	// Self covers a user's own account (language, password, sessions, 2FA);
	// every role has it.
	Self           Permission = "self"
	SystemRead     Permission = "system.read"
	AuditRead      Permission = "audit.read"
	UsersManage    Permission = "users.manage"
	SettingsRead   Permission = "settings.read"
	SettingsManage Permission = "settings.manage"
	NotifyManage   Permission = "notify.manage"
)

// Role is a named set of permissions.
type Role string

// Built-in roles.
const (
	Admin    Role = "admin"
	Operator Role = "operator"
	Auditor  Role = "auditor"
)

// Roles lists the built-in roles in display order.
var Roles = []Role{Admin, Operator, Auditor}

var grants = map[Role]map[Permission]bool{
	Admin: nil, // nil means every permission
	Operator: {
		Self: true, SystemRead: true, SettingsRead: true,
	},
	Auditor: {
		Self: true, SystemRead: true, AuditRead: true, SettingsRead: true,
	},
}

// All lists every permission, for administrators and the UI.
var All = []Permission{SystemRead, AuditRead, UsersManage, SettingsRead, SettingsManage, NotifyManage}

// ValidRole reports whether role is a built-in role.
func ValidRole(role string) bool {
	_, ok := grants[Role(role)]
	return ok
}

// Allows reports whether role has permission p. Unknown roles have none.
func Allows(role string, p Permission) bool {
	g, ok := grants[Role(role)]
	if !ok {
		return false
	}
	if g == nil {
		return true
	}
	return g[p]
}

// PermissionsOf lists the permissions of role (excluding Self), sorted.
func PermissionsOf(role string) []string {
	out := []string{}
	for _, p := range All {
		if Allows(role, p) {
			out = append(out, string(p))
		}
	}
	sort.Strings(out)
	return out
}
