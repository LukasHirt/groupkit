// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// maxFileNameLen is the longest encoded name that is used as is.
const maxFileNameLen = 200

// fileName turns any object, collection or owner name into a file name that is
// safe on common file systems. The mapping is injective, also after
// lower-casing, so names that differ only by case never collide on
// case-insensitive file systems.
//
// Bytes a-z, 0-9, '-' and '_' are kept. A '.' is kept unless it is the first or
// last byte. Every other byte becomes '%' followed by two upper-case hex
// digits. Results longer than maxFileNameLen are replaced by '~' and the hex
// SHA-256 of the name; '~' is always escaped otherwise, so the two forms never
// collide.
func fileName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		case c == '.' && i > 0 && i < len(s)-1:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	out := b.String()

	base, _, _ := strings.Cut(out, ".")
	if isDeviceName(base) {
		out = fmt.Sprintf("%%%02X", out[0]) + out[1:]
	}

	if len(out) > maxFileNameLen {
		sum := sha256.Sum256([]byte(s))
		return "~" + hex.EncodeToString(sum[:])
	}
	return out
}

// isDeviceName reports whether s is a reserved Windows device name.
func isDeviceName(s string) bool {
	switch s {
	case "con", "prn", "aux", "nul":
		return true
	}
	if len(s) == 4 && (strings.HasPrefix(s, "com") || strings.HasPrefix(s, "lpt")) {
		return s[3] >= '1' && s[3] <= '9'
	}
	return false
}
