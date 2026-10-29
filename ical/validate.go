// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"fmt"
	"strings"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// Info describes a validated calendar object resource.
type Info struct {
	// UID is the UID shared by all components.
	UID string
	// Component is the type of the components, such as VEVENT.
	Component string
}

// Validate checks data against the rules for a calendar object resource in
// RFC 4791 section 4.1: VERSION 2.0 and a PRODID, no METHOD, and at least one
// component besides VTIMEZONE. All of those components must be of one type
// that appears in allowed, and must carry the same UID, as a recurring master
// and its overrides do. An empty allowed list means VEVENT only.
func Validate(data []byte, allowed []string) (Info, error) {
	if len(allowed) == 0 {
		allowed = []string{"VEVENT"}
	}
	cal, err := Parse(data)
	if err != nil {
		return Info{}, err
	}
	if v := cal.Prop("VERSION"); v == nil || strings.TrimSpace(v.Value) != "2.0" {
		return Info{}, fmt.Errorf("%w: VERSION must be 2.0", ErrInvalid)
	}
	if cal.Prop("PRODID") == nil {
		return Info{}, fmt.Errorf("%w: missing PRODID", ErrInvalid)
	}
	if cal.Prop("METHOD") != nil {
		return Info{}, fmt.Errorf("%w: METHOD is not allowed", ErrInvalid)
	}

	var comps []*Component
	for _, c := range cal.Children {
		if c.Name != "VTIMEZONE" {
			comps = append(comps, c)
		}
	}
	if len(comps) == 0 {
		return Info{}, fmt.Errorf("%w: no calendar component", ErrInvalid)
	}
	kind := comps[0].Name
	for _, c := range comps[1:] {
		if c.Name != kind {
			return Info{}, fmt.Errorf("%w: mixed components %s and %s",
				ErrInvalid, contentline.Clip(kind), contentline.Clip(c.Name))
		}
	}
	ok := false
	for _, a := range allowed {
		if contentline.EqualFoldASCII(a, kind) {
			ok = true
			break
		}
	}
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrUnsupportedComponent, contentline.Clip(kind))
	}

	uid := ""
	for i, c := range comps {
		p := c.Prop("UID")
		if p == nil || p.Value == "" {
			return Info{}, fmt.Errorf("%w: %s without UID", ErrUID, contentline.Clip(kind))
		}
		if i == 0 {
			uid = p.Text()
		} else if p.Text() != uid {
			return Info{}, fmt.Errorf("%w: %q and %q", ErrUID, contentline.Clip(uid), contentline.Clip(p.Text()))
		}
	}
	return Info{UID: uid, Component: kind}, nil
}
