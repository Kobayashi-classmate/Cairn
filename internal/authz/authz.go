// SPDX-License-Identifier: AGPL-3.0-only

// Package authz decides what a role may do. Every non-public API route
// declares the permission it needs; a route without one cannot be registered
// (default deny). The roles are fixed for now; custom roles come later.
//
// The platform's own permissions are defined here. Feature modules define
// theirs at startup with Define, together with the roles that get them by
// default; administrators always have every permission.
package authz

import (
	"slices"
	"sort"
	"sync"
)

// Permission is a capability checked on the server for every request.
type Permission string

// Platform permissions.
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

var (
	mu      sync.RWMutex
	defined = []Permission{} // display order, without Self
	roles   = map[Permission][]Role{}
	grants  = map[Role]map[Permission]bool{Admin: nil, Operator: {}, Auditor: {}}
)

func init() {
	Define(SystemRead, Operator, Auditor)
	Define(AuditRead, Auditor)
	Define(UsersManage)
	Define(SettingsRead, Operator, Auditor)
	Define(SettingsManage)
	Define(NotifyManage)
	for _, r := range []Role{Operator, Auditor} {
		grants[r][Self] = true
	}
}

// Define adds a permission and grants it to the given roles (administrators
// have every permission anyway). It is called at startup. Defining a
// permission again with the same roles does nothing (tests build the panel
// many times); with different roles it is a programming error and panics.
func Define(p Permission, to ...Role) {
	mu.Lock()
	defer mu.Unlock()
	if p == "" || p == Self {
		panic("authz: invalid permission " + string(p))
	}
	want := slices.Clone(to)
	slices.Sort(want)
	if have, ok := roles[p]; ok {
		if !slices.Equal(have, want) {
			panic("authz: permission " + string(p) + " defined twice with different roles")
		}
		return
	}
	roles[p] = want
	defined = append(defined, p)
	for _, r := range to {
		g, ok := grants[r]
		if !ok {
			panic("authz: unknown role " + string(r))
		}
		if g != nil {
			g[p] = true
		}
	}
}

// Defined reports whether p is a known permission (Self included).
func Defined(p Permission) bool {
	mu.RLock()
	defer mu.RUnlock()
	return p == Self || slices.Contains(defined, p)
}

// All lists every permission except Self, in display order.
func All() []Permission {
	mu.RLock()
	defer mu.RUnlock()
	return slices.Clone(defined)
}

// ValidRole reports whether role is a built-in role.
func ValidRole(role string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := grants[Role(role)]
	return ok
}

// Allows reports whether role has permission p. Unknown roles and unknown
// permissions have none.
func Allows(role string, p Permission) bool {
	mu.RLock()
	defer mu.RUnlock()
	g, ok := grants[Role(role)]
	if !ok || (p != Self && !slices.Contains(defined, p)) {
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
	for _, p := range All() {
		if Allows(role, p) {
			out = append(out, string(p))
		}
	}
	sort.Strings(out)
	return out
}
