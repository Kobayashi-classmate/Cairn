// SPDX-License-Identifier: AGPL-3.0-only

package helperapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// FDHeader marks a helper response that carries a file descriptor.
const FDHeader = "X-Cairn-FD"

// maxFDResponse bounds the JSON part of a response that carries a file.
const maxFDResponse = 1 << 20

// CallFD invokes an action that answers with an open file (a file to
// serve, a terminal's master side) besides its JSON result. The file
// travels over the socket as SCM_RIGHTS ancillary data, so the web process
// gets the descriptor itself, not the right to open the path. The file is
// nil when the action failed.
func (c *Client) CallFD(ctx context.Context, action string, caller Caller, req, resp any) (*os.File, error) {
	if req == nil {
		req = Empty{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, &Error{Code: CodeUnavailable}
	}
	defer func() { _ = conn.Close() }()
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, errors.New("helperapi: not a unix connection")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Minute)
	}
	_ = uc.SetDeadline(deadline)
	hreq, err := http.NewRequest(http.MethodPost, "http://helper/v1/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set(CallerHeader, caller.Encode())
	hreq.Close = true
	if err := hreq.Write(uc); err != nil {
		return nil, &Error{Code: CodeUnavailable}
	}
	data, files, err := readWithRights(uc)
	closeAll := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	if err != nil {
		closeAll()
		return nil, err
	}
	hres, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(data)), hreq)
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("helperapi: %s: %w", action, err)
	}
	defer hres.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(hres.Body, maxFDResponse))
	if hres.StatusCode != http.StatusOK {
		closeAll()
		var he Error
		if json.Unmarshal(payload, &he) == nil && he.Code != "" {
			return nil, &he
		}
		return nil, fmt.Errorf("helperapi: %s: unexpected status %d", action, hres.StatusCode)
	}
	if hres.Header.Get(FDHeader) != "1" || len(files) != 1 {
		closeAll()
		return nil, fmt.Errorf("helperapi: %s: response without a file", action)
	}
	if resp != nil {
		if err := json.Unmarshal(payload, resp); err != nil {
			closeAll()
			return nil, fmt.Errorf("helperapi: %s: decode response: %w", action, err)
		}
	}
	return files[0], nil
}

// readWithRights reads the whole response and the descriptors sent with
// it, as non-blocking files (so that closing one ends a pending read).
func readWithRights(uc *net.UnixConn) ([]byte, []*os.File, error) {
	var data bytes.Buffer
	var files []*os.File
	buf := make([]byte, 64<<10)
	oob := make([]byte, unix.CmsgSpace(4*4))
	for data.Len() <= maxFDResponse+64<<10 {
		n, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
		data.Write(buf[:n])
		if oobn > 0 {
			msgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
			if perr == nil {
				for _, m := range msgs {
					fds, ferr := unix.ParseUnixRights(&m)
					if ferr != nil {
						continue
					}
					for _, fd := range fds {
						unix.CloseOnExec(fd)
						_ = unix.SetNonblock(fd, true)
						files = append(files, os.NewFile(uintptr(fd), "helper-fd"))
					}
				}
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && n == 0 && oobn == 0) {
			return data.Bytes(), files, nil
		}
		if err != nil {
			return data.Bytes(), files, err
		}
	}
	return data.Bytes(), files, errors.New("helperapi: response too large")
}
