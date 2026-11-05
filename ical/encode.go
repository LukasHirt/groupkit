// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// maxLineOctets is the longest content line Encode writes, line break
// excluded (RFC 5545, section 3.1).
const maxLineOctets = 75

// Encode writes the component as iCalendar text: a BEGIN line, the
// properties in order, the children, and an END line. Lines end in CRLF and
// are folded at 75 octets without splitting a UTF-8 character. Parameters
// are written in name order, and a parameter value that holds a comma, a
// semicolon or a colon is quoted. A double quote inside a parameter value is
// dropped, since the syntax has no way to write one.
//
// Names and values are written as they are, so Encode on a parsed component
// gives back the same tree when parsed again. A component built by hand has
// to keep to the syntax itself: a name must be a token, and a value must not
// contain a line break.
func (c *Component) Encode() []byte {
	return c.appendTo(nil)
}

func (c *Component) appendTo(b []byte) []byte {
	b = foldLine(b, "BEGIN:"+c.Name)
	for _, p := range c.Props {
		b = foldLine(b, p.line())
	}
	for _, ch := range c.Children {
		b = ch.appendTo(b)
	}
	return foldLine(b, "END:"+c.Name)
}

// line writes the property as one unfolded content line.
func (p Property) line() string {
	var sb strings.Builder
	sb.WriteString(p.Name)
	names := make([]string, 0, len(p.Params))
	for n := range p.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sb.WriteByte(';')
		sb.WriteString(n)
		sb.WriteByte('=')
		for i, v := range p.Params[n] {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeParamValue(&sb, v)
		}
	}
	sb.WriteByte(':')
	sb.WriteString(p.Value)
	return sb.String()
}

func writeParamValue(sb *strings.Builder, v string) {
	v = strings.ReplaceAll(v, `"`, "")
	if strings.ContainsAny(v, ",;:") {
		sb.WriteByte('"')
		sb.WriteString(v)
		sb.WriteByte('"')
		return
	}
	sb.WriteString(v)
}

// foldLine appends line to b, folded at maxLineOctets and ended by CRLF. A
// continuation line starts with one space, which counts toward its length.
// The fold moves back to the start of a UTF-8 character when it can. Bytes
// that are not UTF-8 are split wherever the limit falls.
func foldLine(b []byte, line string) []byte {
	limit := maxLineOctets
	for len(line) > limit {
		n := limit
		for n > 0 && !utf8.RuneStart(line[n]) {
			n--
		}
		if n == 0 {
			n = limit
		}
		b = append(b, line[:n]...)
		b = append(b, "\r\n "...)
		line = line[n:]
		limit = maxLineOctets - 1
	}
	b = append(b, line...)
	return append(b, "\r\n"...)
}
