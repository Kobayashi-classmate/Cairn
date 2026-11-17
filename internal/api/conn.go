// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"net"
	"net/http"
	"time"
)

type connStartKey struct{}

// ConnContext stamps each connection with the time it was accepted; set it
// as http.Server.ConnContext. Changes that must be confirmed over a new
// connection (the firewall's) compare it with the change's time.
func ConnContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, connStartKey{}, time.Now())
}

// ConnStarted returns when the request's connection was accepted (the zero
// time when the server does not record it).
func ConnStarted(r *http.Request) time.Time {
	t, _ := r.Context().Value(connStartKey{}).(time.Time)
	return t
}

// CloseConnection makes the client open a new connection for its next
// request: HTTP/1.1 closes this one after the response, HTTP/2 sends
// GOAWAY and finishes the requests already running.
func CloseConnection(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
}
