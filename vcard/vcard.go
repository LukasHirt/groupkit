// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package vcard parses and validates vCard data and evaluates address book
// queries against it.
package vcard

import (
	"errors"
	"fmt"
	"strings"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

var (
	// ErrInvalid reports data that is not a well-formed vCard.
	ErrInvalid = errors.New("vcard: invalid data")
	// ErrUnsupportedVersion reports a VERSION other than 3.0 or 4.0.
	ErrUnsupportedVersion = errors.New("vcard: unsupported version")
	// ErrMultipleCards reports more than one VCARD in one object.
	ErrMultipleCards = errors.New("vcard: multiple cards in one object")
)

// Property is one content line of a card. Name, Group and parameter names are
// upper-case. Raw is the value as it appears on the line, still escaped.
type Property struct {
	// Group is the group prefix, such as ITEM1, or empty.
	Group string
	// Name is the property name in upper case.
	Name string
	// Params maps each upper-case parameter name to its values.
	Params map[string][]string
	// Raw is the value as written on the line, still escaped.
	Raw string
}

// Text returns the value with backslash escapes resolved: \\, \, and \; stand
// for the escaped character, \n and \N for a newline, and any other \x for x.
func (p Property) Text() string {
	if !strings.Contains(p.Raw, `\`) {
		return p.Raw
	}
	var b strings.Builder
	b.Grow(len(p.Raw))
	for i := 0; i < len(p.Raw); i++ {
		c := p.Raw[i]
		if c != '\\' || i+1 == len(p.Raw) {
			b.WriteByte(c)
			continue
		}
		i++
		if n := p.Raw[i]; n == 'n' || n == 'N' {
			b.WriteByte('\n')
		} else {
			b.WriteByte(n)
		}
	}
	return b.String()
}

// Card is a parsed vCard. BEGIN, END and VERSION are not part of Props.
type Card struct {
	// Version is the value of the VERSION property.
	Version string
	// Props are the card's properties in the order they appear.
	Props []Property
}

// UID returns the text of the card's UID property, or "" if the card has
// none. Only an ungrouped UID counts: a grouped one such as item1.UID is
// ignored. When the card has several UID lines, the first one wins.
func (c *Card) UID() string {
	for _, p := range c.Props {
		if p.Group == "" && p.Name == "UID" {
			return p.Text()
		}
	}
	return ""
}

// Find returns all properties with the given name, in the order they appear.
// Only ASCII letters are matched case-insensitively.
func (c *Card) Find(name string) []Property {
	var out []Property
	for _, p := range c.Props {
		if contentline.EqualFoldASCII(p.Name, name) {
			out = append(out, p)
		}
	}
	return out
}

// Validate parses data as a single vCard and returns its UID, which is empty
// when the card has none.
func Validate(data []byte) (uid string, err error) {
	c, err := Parse(data)
	if err != nil {
		return "", err
	}
	return c.UID(), nil
}

func isMarker(l contentline.Line, name, value string) bool {
	return l.Group == "" && l.Name == name && strings.EqualFold(strings.TrimSpace(l.Value), value)
}

// Parse parses data as a single vCard in version 3.0 or 4.0.
func Parse(data []byte) (*Card, error) {
	lines, err := contentline.Parse(data, contentline.DefaultLimits)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: empty input", ErrInvalid)
	}
	if !isMarker(lines[0], "BEGIN", "VCARD") {
		return nil, fmt.Errorf("%w: data must start with BEGIN:VCARD", ErrInvalid)
	}

	card := &Card{}
	haveVersion := false
	end := -1
	for i := 1; i < len(lines) && end < 0; i++ {
		l := lines[i]
		switch {
		case l.Group == "" && l.Name == "BEGIN":
			return nil, fmt.Errorf("%w: nested BEGIN:%s", ErrInvalid, contentline.Clip(l.Value))
		case l.Group == "" && l.Name == "END":
			if !isMarker(l, "END", "VCARD") {
				return nil, fmt.Errorf("%w: END:%s does not match BEGIN:VCARD", ErrInvalid, contentline.Clip(l.Value))
			}
			end = i
		case l.Group == "" && l.Name == "VERSION":
			if haveVersion {
				return nil, fmt.Errorf("%w: repeated VERSION", ErrInvalid)
			}
			haveVersion = true
			card.Version = strings.TrimSpace(l.Value)
		default:
			card.Props = append(card.Props, property(l))
		}
	}
	if end < 0 {
		return nil, fmt.Errorf("%w: missing END:VCARD", ErrInvalid)
	}
	for _, l := range lines[end+1:] {
		if isMarker(l, "BEGIN", "VCARD") {
			return nil, fmt.Errorf("%w: second BEGIN:VCARD", ErrMultipleCards)
		}
	}
	if end+1 < len(lines) {
		return nil, fmt.Errorf("%w: content after END:VCARD", ErrInvalid)
	}
	if !haveVersion {
		return nil, fmt.Errorf("%w: missing VERSION", ErrInvalid)
	}
	if card.Version != "3.0" && card.Version != "4.0" {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedVersion, contentline.Clip(card.Version))
	}
	return card, nil
}

func property(l contentline.Line) Property {
	p := Property{Group: l.Group, Name: l.Name, Raw: l.Value}
	if len(l.Params) > 0 {
		p.Params = make(map[string][]string, len(l.Params))
		for _, pa := range l.Params {
			p.Params[pa.Name] = append(p.Params[pa.Name], pa.Values...)
		}
	}
	return p
}
