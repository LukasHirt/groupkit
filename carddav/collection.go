// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// Limits on the text properties of an address book.
const (
	maxDisplayNameBytes = 255
	maxDescriptionBytes = 4096
)

// textValue returns the value of a settable property element: its text with
// surrounding whitespace removed. A blank value is the same as no value; the
// property is then unset (the display name falls back to the book name). An
// element with child elements has no text value and is refused, as is a value
// that is too long or not valid UTF-8.
func textValue(name xml.Name, n *dav.Node) (string, *dav.Error) {
	limit := maxDisplayNameBytes
	if name == dav.AddressbookDescription {
		limit = maxDescriptionBytes
	}
	if len(n.Children) > 0 {
		return "", dav.BadRequest("property value has child elements")
	}
	s := n.Text
	if !utf8.ValidString(s) {
		return "", dav.BadRequest("property value is not valid UTF-8")
	}
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return "", dav.BadRequest("property value is too long")
	}
	return s, nil
}

// settable reports whether an address book property can be set by clients.
func settable(name xml.Name) bool {
	return name == dav.DisplayName || name == dav.AddressbookDescription
}

// protectedOnCreate lists the CardDAV and CalendarServer properties that the
// server computes.
var protectedOnCreate = map[xml.Name]bool{
	dav.GetCTag: true,
	dav.N(dav.NSCardDAV, "supported-address-data"):    true,
	dav.N(dav.NSCardDAV, "max-resource-size"):         true,
	dav.N(dav.NSCardDAV, "supported-collation-set"):   true,
	dav.N(dav.NSCardDAV, "addressbook-home-set"):      true,
	dav.N(dav.NSCardDAV, "supported-address-data-v4"): true,
}

// refusedOnCreate reports whether a property that the engine does not handle
// fails the creation: a DAV: property (live or protected) or a computed
// CardDAV one. Any other property is skipped, as clients send extras with a
// new collection.
func refusedOnCreate(name xml.Name) bool {
	return name.Space == dav.NSDAV || protectedOnCreate[name]
}

// mkcol creates an address book. The body is optional; an extended body may
// set the display name and description.
func (h *Handler) mkcol(w http.ResponseWriter, r *http.Request, t target, owner string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	cr, err := dav.ParseMkcol(body)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}

	var props store.Props
	hasType := false
	for _, p := range cr.Props {
		switch p.Name {
		case dav.ResourceType:
			hasType = true
		case dav.DisplayName, dav.AddressbookDescription:
			v, verr := textValue(p.Name, p.Node)
			if verr != nil {
				dav.WriteError(w, verr)
				return
			}
			if p.Name == dav.DisplayName {
				props.DisplayName = v
			} else {
				props.Description = v
			}
		default:
			if refusedOnCreate(p.Name) {
				dav.WriteError(w, dav.Forbidden(dav.CannotModifyProtectedProperty))
				return
			}
			// Other properties are skipped.
		}
	}
	if hasType {
		ok := slices.Contains(cr.ResourceTypes, dav.Addressbook)
		for _, rt := range cr.ResourceTypes {
			if rt != dav.Addressbook && rt != dav.Collection {
				ok = false
			}
		}
		if !ok {
			dav.WriteError(w, dav.Forbidden(dav.ValidResourceType))
			return
		}
	}

	_, err = h.store.CreateCollection(r.Context(), h.bookRef(owner, t.book), props)
	if err != nil {
		if errors.Is(err, store.ErrExists) {
			// MKCOL on something that exists: advertise what it does allow.
			w.Header().Set("Allow", strings.Join(slices.DeleteFunc(allowed(t.kind), func(m string) bool { return m == "MKCOL" }), ", "))
		}
		h.fail(w, r, t, err)
		return
	}
	w.Header().Set("Location", h.bookHref(t.user, t.book))
	w.WriteHeader(http.StatusCreated)
}

// proppatch sets or removes the display name and description. It is atomic:
// if any property cannot be changed, none is, the offending properties get
// 403 and the others 424.
func (h *Handler) proppatch(w http.ResponseWriter, r *http.Request, t target, owner string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	updates, err := dav.ParsePropPatch(body)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	ref := h.bookRef(owner, t.book)
	if _, err := h.store.GetCollection(r.Context(), ref); err != nil {
		h.fail(w, r, t, err)
		return
	}

	// A property named twice is applied last-wins and answered once.
	var upd store.PropsUpdate
	var denied, dependent []dav.Prop
	seenDenied := map[xml.Name]bool{}
	seenOK := map[xml.Name]bool{}
	for _, u := range updates {
		bad := !settable(u.Name)
		value := ""
		if !bad && !u.Remove {
			var verr *dav.Error
			value, verr = textValue(u.Name, u.Node)
			bad = verr != nil
		}
		if bad {
			if !seenDenied[u.Name] {
				seenDenied[u.Name] = true
				denied = append(denied, dav.EmptyProp(u.Name))
			}
			continue
		}
		if !seenOK[u.Name] {
			seenOK[u.Name] = true
			dependent = append(dependent, dav.EmptyProp(u.Name))
		}
		if u.Name == dav.DisplayName {
			upd.DisplayName = &value
		} else {
			upd.Description = &value
		}
	}
	// A property that was refused in any of its occurrences is not "dependent".
	dependent = slices.DeleteFunc(dependent, func(p dav.Prop) bool { return seenDenied[p.Name] })

	var stats []dav.Propstat
	if len(denied) == 0 {
		if _, err := h.store.UpdateCollection(r.Context(), ref, upd); err != nil {
			h.fail(w, r, t, err)
			return
		}
		stats = []dav.Propstat{{Status: http.StatusOK, Props: dependent}}
	} else {
		stats = []dav.Propstat{{Status: http.StatusForbidden, Props: denied}}
		if len(dependent) > 0 {
			stats = append(stats, dav.Propstat{Status: http.StatusFailedDependency, Props: dependent})
		}
	}
	resp := dav.Response{Href: h.bookHref(t.user, t.book), Propstats: stats}
	if err := dav.WriteMultiStatus(w, slices.Values([]dav.Response{resp}), ""); err != nil {
		h.log.Warn("cannot write PROPPATCH response", "collection", t.book, "error", err)
	}
}

// deleteCollection removes an address book with its objects. Conditional
// headers and Depth are ignored.
func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request, t target, owner string) {
	if err := h.store.DeleteCollection(r.Context(), h.bookRef(owner, t.book)); err != nil {
		h.fail(w, r, t, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
