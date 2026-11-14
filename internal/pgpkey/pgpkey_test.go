// SPDX-License-Identifier: AGPL-3.0-only

package pgpkey

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: reference computation of v4 fingerprints.
	"encoding/hex"
	"strings"
	"testing"
)

// pubBody is a version 4 RSA public key packet body (arbitrary numbers).
func pubBody(seed byte) []byte {
	b := []byte{4, 0x65, 0x00, 0x00, seed, 1}   // version, creation time, RSA
	b = append(b, 0x00, 0x10, seed, 0x01)       // MPI n (16 bits)
	b = append(b, 0x00, 0x11, 0x01, 0x00, 0x01) // MPI e = 65537
	return b
}

func newFormat(tag byte, body []byte) []byte {
	return append([]byte{0xC0 | tag, byte(len(body))}, body...)
}

func oldFormat(tag byte, body []byte) []byte {
	return append([]byte{0x80 | tag<<2 | 1, byte(len(body) >> 8), byte(len(body))}, body...)
}

func want(body []byte) string {
	h := sha1.New() //nolint:gosec // G401: reference computation.
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

func TestFingerprints(t *testing.T) {
	userID := newFormat(13, []byte("Test <test@cairn.invalid>"))
	sig := newFormat(2, []byte{4, 0x13, 1, 8, 0, 0, 0, 0})
	sub := newFormat(14, pubBody(9)) // subkeys are not listed
	file := bytes.Join([][]byte{oldFormat(tagPublic, pubBody(1)), userID, sig, sub, newFormat(tagPublic, pubBody(2))}, nil)

	for name, data := range map[string][]byte{"binary": file, "armored": Armor(file)} {
		got, err := Fingerprints(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 2 || got[0] != want(pubBody(1)) || got[1] != want(pubBody(2)) {
			t.Fatalf("%s: %v", name, got)
		}
	}
	armored := Armor(file)
	if !IsArmored(armored) || IsArmored(file) {
		t.Fatal("IsArmored")
	}
	if back, err := Binary(armored); err != nil || !bytes.Equal(back, file) {
		t.Fatalf("armor round trip: %v", err)
	}
	// Armor headers are skipped.
	withHeader := bytes.Replace(armored, []byte("BLOCK-----\n\n"), []byte("BLOCK-----\nComment: hello\n\n"), 1)
	if got, err := Fingerprints(withHeader); err != nil || len(got) != 2 {
		t.Fatalf("headers: %v %v", got, err)
	}
}

func TestRejects(t *testing.T) {
	good := newFormat(tagPublic, pubBody(1))
	armored := Armor(good)
	corrupt := bytes.Replace(armored, []byte("\n="), []byte("\n=AAAA"), 1)
	v5 := pubBody(1)
	v5[0] = 5
	for name, data := range map[string][]byte{
		"empty":           {},
		"text":            []byte("not a key"),
		"truncated":       good[:len(good)-3],
		"no public key":   newFormat(13, []byte("only a user id")),
		"bad checksum":    corrupt,
		"version 5":       newFormat(tagPublic, v5),
		"partial lengths": {0xC6, 0xE1, 0x00},
	} {
		if _, err := Fingerprints(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
