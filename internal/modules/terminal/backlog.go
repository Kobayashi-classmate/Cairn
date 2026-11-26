// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"bytes"
	"unicode/utf8"
)

// lineSearch bounds how far past the cut trimBacklog looks for a line end.
const lineSearch = 4 << 10

// trimBacklog keeps at most the last limit bytes of buf, the output a
// reconnecting browser is sent first. The cut is moved to where a replay
// starts cleanly: just after the next line end when one comes within
// lineSearch bytes, so neither a character nor an escape sequence is cut
// in two; otherwise to the next UTF-8 character start, so the replay never
// begins with half a character.
func trimBacklog(buf []byte, limit int) []byte {
	over := len(buf) - limit
	if over <= 0 {
		return buf
	}
	rest := buf[over:]
	if buf[over-1] != '\n' {
		if i := bytes.IndexByte(rest[:min(len(rest), lineSearch)], '\n'); i >= 0 {
			rest = rest[i+1:]
		} else {
			for skipped := 0; skipped < utf8.UTFMax-1 && len(rest) > 0 && !utf8.RuneStart(rest[0]); skipped++ {
				rest = rest[1:]
			}
		}
	}
	return append([]byte(nil), rest...)
}
