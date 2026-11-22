// SPDX-License-Identifier: AGPL-3.0-only

package modutil

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/api"
	"github.com/Kobayashi-classmate/Cairn/internal/auth"
)

func TestBusyHashingIsServiceUnavailable(t *testing.T) {
	var ae *api.Error
	if err := MapError(fmt.Errorf("sign-in: %w", auth.ErrBusy), nil); !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable || ae.Code != "auth.busy" {
		t.Fatalf("mapped to %v", err)
	}
}
