// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package contentline parses the content lines shared by vCard (RFC 6350)
// and iCalendar (RFC 5545): unfolding, group, name, parameters and a raw value.
package contentline

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrSyntax is wrapped by every error Parse returns.
var ErrSyntax = errors.New("contentline: syntax error")

// Param is a property parameter. Name is upper-cased. Every double quote in
// a value is removed, not only the surrounding ones: quotes only stop ',', ';'
// and ':' from ending the value, so CN=John "J" Doe gives "John J Doe".
type Param struct {
	Name   string
	Values []string
}

// Line is one logical content line. Group and Name are upper-cased, Group is
// empty when absent, and Value is left exactly as it appeared after unfolding.
// Group, Name and parameter names are tokens: ASCII letters, digits and '-'.
type Line struct {
	Group, Name string
	Params      []Param
	Value       string
}

// Param returns the values of the named parameter, or nil when the line has
// no such parameter. Only ASCII letters are compared case-insensitively.
func (l Line) Param(name string) []string {
	for _, p := range l.Params {
		if EqualFoldASCII(p.Name, name) {
			return p.Values
		}
	}
	return nil
}

// Limits bounds the work Parse does on untrusted input. A zero field selects
// the value from DefaultLimits.
type Limits struct {
	MaxLines     int // physical lines, blank and continuation lines included
	MaxLineBytes int // bytes in one unfolded logical line
}

// DefaultLimits are the limits used when the caller has no stricter ones.
// MaxLineBytes is a backstop well above any object size a server accepts, so
// a large contact photo on one folded line still parses; callers bound the
// size of the whole object separately.
var DefaultLimits = Limits{MaxLines: 100000, MaxLineBytes: 16 << 20}

// maxFragment is the most bytes of input an error message quotes.
const maxFragment = 40

// Clip shortens s to at most 40 bytes, without cutting a UTF-8 character,
// and marks a shortened string with "...". Use it for input quoted in error
// messages.
func Clip(s string) string {
	if len(s) <= maxFragment {
		return s
	}
	n := maxFragment
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

var bom = []byte{0xef, 0xbb, 0xbf}

func syntaxErr(line int, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrSyntax, line, fmt.Sprintf(format, args...))
}

// Parse unfolds data and parses every logical line. Limits are enforced while
// scanning. It works on bytes, so a fold inside a multi-byte character is
// reassembled correctly, and it never panics. Line numbers in errors are
// physical: a logical line is reported at the line where it starts.
func Parse(data []byte, limits Limits) ([]Line, error) {
	if limits.MaxLines == 0 {
		limits.MaxLines = DefaultLimits.MaxLines
	}
	if limits.MaxLineBytes == 0 {
		limits.MaxLineBytes = DefaultLimits.MaxLineBytes
	}
	data = bytes.TrimPrefix(data, bom)

	var (
		lines []Line
		cur   []byte
		start int // physical line where cur starts
		phys  int // physical line being read
	)
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		l, err := parseLine(cur, start)
		if err != nil {
			return err
		}
		lines = append(lines, l)
		return nil
	}

	for len(data) > 0 {
		var p []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			p, data = data[:i], data[i+1:]
		} else {
			p, data = data, nil
		}
		p = bytes.TrimSuffix(p, []byte{'\r'})
		phys++
		if phys > limits.MaxLines {
			return nil, syntaxErr(phys, "more than %d lines", limits.MaxLines)
		}

		if len(p) > 0 && (p[0] == ' ' || p[0] == '\t') {
			if len(cur) == 0 {
				return nil, syntaxErr(phys, "continuation line without a preceding line")
			}
			if len(cur)+len(p)-1 > limits.MaxLineBytes {
				return nil, syntaxErr(phys, "line longer than %d bytes", limits.MaxLineBytes)
			}
			cur = append(cur, p[1:]...)
			continue
		}

		if err := flush(); err != nil {
			return nil, err
		}
		if len(p) > limits.MaxLineBytes {
			return nil, syntaxErr(phys, "line longer than %d bytes", limits.MaxLineBytes)
		}
		cur = append(cur[:0], p...)
		start = phys
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return lines, nil
}

// parseLine parses one unfolded, non-empty logical line.
func parseLine(b []byte, lineNo int) (Line, error) {
	var l Line

	i := bytes.IndexAny(b, ";:")
	if i < 0 {
		return l, syntaxErr(lineNo, "missing colon")
	}
	name := string(b[:i])
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		group := name[:dot]
		name = name[dot+1:]
		if group == "" {
			return l, syntaxErr(lineNo, "empty group")
		}
		if !isToken(group) {
			return l, syntaxErr(lineNo, "invalid group %q", Clip(group))
		}
		l.Group = upperASCII(group)
	}
	if name == "" {
		return l, syntaxErr(lineNo, "empty property name")
	}
	if !isToken(name) {
		return l, syntaxErr(lineNo, "invalid property name %q", Clip(name))
	}
	l.Name = upperASCII(name)

	for b[i] == ';' {
		i++
		eq := bytes.IndexAny(b[i:], "=;:")
		if eq < 0 || b[i+eq] != '=' {
			return l, syntaxErr(lineNo, "parameter without a value")
		}
		if eq == 0 {
			return l, syntaxErr(lineNo, "empty parameter name")
		}
		pname := string(b[i : i+eq])
		if !isToken(pname) {
			return l, syntaxErr(lineNo, "invalid parameter name %q", Clip(pname))
		}
		p := Param{Name: upperASCII(pname)}
		i += eq + 1

		var val []byte
		quoted := false
		for ; i < len(b); i++ {
			c := b[i]
			if c == '"' {
				quoted = !quoted
				continue
			}
			if !quoted && (c == ',' || c == ';' || c == ':') {
				if c == ',' {
					p.Values = append(p.Values, string(val))
					val = val[:0]
					continue
				}
				break
			}
			val = append(val, c)
		}
		if quoted {
			return l, syntaxErr(lineNo, "unterminated quoted parameter value")
		}
		if i >= len(b) {
			return l, syntaxErr(lineNo, "missing colon")
		}
		p.Values = append(p.Values, string(val))
		l.Params = append(l.Params, p)
	}

	l.Value = string(b[i+1:])
	return l, nil
}

// isToken reports whether s is non-empty and holds only ASCII letters, digits
// and '-'.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// upperASCII upper-cases ASCII letters and leaves every other byte alone.
// strings.ToUpper would map U+0131 to I and U+017F to S.
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

// EqualFoldASCII reports whether a and b are equal with ASCII letters folded
// and every other byte compared exactly. strings.EqualFold would fold the Kelvin sign onto K.
func EqualFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
