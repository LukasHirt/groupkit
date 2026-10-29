// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bufio"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// prefixes maps the namespaces the server knows to their fixed prefixes.
var prefixes = map[string]string{
	NSDAV:     "D",
	NSCalDAV:  "C",
	NSCardDAV: "CR",
	NSCS:      "CS",
	NSApple:   "A",
	// The xml prefix is predeclared and must not be declared again.
	xmlNamespace: "xml",
}

// rootAttrs declares the five known namespaces, in a fixed order.
func rootAttrs() []Attr {
	return []Attr{
		{"xmlns:D", NSDAV},
		{"xmlns:C", NSCalDAV},
		{"xmlns:CR", NSCardDAV},
		{"xmlns:CS", NSCS},
		{"xmlns:A", NSApple},
	}
}

// Attr is one attribute. Name is written as given (for example "xmlns:C"
// or "name"); it and any prefix in it are supplied by the developer and must
// be valid XML names, or the Writer records an error. Value is escaped.
type Attr struct{ Name, Value string }

// Writer writes XML to an io.Writer. It is meant for building DAV bodies: it
// tracks open elements, prefixes names, and escapes everything it is given.
//
// Element names may use any characters XML 1.0 allows in a name, so names a
// client sent in a request can be written back.
//
// Names in the five known namespaces use the fixed prefixes D, C, CR, CS and
// A, which the caller declares on the root element (WriteMultiStatus and
// WriteError do). A name in any other namespace gets a prefix ns1, ns2, ...
// declared on the first element that needs it and reused by its descendants.
// The XML namespace uses the predeclared xml prefix.
//
// The first error is kept and later calls do nothing, so a caller can write
// freely and check Err or Flush once. A Writer never panics on odd input;
// an invalid name or an unbalanced End is recorded as an error.
type Writer struct {
	bw    *bufio.Writer
	stack []string // qualified names of the open elements
	decls []nsDecl // generated prefixes in scope
	err   error
	nsN   int
	// floor is the stack depth End may not close below. writeProp raises it
	// around a Prop.Write so a property cannot close the elements around it.
	floor int
}

// NewWriter returns a Writer that buffers its output for w. Call Flush when
// done.
func NewWriter(w io.Writer) *Writer { return &Writer{bw: bufio.NewWriterSize(w, 8192)} }

// Err returns the first error recorded, from the underlying writer or from
// misuse such as an invalid name.
func (w *Writer) Err() error { return w.err }

// Flush writes buffered output to the underlying writer and returns the
// first error recorded. Output is only ever appended in whole tokens (a name
// is checked before its tag is started), so what is buffered is always safe
// to send: Flush sends it even after a recorded misuse error, which keeps the
// part of a body that was written correctly. After a failure of the
// underlying writer the buffer reports that failure and sends nothing more.
func (w *Writer) Flush() error {
	if err := w.bw.Flush(); err != nil && w.err == nil {
		w.err = err
	}
	return w.err
}

// unwind closes open elements down to depth n, even after an error, so the
// output stays well-formed as far as it goes. Nothing is written once the
// underlying writer has failed.
func (w *Writer) unwind(n int) {
	for len(w.stack) > n {
		q := w.stack[len(w.stack)-1]
		w.stack = w.stack[:len(w.stack)-1]
		for len(w.decls) > 0 && w.decls[len(w.decls)-1].depth >= len(w.stack) {
			w.decls = w.decls[:len(w.decls)-1]
		}
		_, _ = w.bw.WriteString("</" + q + ">")
	}
}

// validNamespace reports whether s may be written as a namespace URI: valid
// UTF-8 with no whitespace or control characters, and not the reserved xmlns
// namespace.
func validNamespace(s string) bool {
	if s == "http://www.w3.org/2000/xmlns/" || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

func (w *Writer) fail(msg string) {
	if w.err == nil {
		w.err = errors.New("dav: xml writer: " + msg)
	}
}

func (w *Writer) str(s string) {
	if w.err == nil {
		_, w.err = w.bw.WriteString(s)
	}
}

func (w *Writer) escaped(s string) {
	if w.err == nil {
		w.err = xml.EscapeText(w.bw, []byte(cleanText(s)))
	}
}

// nameStart and nameChar implement the XML 1.0 (fifth edition) NameStartChar
// and NameChar productions, without the colon.
func nameStart(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		return true
	case r >= 0xC0 && r <= 0xD6, r >= 0xD8 && r <= 0xF6, r >= 0xF8 && r <= 0x2FF,
		r >= 0x370 && r <= 0x37D, r >= 0x37F && r <= 0x1FFF, r >= 0x200C && r <= 0x200D,
		r >= 0x2070 && r <= 0x218F, r >= 0x2C00 && r <= 0x2FEF, r >= 0x3001 && r <= 0xD7FF,
		r >= 0xF900 && r <= 0xFDCF, r >= 0xFDF0 && r <= 0xFFFD, r >= 0x10000 && r <= 0xEFFFF:
		return true
	}
	return false
}

func nameChar(r rune) bool {
	return nameStart(r) || r == '-' || r == '.' || (r >= '0' && r <= '9') || r == 0xB7 ||
		(r >= 0x300 && r <= 0x36F) || (r >= 0x203F && r <= 0x2040)
}

// validXMLName reports whether s is a well-formed XML name. When colon is
// true one colon is allowed between two names (a prefixed attribute name);
// local names never contain one.
func validXMLName(s string, colon bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	if colon {
		if p, l, ok := strings.Cut(s, ":"); ok {
			return validXMLName(p, false) && validXMLName(l, false)
		}
	}
	for i, r := range s {
		if i == 0 && !nameStart(r) || !nameChar(r) {
			return false
		}
	}
	return s != ""
}

// nsDecl is an automatically chosen prefix declared on the open element at
// stack index depth.
type nsDecl struct {
	space, prefix string
	depth         int
}

func (w *Writer) lookup(space string) (string, bool) {
	for i := len(w.decls) - 1; i >= 0; i-- {
		if w.decls[i].space == space {
			return w.decls[i].prefix, true
		}
	}
	return "", false
}

// open writes the start of an element up to its attributes and returns the
// qualified name and the prefixes it declared. space lists further
// namespaces to declare on this element so descendants can reuse them. It
// returns "" after recording an error.
func (w *Writer) open(name xml.Name, attrs []Attr, spaces []string) (string, []nsDecl) {
	if w.err != nil {
		return "", nil
	}
	if !validXMLName(name.Local, false) {
		w.fail("invalid element name")
		return "", nil
	}
	for _, a := range attrs {
		if !validXMLName(a.Name, true) {
			w.fail("invalid attribute name")
			return "", nil
		}
	}
	for _, sp := range append([]string{name.Space}, spaces...) {
		if sp != "" && !validNamespace(sp) {
			w.fail("invalid namespace")
			return "", nil
		}
	}
	var fresh []nsDecl
	prefixFor := func(space string) string {
		if p, ok := prefixes[space]; ok {
			return p
		}
		if p, ok := w.lookup(space); ok {
			return p
		}
		for _, d := range fresh {
			if d.space == space {
				return d.prefix
			}
		}
		w.nsN++
		p := "ns" + strconv.Itoa(w.nsN)
		fresh = append(fresh, nsDecl{space: space, prefix: p, depth: len(w.stack)})
		return p
	}
	q := name.Local
	if name.Space != "" {
		q = prefixFor(name.Space) + ":" + name.Local
	}
	for _, sp := range spaces {
		if sp != "" {
			prefixFor(sp)
		}
	}
	w.str("<")
	w.str(q)
	for _, d := range fresh {
		w.str(" xmlns:" + d.prefix + `="`)
		w.escaped(d.space)
		w.str(`"`)
	}
	for _, a := range attrs {
		w.str(" " + a.Name + `="`)
		w.escaped(a.Value)
		w.str(`"`)
	}
	return q, fresh
}

// Start opens an element.
func (w *Writer) Start(name xml.Name, attrs ...Attr) { w.start(name, attrs, nil) }

// start opens an element and also declares spaces on it.
func (w *Writer) start(name xml.Name, attrs []Attr, spaces []string) {
	if q, fresh := w.open(name, attrs, spaces); q != "" {
		w.str(">")
		w.decls = append(w.decls, fresh...)
		w.stack = append(w.stack, q)
	}
}

// End closes the innermost open element. With none open it records an
// error and writes nothing.
func (w *Writer) End() {
	if w.err != nil {
		return
	}
	if len(w.stack) <= w.floor {
		w.fail("End without open element")
		return
	}
	q := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	for len(w.decls) > 0 && w.decls[len(w.decls)-1].depth >= len(w.stack) {
		w.decls = w.decls[:len(w.decls)-1]
	}
	w.str("</" + q + ">")
}

// Empty writes an element with no content.
func (w *Writer) Empty(name xml.Name) {
	if q, _ := w.open(name, nil, nil); q != "" {
		w.str("/>")
	}
}

// Text writes character data, escaped. Characters XML 1.0 forbids are
// dropped and invalid UTF-8 becomes U+FFFD.
func (w *Writer) Text(s string) { w.escaped(s) }

// Raw writes markup as given, without escaping. Use it only for trusted,
// well-formed XML such as a constant; never pass request data or stored
// values to it.
func (w *Writer) Raw(s string) { w.str(s) }

// cleanText returns s with invalid UTF-8 replaced by U+FFFD and characters
// outside the XML 1.0 Char production removed. Tab, newline and carriage
// return are kept.
func cleanText(s string) string {
	ok := true
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && n == 1) || !xmlChar(r) {
			ok = false
			break
		}
		i += n
	}
	if ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune(utf8.RuneError)
		case xmlChar(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func xmlChar(r rune) bool {
	return r == '\t' || r == '\n' || r == '\r' ||
		(r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}
