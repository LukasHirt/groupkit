// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package collation implements the text collations used by CardDAV and
// CalDAV text matching (RFC 4790, RFC 5051).
package collation

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

// ErrUnknown is wrapped by the error Get returns for an unsupported collation.
var ErrUnknown = errors.New("collation: unknown collation")

const (
	octet          = "i;octet"
	asciiCasemap   = "i;ascii-casemap"
	unicodeCasemap = "i;unicode-casemap"
)

// Collation compares strings under one named collation. It is safe for
// concurrent use.
type Collation struct {
	name string
	fold func(string) string
}

// Get returns the collation with the given name, matched with ASCII letters
// folded (clients vary the case). Nothing else is normalized, so surrounding
// whitespace is an error. The empty name selects i;unicode-casemap, the
// protocol default.
func Get(name string) (Collation, error) {
	switch foldASCII(name) {
	case octet:
		return Collation{name: octet, fold: identity}, nil
	case asciiCasemap:
		return Collation{name: asciiCasemap, fold: foldASCII}, nil
	case unicodeCasemap, "":
		return Collation{name: unicodeCasemap, fold: foldUnicode}, nil
	}
	return Collation{}, fmt.Errorf("%w: %q", ErrUnknown, clip(name))
}

// clip shortens a name quoted in an error to at most 40 bytes, without
// cutting a UTF-8 character.
func clip(s string) string {
	const n = 40
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "..."
}

// Fold returns s in the form the collation compares: Equal(a, b) is
// Fold(a) == Fold(b), and Contains, HasPrefix and HasSuffix are their
// strings counterparts on folded text. Folding a long text once and
// comparing it many times saves work.
func (c Collation) Fold(s string) string { return c.fold(s) }

// Name returns the collation identifier.
func (c Collation) Name() string { return c.name }

// Equal reports whether a and b are equal under the collation.
func (c Collation) Equal(a, b string) bool { return c.fold(a) == c.fold(b) }

// Contains reports whether sub occurs within s. An empty sub always matches.
func (c Collation) Contains(s, sub string) bool {
	return strings.Contains(c.fold(s), c.fold(sub))
}

// HasPrefix reports whether s starts with prefix. An empty prefix always matches.
func (c Collation) HasPrefix(s, prefix string) bool {
	return strings.HasPrefix(c.fold(s), c.fold(prefix))
}

// HasSuffix reports whether s ends with suffix. An empty suffix always matches.
func (c Collation) HasSuffix(s, suffix string) bool {
	return strings.HasSuffix(c.fold(s), c.fold(suffix))
}

func identity(s string) string { return s }

// foldASCII lowercases A-Z and leaves every other byte untouched.
func foldASCII(s string) string {
	i := 0
	for ; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			break
		}
	}
	if i == len(s) {
		return s
	}
	b := []byte(s)
	for ; i < len(b); i++ {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// foldUnicode applies Unicode case folding. A cases.Caser is not safe for
// concurrent use, so each call makes its own.
func foldUnicode(s string) string { return cases.Fold().String(s) }
