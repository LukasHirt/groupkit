// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package ical parses and validates iCalendar data and evaluates calendar
// queries against it: time ranges with recurrence expansion, overrides,
// time zone mapping and text matching.
package ical

import (
	"errors"
	"fmt"
	"strings"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

var (
	// ErrInvalid reports data that is not a well-formed iCalendar object or
	// that breaks the CalDAV rules for a stored resource.
	ErrInvalid = errors.New("ical: invalid data")
	// ErrUnsupportedComponent reports a component type that is not allowed
	// in the collection.
	ErrUnsupportedComponent = errors.New("ical: unsupported component")
	// ErrUID reports a missing UID, or components with different UIDs.
	ErrUID = errors.New("ical: missing or inconsistent UID")
)

// Property is one content line of a component. Name and parameter names are
// upper-case. Value is the text as it appears on the line, still escaped.
type Property struct {
	// Name is the property name in upper case.
	Name string
	// Params maps each upper-case parameter name to its values.
	Params map[string][]string
	// Value is the text as written on the line, still escaped.
	Value string
}

// Text returns the value with backslash escapes resolved: \\, \, and \; stand
// for the escaped character, \n and \N for a newline, and any other \x for x.
func (p Property) Text() string {
	if !strings.Contains(p.Value, `\`) {
		return p.Value
	}
	var b strings.Builder
	b.Grow(len(p.Value))
	for i := 0; i < len(p.Value); i++ {
		c := p.Value[i]
		if c != '\\' || i+1 == len(p.Value) {
			b.WriteByte(c)
			continue
		}
		i++
		if n := p.Value[i]; n == 'n' || n == 'N' {
			b.WriteByte('\n')
		} else {
			b.WriteByte(n)
		}
	}
	return b.String()
}

// Component is a BEGIN/END block with its properties and nested components.
type Component struct {
	// Name is the component name in upper case, such as VEVENT.
	Name string
	// Props are the component's properties in the order they appear.
	Props []Property
	// Children are the nested components.
	Children []*Component
}

// Prop returns the first property with the given name, matched
// case-insensitively, or nil if there is none.
func (c *Component) Prop(name string) *Property {
	for i := range c.Props {
		if contentline.EqualFoldASCII(c.Props[i].Name, name) {
			return &c.Props[i]
		}
	}
	return nil
}

// PropAll returns all properties with the given name, in the order they
// appear.
func (c *Component) PropAll(name string) []Property {
	var out []Property
	for _, p := range c.Props {
		if contentline.EqualFoldASCII(p.Name, name) {
			out = append(out, p)
		}
	}
	return out
}

// Comps returns the direct child components with the given name, in the order
// they appear.
func (c *Component) Comps(name string) []*Component {
	var out []*Component
	for _, ch := range c.Children {
		if contentline.EqualFoldASCII(ch.Name, name) {
			out = append(out, ch)
		}
	}
	return out
}

// Calendar is a parsed VCALENDAR object.
type Calendar struct {
	Component
}

func isMarker(l contentline.Line, name string) bool {
	return l.Group == "" && l.Name == name
}

// Parse parses data as a single VCALENDAR object. It checks only that BEGIN
// and END lines nest, that every component has a name and that no VCALENDAR
// sits inside another; use Validate for the CalDAV rules.
func Parse(data []byte) (*Calendar, error) {
	lines, err := contentline.Parse(data, contentline.DefaultLimits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: empty input", ErrInvalid)
	}
	if !isMarker(lines[0], "BEGIN") || componentName(lines[0].Value) != "VCALENDAR" {
		return nil, fmt.Errorf("%w: data must start with BEGIN:VCALENDAR", ErrInvalid)
	}

	cal := &Calendar{Component: Component{Name: "VCALENDAR"}}
	stack := []*Component{&cal.Component}
	for i := 1; i < len(lines); i++ {
		l := lines[i]
		if len(stack) == 0 {
			return nil, fmt.Errorf("%w: content after END:VCALENDAR", ErrInvalid)
		}
		cur := stack[len(stack)-1]
		switch {
		case isMarker(l, "BEGIN"):
			name := componentName(l.Value)
			if name == "" || name == "VCALENDAR" {
				return nil, fmt.Errorf("%w: BEGIN:%s inside VCALENDAR", ErrInvalid, contentline.Clip(l.Value))
			}
			child := &Component{Name: name}
			cur.Children = append(cur.Children, child)
			stack = append(stack, child)
		case isMarker(l, "END"):
			if componentName(l.Value) != cur.Name {
				return nil, fmt.Errorf("%w: END:%s does not match BEGIN:%s",
					ErrInvalid, contentline.Clip(l.Value), contentline.Clip(cur.Name))
			}
			stack = stack[:len(stack)-1]
		default:
			cur.Props = append(cur.Props, property(l))
		}
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("%w: missing END:%s", ErrInvalid, contentline.Clip(stack[len(stack)-1].Name))
	}
	return cal, nil
}

// componentName returns the name on a BEGIN or END line, trimmed and
// upper-cased.
func componentName(v string) string {
	return upperASCII(strings.TrimSpace(v))
}

// upperASCII upper-cases ASCII letters and leaves every other byte alone.
// strings.ToUpper would turn U+0131 into I and U+017F into S, so a lookalike
// would pass for an ASCII name.
func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'a' <= c && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

func property(l contentline.Line) Property {
	p := Property{Name: l.Name, Value: l.Value}
	if len(l.Params) > 0 {
		p.Params = make(map[string][]string, len(l.Params))
		for _, pa := range l.Params {
			p.Params[pa.Name] = append(p.Params[pa.Name], pa.Values...)
		}
	}
	return p
}
