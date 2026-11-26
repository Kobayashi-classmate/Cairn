// SPDX-License-Identifier: AGPL-3.0-only

package terminal

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTrimBacklog(t *testing.T) {
	// Within the limit nothing is cut.
	if got := trimBacklog([]byte("abc\n"), 10); string(got) != "abc\n" {
		t.Fatalf("short: %q", got)
	}

	// A cut inside a line moves to the start of the next one, past any
	// escape sequence or character it would split.
	buf := []byte("first \x1b[31mred\x1b[0m line\r\nsecond\r\n")
	if got := trimBacklog(buf, len(buf)-8); string(got) != "second\r\n" {
		t.Fatalf("mid-line: %q", got)
	}

	// A cut that falls on a line start keeps that line.
	buf = []byte("one\ntwo\n")
	if got := trimBacklog(buf, 4); string(got) != "two\n" {
		t.Fatalf("line start: %q", got)
	}

	// With no line end near the cut it moves to the next character start:
	// a CJK ideograph is three bytes, and the cut lands after its first.
	const ideograph = string(rune(0x4e2d))
	buf = []byte(strings.Repeat(ideograph, lineSearch))
	got := trimBacklog(buf, len(buf)-1)
	if !utf8.Valid(got) || len(got) != len(buf)-3 || !bytes.HasPrefix(got, []byte(ideograph)) {
		t.Fatalf("mid-character: %d bytes, valid %v", len(got), utf8.Valid(got))
	}

	// A line end too far past the cut is not waited for.
	buf = []byte("é" + strings.Repeat("x", lineSearch+10) + "\nrest")
	got = trimBacklog(buf, len(buf)-1)
	if !bytes.HasPrefix(got, []byte("xxx")) || !bytes.HasSuffix(got, []byte("\nrest")) {
		t.Fatalf("far line end: %q…", got[:10])
	}

	// The result never shares memory with the input.
	buf = []byte("aaaa\nbbbb\n")
	got = trimBacklog(buf, 5)
	got[0] = 'z'
	if buf[5] != 'b' {
		t.Fatal("the backlog aliases the read buffer")
	}
}
