// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Limits on request bodies. They bound memory and CPU for hostile input.
const (
	// MaxXMLBytes is the largest XML body ParseXML accepts.
	MaxXMLBytes = 1 << 20
	// MaxXMLDepth is the deepest element nesting ParseXML accepts. The root
	// element is at depth 1.
	MaxXMLDepth = 64

	maxXMLElements   = 100000
	maxXMLAttributes = 100000
)

// ErrXML reports a request body that is not acceptable XML: malformed, too
// large, too deep, or using features the server refuses (DTDs, entities,
// other encodings).
var ErrXML = errors.New("dav: malformed XML")

// xmlNamespace is the namespace the xml prefix is bound to.
const xmlNamespace = "http://www.w3.org/XML/1998/namespace"

// Node is one element of a parsed XML document.
type Node struct {
	Name xml.Name
	// Attrs holds the attributes keyed by expanded name. Namespace
	// declarations are not attributes and are left out.
	Attrs map[xml.Name]string
	// Text is the character data directly inside the element. It is trimmed
	// of surrounding whitespace when the element has children.
	Text     string
	Children []*Node
}

// Child returns the first child element with the given name, or nil.
func (n *Node) Child(name xml.Name) *Node {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Kids returns all child elements with the given name, in document order.
func (n *Node) Kids(name xml.Name) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// Attr returns the value of the attribute with the given local name,
// ignoring its namespace, or "" if there is none. An attribute without a
// namespace is preferred; otherwise the one whose namespace sorts first.
func (n *Node) Attr(local string) string {
	if v, ok := n.Attrs[xml.Name{Local: local}]; ok {
		return v
	}
	var spaces []string
	for k := range n.Attrs {
		if k.Local == local {
			spaces = append(spaces, k.Space)
		}
	}
	if len(spaces) == 0 {
		return ""
	}
	sort.Strings(spaces)
	return n.Attrs[xml.Name{Space: spaces[0], Local: local}]
}

func xmlErr(reason string) error { return fmt.Errorf("%w: %s", ErrXML, reason) }

// frame is an open element during parsing.
type frame struct {
	node *Node
	raw  xml.Name // name as written, for matching the end tag
	text strings.Builder
	ns   map[string]string // prefix declarations made on this element
}

// ParseXML parses one XML document into a tree. It accepts at most
// MaxXMLBytes bytes and MaxXMLDepth levels of nesting, a single root element,
// and UTF-8 text. DOCTYPE declarations are refused, so entities beyond the
// five predefined ones are never expanded. Comments and processing
// instructions are ignored. Every error wraps ErrXML.
//
// Namespaces are resolved here, from the raw token stream, because the
// standard decoder leaves an undeclared prefix in place of the namespace
// instead of failing.
func ParseXML(data []byte) (root *Node, err error) {
	if len(data) > MaxXMLBytes {
		return nil, xmlErr("document too large")
	}
	defer func() {
		// The decoder should never panic; make sure a bug there cannot
		// take the server down.
		if r := recover(); r != nil {
			root, err = nil, xmlErr("internal parser error")
		}
	}()
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = true
	d.Entity = nil
	d.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		switch strings.ToLower(label) {
		case "utf-8", "us-ascii":
			return r, nil
		}
		return nil, xmlErr("unsupported encoding")
	}

	var (
		stack      []*frame
		elements   int
		attributes int
		done       bool
		first      = true
	)
	for {
		tok, terr := d.RawToken()
		if terr == io.EOF {
			break
		}
		if terr != nil {
			return nil, xmlErr("syntax error")
		}
		isFirst := first
		first = false
		switch t := tok.(type) {
		case xml.StartElement:
			if !validName(t.Name) {
				return nil, xmlErr("invalid name")
			}
			if done {
				return nil, xmlErr("multiple root elements")
			}
			if len(stack) >= MaxXMLDepth {
				return nil, xmlErr("nesting too deep")
			}
			elements++
			attributes += len(t.Attr)
			if elements > maxXMLElements || attributes > maxXMLAttributes {
				return nil, xmlErr("too many elements or attributes")
			}
			f := &frame{raw: t.Name}
			for _, a := range t.Attr {
				if !validName(a.Name) {
					return nil, xmlErr("invalid name")
				}
				var ok bool
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					ok = f.declare("", a.Value)
				case a.Name.Space == "xmlns":
					ok = f.declare(a.Name.Local, a.Value)
				default:
					ok = true
				}
				if !ok {
					return nil, xmlErr("invalid namespace declaration")
				}
			}
			stack = append(stack, f)
			name, ok := resolve(stack, t.Name, true)
			if !ok {
				return nil, xmlErr("undeclared namespace prefix")
			}
			f.node = &Node{Name: name}
			for _, a := range t.Attr {
				if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
					continue
				}
				an, ok := resolve(stack, a.Name, false)
				if !ok {
					return nil, xmlErr("undeclared namespace prefix")
				}
				if _, dup := f.node.Attrs[an]; dup {
					return nil, xmlErr("duplicate attribute")
				}
				if f.node.Attrs == nil {
					f.node.Attrs = make(map[xml.Name]string)
				}
				f.node.Attrs[an] = a.Value
			}
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].raw != t.Name {
				return nil, xmlErr("mismatched end element")
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f.node.Text = f.text.String()
			if len(f.node.Children) > 0 {
				f.node.Text = strings.TrimSpace(f.node.Text)
			}
			if len(stack) == 0 {
				root, done = f.node, true
			} else {
				p := stack[len(stack)-1].node
				p.Children = append(p.Children, f.node)
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, xmlErr("content outside the root element")
			}
		case xml.Directive:
			return nil, xmlErr("DOCTYPE and other declarations are not allowed")
		case xml.ProcInst:
			// Only the first token may be the XML declaration.
			if strings.EqualFold(t.Target, "xml") && !isFirst {
				return nil, xmlErr("misplaced XML declaration")
			}
		case xml.Comment:
			// Ignored.
		}
	}
	if len(stack) > 0 || root == nil {
		return nil, xmlErr("no complete root element")
	}
	return root, nil
}

// validName reports whether a raw name has non-empty prefix and local parts
// and no stray colon. The decoder passes malformed names through whole.
func validName(n xml.Name) bool {
	return n.Local != "" && !strings.Contains(n.Local, ":") && !strings.Contains(n.Space, ":")
}

// declare records a namespace declaration. It reports false for one that
// XML Namespaces forbids: a repeat on the same element, an empty URI for a
// prefix, the xmlns prefix, or the xml prefix bound to anything else.
func (f *frame) declare(prefix, uri string) bool {
	if _, dup := f.ns[prefix]; dup {
		return false
	}
	switch {
	case prefix == "xmlns", prefix == "xml" && uri != xmlNamespace, prefix != "" && uri == "":
		return false
	case uri == xmlNamespace && prefix != "xml", !validNamespace(uri):
		return false // reserved or malformed namespace URI
	}
	if f.ns == nil {
		f.ns = make(map[string]string)
	}
	f.ns[prefix] = uri
	return true
}

// resolve expands a raw name against the declarations in scope. An
// unprefixed element takes the default namespace; an unprefixed attribute
// has none. ok is false for an undeclared prefix.
func resolve(stack []*frame, raw xml.Name, element bool) (name xml.Name, ok bool) {
	prefix := raw.Space
	if prefix == "" && !element {
		return raw, true
	}
	if prefix == "xml" {
		return xml.Name{Space: xmlNamespace, Local: raw.Local}, true
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if uri, found := stack[i].ns[prefix]; found {
			return xml.Name{Space: uri, Local: raw.Local}, prefix == "" || uri != ""
		}
	}
	if prefix == "" {
		return raw, true
	}
	return xml.Name{}, false
}
