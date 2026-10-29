// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"cmp"
	"encoding/xml"
	"net/http"
	"slices"
)

// PropDef is one property of a resource. Get produces its value on demand, so
// expensive values are only computed when asked for. AllProp is false for
// heavy properties, such as calendar-data, that DAV:allprop must leave out.
//
// Do I/O in Get: it runs before anything is sent, and an error becomes a 500
// propstat for that property alone. The Prop it returns should carry a pure
// Write (see Prop), because Write runs after the 207 is committed.
type PropDef struct {
	Get     func() (Prop, error)
	AllProp bool
}

// Resource is what PropFind needs to answer for one resource: its encoded
// href and its properties by name.
type Resource struct {
	Href  string
	Props map[xml.Name]PropDef
}

// compareNames orders names by namespace, then local name.
func compareNames(a, b xml.Name) int {
	if c := cmp.Compare(a.Space, b.Space); c != 0 {
		return c
	}
	return cmp.Compare(a.Local, b.Local)
}

// PropFind evaluates a PROPFIND request against one resource and returns its
// response. The order of the output depends only on the request and the
// property names, never on map iteration order. Properties that cannot be
// produced are reported in a 500 propstat without the cause, and never stop
// the others. A response always has at least one propstat, so it is a valid
// shape even when nothing matched.
func PropFind(req PropfindRequest, res Resource) Response {
	var ok, notFound, failed []Prop
	// get adds the named property to the right list.
	get := func(name xml.Name) {
		def, found := res.Props[name]
		if !found {
			notFound = append(notFound, Prop{Name: name})
			return
		}
		if def.Get == nil {
			failed = append(failed, Prop{Name: name})
			return
		}
		p, err := def.Get()
		if err != nil {
			failed = append(failed, Prop{Name: name})
			return
		}
		p.Name = name // the map key is the truth
		ok = append(ok, p)
	}

	switch {
	case req.PropName:
		for _, name := range sortedNames(res.Props, func(PropDef) bool { return true }) {
			ok = append(ok, Prop{Name: name})
		}
	case req.AllProp:
		listed := sortedNames(res.Props, func(d PropDef) bool { return d.AllProp })
		for _, name := range listed {
			get(name)
		}
		for _, name := range req.Props {
			if !slices.Contains(listed, name) {
				get(name)
			}
		}
	default:
		for _, name := range req.Props {
			get(name)
		}
	}

	var out []Propstat
	for _, g := range []struct {
		status int
		props  []Prop
	}{
		{http.StatusOK, ok},
		{http.StatusNotFound, notFound},
		{http.StatusInternalServerError, failed},
	} {
		if len(g.props) > 0 {
			out = append(out, Propstat{Status: g.status, Props: g.props})
		}
	}
	if len(out) == 0 {
		out = []Propstat{{Status: http.StatusOK}}
	}
	return Response{Href: res.Href, Propstats: out}
}

// sortedNames lists the names of the definitions that pass keep, sorted by
// namespace, then local name.
func sortedNames(props map[xml.Name]PropDef, keep func(PropDef) bool) []xml.Name {
	var out []xml.Name
	for name, def := range props {
		if keep(def) {
			out = append(out, name)
		}
	}
	slices.SortFunc(out, compareNames)
	return out
}
