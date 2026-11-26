// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"testing"

	"github.com/Kobayashi-classmate/Cairn/internal/auth"
	"github.com/Kobayashi-classmate/Cairn/internal/execx"
)

// A terminal ends with the browser session that opened it (at the next
// sweep at the latest), so the longest a shell's unit may run is past the
// longest a session lasts: the limit ends only shells that outlived their
// terminal, never one in use.
func TestTerminalUnitsOutliveTheirSession(t *testing.T) {
	if longest := auth.AbsoluteTimeout + reapInterval + detachedTTL; execx.TerminalMaxRuntime <= longest {
		t.Fatalf("a terminal's unit may run %v, a terminal in use %v", execx.TerminalMaxRuntime, longest)
	}
}
