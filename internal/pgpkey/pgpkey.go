// SPDX-License-Identifier: AGPL-3.0-only

// Package pgpkey reads OpenPGP public key files just far enough to list the
// fingerprints of their primary keys (RFC 4880: a version 4 fingerprint is
// SHA-1 over 0x99, the two-byte body length and the public key packet body).
// The helper uses it to check a downloaded repository key against the
// fingerprint pinned in the software catalog before trusting it. It does
// not verify signatures; apt and dnf do that with the trusted key.
package pgpkey

import (
	"bytes"
	"crypto/sha1" //nolint:gosec // G505: SHA-1 is how OpenPGP v4 defines fingerprints.
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	beginArmor = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	endArmor   = "-----END PGP PUBLIC KEY BLOCK-----"
	tagPublic  = 6
	maxKeyFile = 1 << 20
)

// ErrNoKey means the data holds no public key.
var ErrNoKey = errors.New("pgpkey: no public key found")

// IsArmored reports whether data is an ASCII-armored key block.
func IsArmored(data []byte) bool {
	return bytes.Contains(data, []byte(beginArmor))
}

// Binary returns the packets of data, removing ASCII armor if present.
func Binary(data []byte) ([]byte, error) {
	if len(data) > maxKeyFile {
		return nil, errors.New("pgpkey: key file too large")
	}
	if !IsArmored(data) {
		return data, nil
	}
	text := string(data)
	start := strings.Index(text, beginArmor)
	end := strings.Index(text, endArmor)
	if end < start {
		return nil, errors.New("pgpkey: unterminated armor")
	}
	// The first element is what follows the BEGIN marker on its own line.
	lines := strings.Split(text[start+len(beginArmor):end], "\n")[1:]
	var body strings.Builder
	var crc string
	inHeaders := true
	for _, l := range lines {
		l = strings.TrimSpace(l)
		switch {
		case inHeaders:
			// Armor headers ("Version: ...") end at the first blank line;
			// some files have no headers at all.
			if l == "" {
				inHeaders = false
			} else if !strings.Contains(l, ": ") {
				inHeaders = false
				body.WriteString(l)
			}
		case strings.HasPrefix(l, "="):
			crc = l[1:]
		default:
			body.WriteString(l)
		}
	}
	raw, err := base64.StdEncoding.DecodeString(body.String())
	if err != nil {
		return nil, fmt.Errorf("pgpkey: bad armor: %w", err)
	}
	if crc != "" {
		want, err := base64.StdEncoding.DecodeString(crc)
		if err != nil || len(want) != 3 {
			return nil, errors.New("pgpkey: bad armor checksum")
		}
		if !bytes.Equal(want, crcBytes(crc24(raw))) {
			return nil, errors.New("pgpkey: armor checksum mismatch")
		}
	}
	return raw, nil
}

// Armor encodes packets as an ASCII-armored public key block (rpm needs
// armored keys).
func Armor(packets []byte) []byte {
	var b bytes.Buffer
	b.WriteString(beginArmor + "\n\n")
	enc := base64.StdEncoding.EncodeToString(packets)
	for len(enc) > 64 {
		b.WriteString(enc[:64] + "\n")
		enc = enc[64:]
	}
	b.WriteString(enc + "\n")
	b.WriteString("=" + base64.StdEncoding.EncodeToString(crcBytes(crc24(packets))) + "\n")
	b.WriteString(endArmor + "\n")
	return b.Bytes()
}

// Fingerprints lists the upper-case hex fingerprints of the primary keys in
// data (armored or binary). Subkeys are not listed: pinning applies to the
// primary key. Keys other than version 4 are rejected.
func Fingerprints(data []byte) ([]string, error) {
	raw, err := Binary(data)
	if err != nil {
		return nil, err
	}
	var out []string
	for len(raw) > 0 {
		tag, body, rest, err := nextPacket(raw)
		if err != nil {
			return nil, err
		}
		raw = rest
		if tag != tagPublic {
			continue
		}
		if len(body) == 0 || body[0] != 4 {
			return nil, errors.New("pgpkey: only version 4 keys are supported")
		}
		if len(body) > 0xFFFF {
			return nil, errors.New("pgpkey: key packet too large for a version 4 fingerprint")
		}
		var hdr [3]byte
		hdr[0] = 0x99
		binary.BigEndian.PutUint16(hdr[1:], uint16(len(body))) //nolint:gosec // G115: bounded above.
		h := sha1.New()                                        //nolint:gosec // G401: OpenPGP v4 fingerprint definition.
		h.Write(hdr[:])
		h.Write(body)
		out = append(out, strings.ToUpper(hex.EncodeToString(h.Sum(nil))))
	}
	if len(out) == 0 {
		return nil, ErrNoKey
	}
	return out, nil
}

// nextPacket splits off one packet (old or new format header).
func nextPacket(data []byte) (tag int, body, rest []byte, err error) {
	if len(data) < 2 || data[0]&0x80 == 0 {
		return 0, nil, nil, errors.New("pgpkey: not an OpenPGP packet")
	}
	h := data[0]
	var n, hdr int
	if h&0x40 != 0 { // new format
		tag = int(h & 0x3f)
		l1 := int(data[1])
		switch {
		case l1 < 192:
			n, hdr = l1, 2
		case l1 < 224:
			if len(data) < 3 {
				return 0, nil, nil, errors.New("pgpkey: truncated packet")
			}
			n, hdr = ((l1-192)<<8)+int(data[2])+192, 3
		case l1 == 255:
			if len(data) < 6 {
				return 0, nil, nil, errors.New("pgpkey: truncated packet")
			}
			n, hdr = int(binary.BigEndian.Uint32(data[2:6])), 6
		default:
			return 0, nil, nil, errors.New("pgpkey: partial body lengths are not valid in key files")
		}
	} else { // old format
		tag = int(h>>2) & 0x0f
		switch h & 3 {
		case 0:
			n, hdr = int(data[1]), 2
		case 1:
			if len(data) < 3 {
				return 0, nil, nil, errors.New("pgpkey: truncated packet")
			}
			n, hdr = int(binary.BigEndian.Uint16(data[1:3])), 3
		case 2:
			if len(data) < 5 {
				return 0, nil, nil, errors.New("pgpkey: truncated packet")
			}
			n, hdr = int(binary.BigEndian.Uint32(data[1:5])), 5
		default:
			return 0, nil, nil, errors.New("pgpkey: indeterminate packet length")
		}
	}
	if n < 0 || hdr+n > len(data) {
		return 0, nil, nil, errors.New("pgpkey: truncated packet")
	}
	return tag, data[hdr : hdr+n], data[hdr+n:], nil
}

// crcBytes returns the three bytes of a CRC-24.
func crcBytes(c uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], c)
	return b[1:]
}

// crc24 is the armor checksum of RFC 4880 section 6.1.
func crc24(data []byte) uint32 {
	crc := uint32(0xB704CE)
	for _, b := range data {
		crc ^= uint32(b) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return crc & 0xFFFFFF
}
