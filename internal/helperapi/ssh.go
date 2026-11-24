// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"regexp"
	"strings"
)

// SSH: the panel's settings are a drop-in read before every other file
// (sshd keeps the first value it reads), checked with sshd -t and
// compared with sshd -T. A port change listens on the old and the new port
// first; the old one closes when the user says so or when the time is up.
const (
	ActionSSHStatus     = "ssh.status"      // synchronous
	ActionSSHSettings   = "ssh.settings"    // operation
	ActionSSHPort       = "ssh.port"        // operation: add a port, close the old ones later
	ActionSSHPortFinish = "ssh.port.finish" // operation: close the old ports now
	ActionSSHPortCancel = "ssh.port.cancel" // operation: keep the old ports, drop the new one
	ActionSSHKeys       = "ssh.keys"        // synchronous
	ActionSSHKeyAdd     = "ssh.key.add"     // synchronous
	ActionSSHKeyDelete  = "ssh.key.delete"  // synchronous
	ActionSSHLogins     = "ssh.logins"      // synchronous
)

// SSH error codes.
const (
	CodeSSHNotInstalled  = "ssh.not_installed"
	CodeSSHConfigInvalid = "ssh.config_invalid"
	CodeSSHConflict      = "ssh.conflict"
	CodeSSHNoKeys        = "ssh.no_keys"
	CodeSSHPortInUse     = "ssh.port_in_use"
	CodeSSHPortInvalid   = "ssh.port_invalid"
	CodeSSHPending       = "ssh.pending"
	CodeSSHNoPending     = "ssh.no_pending"
	CodeSSHSELinuxPort   = "ssh.selinux_port"
	CodeSSHNotListening  = "ssh.not_listening"
	// CodeSSHPortUnreached: the time of a port change is up and nobody
	// connected to the new port from another host, so the old ports stay.
	CodeSSHPortUnreached = "ssh.port_unreached"
	CodeSSHKeyInvalid    = "ssh.key_invalid"
	CodeSSHKeyExists     = "ssh.key_exists"
	CodeSSHUserNotFound  = "ssh.user_not_found"
	CodeSSHRootDisabled  = "ssh.root_disabled"
)

// PermitRootLogin values the panel sets.
const (
	RootLoginYes      = "yes"
	RootLoginKeysOnly = "prohibit-password"
	RootLoginNo       = "no"
)

// Limits.
const (
	SSHCloseMin     = 5
	SSHCloseMax     = 240
	SSHCloseDefault = 30
	SSHKeyMax       = 16 << 10
	SSHLoginsMax    = 500
)

// Why the timer of a port change left the old ports open.
const (
	// SSHHeldUnreached: no connection from another host reached the new port.
	SSHHeldUnreached = "unreached"
	// SSHHeldNotListening: sshd did not answer on the new port.
	SSHHeldNotListening = "not_listening"
)

// SSHPortPending is a port change in progress.
type SSHPortPending struct {
	OldPorts []int `json:"oldPorts"`
	NewPort  int   `json:"newPort"`
	CloseAt  int64 `json:"closeAt"`
	// ReachedAt is when a connection from another host was first seen on
	// the new port (0: not yet). Only then does the timer close the old
	// ports: that sshd answers on this host says nothing about a firewall
	// or security group in front of it.
	ReachedAt int64 `json:"reachedAt,omitempty"`
	// Held is set when the time ran out and the old ports were kept
	// (SSHHeldUnreached, SSHHeldNotListening): the change waits for
	// someone to finish or cancel it.
	Held string `json:"held,omitempty"`
}

// SSHUser is an account whose keys the panel manages.
type SSHUser struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	Keys int    `json:"keys"`
	Root bool   `json:"root"`
}

// SSHStatus describes the SSH server.
type SSHStatus struct {
	Installed              bool            `json:"installed"`
	Service                string          `json:"service"`
	SocketActivated        bool            `json:"socketActivated"`
	Ports                  []int           `json:"ports"`
	ListenAddresses        []string        `json:"listenAddresses"`
	PasswordAuthentication bool            `json:"passwordAuthentication"`
	PermitRootLogin        string          `json:"permitRootLogin"`
	PubkeyAuthentication   bool            `json:"pubkeyAuthentication"`
	Pending                *SSHPortPending `json:"pending"`
	Users                  []SSHUser       `json:"users"`
	RootKeys               bool            `json:"rootKeys"`
	Firewall               string          `json:"firewall"`
}

// SSHSettingsRequest sets how users may log in.
type SSHSettingsRequest struct {
	PasswordAuthentication bool   `json:"passwordAuthentication"`
	PermitRootLogin        string `json:"permitRootLogin"`
}

// Validate implements the helper's request validation hook.
func (r SSHSettingsRequest) Validate() error {
	switch r.PermitRootLogin {
	case RootLoginYes, RootLoginKeysOnly, RootLoginNo:
		return nil
	}
	return invalid("permitRootLogin", "oneof")
}

// SSHPortRequest adds a port; the old ones close after CloseAfterMinutes.
type SSHPortRequest struct {
	Port              int `json:"port"`
	CloseAfterMinutes int `json:"closeAfterMinutes"`
}

// Validate implements the helper's request validation hook.
func (r SSHPortRequest) Validate() error {
	if r.Port < 1 || r.Port > 65535 {
		return &Error{Code: CodeSSHPortInvalid, Params: map[string]any{"port": r.Port}}
	}
	if r.CloseAfterMinutes < SSHCloseMin || r.CloseAfterMinutes > SSHCloseMax {
		return invalid("closeAfterMinutes", "range")
	}
	return nil
}

var sshUser = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// SSHKeysRequest names whose keys to list.
type SSHKeysRequest struct {
	User string `json:"user"`
}

// Validate implements the helper's request validation hook.
func (r SSHKeysRequest) Validate() error {
	if !sshUser.MatchString(r.User) {
		return invalid("user", "posix_name")
	}
	return nil
}

// SSHKey is one authorized key.
type SSHKey struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
	Options     string `json:"options"`
}

// SSHKeys lists a user's authorized keys.
type SSHKeys struct {
	Keys []SSHKey `json:"keys"`
}

// SSHKeyAddRequest authorizes a public key ("ssh-ed25519 AAAA… comment").
type SSHKeyAddRequest struct {
	User string `json:"user"`
	Key  string `json:"key"`
}

// Validate implements the helper's request validation hook.
func (r SSHKeyAddRequest) Validate() error {
	if !sshUser.MatchString(r.User) {
		return invalid("user", "posix_name")
	}
	if k := strings.TrimSpace(r.Key); k == "" || len(k) > SSHKeyMax || strings.ContainsAny(k, "\x00\r\n") {
		return &Error{Code: CodeSSHKeyInvalid}
	}
	return nil
}

var sshFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// SSHKeyDeleteRequest removes a key by its SHA256 fingerprint.
type SSHKeyDeleteRequest struct {
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
}

// Validate implements the helper's request validation hook.
func (r SSHKeyDeleteRequest) Validate() error {
	if !sshUser.MatchString(r.User) {
		return invalid("user", "posix_name")
	}
	if !sshFingerprint.MatchString(r.Fingerprint) {
		return invalid("fingerprint", "fingerprint")
	}
	return nil
}

// Login results.
const (
	LoginAccepted    = "accepted"
	LoginFailed      = "failed"
	LoginInvalidUser = "invalid_user"
)

// SSHLoginsRequest pages through login attempts: Before (a cursor from a
// previous answer) lists older ones newest first; After lists newer ones
// oldest first (for notifications).
type SSHLoginsRequest struct {
	Limit  int    `json:"limit,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

var loginCursor = regexp.MustCompile(`^[0-9]{1,20}$`)

// Validate implements the helper's request validation hook.
func (r SSHLoginsRequest) Validate() error {
	if r.Limit < 0 || r.Limit > SSHLoginsMax {
		return invalid("limit", "range")
	}
	for f, v := range map[string]string{"before": r.Before, "after": r.After} {
		if v != "" && !loginCursor.MatchString(v) {
			return invalid(f, "cursor")
		}
	}
	return nil
}

// SSHLogin is one login attempt; Time is in unix seconds.
type SSHLogin struct {
	Time   int64  `json:"time"`
	User   string `json:"user"`
	IP     string `json:"ip"`
	Method string `json:"method"`
	Result string `json:"result"`
}

// SSHLogins is a page of login attempts; Next continues it ("" at the
// end), Latest is the cursor of the newest entry seen.
type SSHLogins struct {
	Entries []SSHLogin `json:"entries"`
	Next    string     `json:"next"`
	Latest  string     `json:"latest"`
}
