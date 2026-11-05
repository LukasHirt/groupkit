// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"strings"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// Object is one calendar object resource cut out of a larger file by Split.
type Object struct {
	// UID is the UID the object's components share, with escapes resolved.
	// It is empty for a component that has no UID.
	UID string
	// Calendar holds the object: the top-level properties of the file
	// except METHOD, the VTIMEZONEs the components refer to, and the
	// components themselves in the order they appeared. Encode it to store
	// it.
	Calendar *Calendar
}

// Split cuts a calendar file into the objects a collection stores: one per
// UID, in the order the UIDs first appear. A recurring master and its
// overrides share a UID and so end up in one object. Each object gets a copy
// of every VTIMEZONE whose TZID one of its components refers to, in file
// order, and a copy of the file's top-level properties except METHOD, which
// a stored object must not have. A component without a UID becomes an object
// of its own with an empty UID. Objects share no memory with each other.
//
// Split checks the structure as Parse does and returns ErrInvalid for the
// same input, with no limit on the number of lines; the caller bounds the
// size of the file. It does not apply the rules of Validate, so an object
// may still be refused by Validate, for instance when it mixes component
// types or has no UID.
func Split(data []byte) ([]Object, error) {
	// Lines are at least one byte, so this never cuts the file short.
	cal, err := parse(data, contentline.Limits{MaxLines: len(data) + 1})
	if err != nil {
		return nil, err
	}

	var top []Property
	for _, p := range cal.Props {
		if p.Name != "METHOD" {
			top = append(top, p)
		}
	}

	var (
		zones   []*Component
		objects []Object
		byUID   = map[string]int{}
	)
	for _, c := range cal.Children {
		if c.Name == "VTIMEZONE" {
			zones = append(zones, c)
			continue
		}
		uid := ""
		if p := c.Prop("UID"); p != nil {
			uid = p.Text()
		}
		if i, ok := byUID[uid]; ok && uid != "" {
			objects[i].Calendar.Children = append(objects[i].Calendar.Children, c)
			continue
		}
		if uid != "" {
			byUID[uid] = len(objects)
		}
		objects = append(objects, Object{UID: uid, Calendar: &Calendar{Component: Component{
			Name:     "VCALENDAR",
			Children: []*Component{c},
		}}})
	}

	for i := range objects {
		cal := objects[i].Calendar
		comps := cal.Children
		cal.Props = cloneProps(top)
		cal.Children = nil
		if len(zones) > 0 {
			used := map[string]bool{}
			for _, c := range comps {
				collectTZIDs(c, used)
			}
			for _, z := range zones {
				if p := z.Prop("TZID"); p != nil && used[strings.TrimSpace(p.Value)] {
					cal.Children = append(cal.Children, clone(z))
				}
			}
		}
		for _, c := range comps {
			cal.Children = append(cal.Children, clone(c))
		}
	}
	return objects, nil
}

// collectTZIDs adds every TZID parameter value in c and its children to used,
// with surrounding white space removed, as the Resolver reads them.
func collectTZIDs(c *Component, used map[string]bool) {
	for _, p := range c.Props {
		for _, v := range p.Params["TZID"] {
			used[strings.TrimSpace(v)] = true
		}
	}
	for _, ch := range c.Children {
		collectTZIDs(ch, used)
	}
}

// clone returns a deep copy of c.
func clone(c *Component) *Component {
	out := &Component{Name: c.Name, Props: cloneProps(c.Props)}
	if len(c.Children) > 0 {
		out.Children = make([]*Component, len(c.Children))
		for i, ch := range c.Children {
			out.Children[i] = clone(ch)
		}
	}
	return out
}

func cloneProps(props []Property) []Property {
	if len(props) == 0 {
		return nil
	}
	out := make([]Property, len(props))
	for i, p := range props {
		out[i] = Property{Name: p.Name, Value: p.Value}
		if p.Params != nil {
			out[i].Params = make(map[string][]string, len(p.Params))
			for k, v := range p.Params {
				out[i].Params[k] = append([]string(nil), v...)
			}
		}
	}
	return out
}
