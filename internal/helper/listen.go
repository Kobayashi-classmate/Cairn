// SPDX-License-Identifier: AGPL-3.0-only

package helper

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// listenFDsStart is the first file descriptor passed by systemd socket
// activation (sd_listen_fds(3)).
const listenFDsStart = 3

// Listen returns the helper's listening socket. Under systemd the socket is
// inherited through socket activation (the .socket unit sets the owner and
// mode). Otherwise, for development, the socket is created at path with mode
// 0660 and group gid.
func Listen(path string, gid int) (net.Listener, error) {
	if l, err := activationListener(); l != nil || err != nil {
		return l, err
	}
	// The socket directory must be traversable by the cairn account; access
	// control is on the socket itself (0660, root:cairn) and on SO_PEERCRED.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: see above.
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil { //nolint:gosec // G302: the cairn group must be able to connect.
		_ = l.Close()
		return nil, err
	}
	if gid >= 0 {
		if err := os.Chown(path, 0, gid); err != nil {
			_ = l.Close()
			return nil, err
		}
	}
	return l, nil
}

// activationListener returns the socket passed by systemd, or nil when the
// process was not socket-activated.
func activationListener() (net.Listener, error) {
	pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
	if err != nil || pid != os.Getpid() {
		return nil, nil
	}
	n, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil || n < 1 {
		return nil, fmt.Errorf("helper: invalid LISTEN_FDS %q", os.Getenv("LISTEN_FDS"))
	}
	// Do not leak activation variables to child processes.
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(k)
	}
	f := os.NewFile(uintptr(listenFDsStart), "helper.sock")
	defer f.Close()
	return net.FileListener(f)
}
