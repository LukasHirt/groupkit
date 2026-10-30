// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit/ical"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// Limits on the properties of a calendar.
const (
	maxDisplayNameBytes = 255
	maxDescriptionBytes = 4096
	maxTimezoneBytes    = 64 << 10
	maxOrder            = 1000000
	maxComponents       = 8
	maxComponentName    = 32
)

// settable reports whether a calendar property can be set by clients after
// the calendar exists.
func settable(name xml.Name) bool {
	switch name {
	case dav.DisplayName, dav.CalendarDescription, dav.CalendarColor, dav.CalendarOrder, dav.CalendarTimezone:
		return true
	}
	return false
}

// protectedOnCreate lists the CalDAV and CalendarServer properties that the
// server computes. Asking to set one when creating a calendar is refused.
var protectedOnCreate = map[xml.Name]bool{
	dav.GetCTag: true,
	dav.N(dav.NSCalDAV, "supported-calendar-data"):    true,
	dav.N(dav.NSCalDAV, "max-resource-size"):          true,
	dav.N(dav.NSCalDAV, "supported-collation-set"):    true,
	dav.N(dav.NSCalDAV, "min-date-time"):              true,
	dav.N(dav.NSCalDAV, "max-date-time"):              true,
	dav.N(dav.NSCalDAV, "max-instances"):              true,
	dav.N(dav.NSCalDAV, "max-attendees-per-instance"): true,
	dav.N(dav.NSCalDAV, "calendar-home-set"):          true,
	dav.N(dav.NSCalDAV, "calendar-user-address-set"):  true,
}

// refusedOnCreate reports whether a property that the engine does not handle
// fails the creation. A DAV: property is live or protected, and so is a
// computed CalDAV one. Anything else, such as Apple's schedule-calendar-transp
// or refreshrate, is skipped: clients send those with every new calendar and
// are not helped by a refusal.
func refusedOnCreate(name xml.Name) bool {
	return name.Space == dav.NSDAV || protectedOnCreate[name]
}

// propText returns the text of a property element with surrounding whitespace
// removed. An element with child elements has no text value, and the value
// must be valid UTF-8 and at most limit bytes.
func propText(n *dav.Node, limit int) (string, *dav.Error) {
	if len(n.Children) > 0 {
		return "", dav.BadRequest("property value has child elements")
	}
	if !utf8.ValidString(n.Text) {
		return "", dav.BadRequest("property value is not valid UTF-8")
	}
	s := strings.TrimSpace(n.Text)
	if len(s) > limit {
		return "", dav.BadRequest("property value is too long")
	}
	return s, nil
}

// validColor reports whether s is #RRGGBB or #RRGGBBAA.
func validColor(s string) bool {
	if len(s) != 7 && len(s) != 9 || s[0] != '#' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// validTimezone checks that s is an iCalendar object holding exactly one
// VTIMEZONE and nothing else.
func validTimezone(s string) bool {
	cal, err := ical.Parse([]byte(s))
	if err != nil {
		return false
	}
	// RFC 4791 section 5.2.2: exactly one VTIMEZONE and no other component.
	return len(cal.Children) == 1 && len(cal.Comps("VTIMEZONE")) == 1
}

// propValue converts the value of a settable property into an update of that
// property. A blank value unsets the property, except for the order. A removal
// sets the empty value (0 for the order).
func propValue(name xml.Name, n *dav.Node, remove bool, upd *store.PropsUpdate) *dav.Error {
	empty := ""
	if remove {
		switch name {
		case dav.DisplayName:
			upd.DisplayName = &empty
		case dav.CalendarDescription:
			upd.Description = &empty
		case dav.CalendarColor:
			upd.Color = &empty
		case dav.CalendarOrder:
			zero := 0
			upd.Order = &zero
		case dav.CalendarTimezone:
			upd.Timezone = &empty
		}
		return nil
	}
	switch name {
	case dav.DisplayName:
		v, err := propText(n, maxDisplayNameBytes)
		if err != nil {
			return err
		}
		upd.DisplayName = &v
	case dav.CalendarDescription:
		v, err := propText(n, maxDescriptionBytes)
		if err != nil {
			return err
		}
		upd.Description = &v
	case dav.CalendarColor:
		v, err := propText(n, 16)
		if err != nil {
			return err
		}
		if v != "" && !validColor(v) {
			return dav.BadRequest("invalid calendar-color")
		}
		upd.Color = &v
	case dav.CalendarOrder:
		v, err := propText(n, 16)
		if err != nil {
			return err
		}
		i, cerr := strconv.Atoi(v)
		if cerr != nil || i < -maxOrder || i > maxOrder {
			return dav.BadRequest("invalid calendar-order")
		}
		upd.Order = &i
	case dav.CalendarTimezone:
		v, err := propText(n, maxTimezoneBytes)
		if err != nil {
			return err
		}
		if v != "" && !validTimezone(v) {
			return dav.BadRequest("invalid calendar-timezone")
		}
		upd.Timezone = &v
	}
	return nil
}

// componentSet reads the children of supported-calendar-component-set: comp
// elements whose name is letters only. Names are upper-cased and listed once.
func componentSet(n *dav.Node) ([]string, *dav.Error) {
	if len(n.Children) == 0 || len(n.Children) > 4*maxComponents {
		return nil, dav.BadRequest("invalid supported-calendar-component-set")
	}
	var out []string
	for _, c := range n.Children {
		if c.Name != dav.CalComp {
			return nil, dav.BadRequest("supported-calendar-component-set holds only comp elements")
		}
		name := c.Attr("name")
		if name == "" || len(name) > maxComponentName {
			return nil, dav.BadRequest("invalid component name")
		}
		for i := 0; i < len(name); i++ {
			if ch := name[i]; (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') {
				return nil, dav.BadRequest("invalid component name")
			}
		}
		name = strings.ToUpper(name)
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) > maxComponents {
		return nil, dav.BadRequest("too many components")
	}
	return out, nil
}

// create makes a calendar for MKCALENDAR and extended MKCOL. Both bodies are
// optional. Any property that cannot be set, or any invalid value, refuses the
// request and creates nothing.
func (h *Handler) create(w http.ResponseWriter, r *http.Request, t target, owner string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	var cr dav.CollectionRequest
	if r.Method == "MKCALENDAR" {
		cr, err = dav.ParseMkcalendar(body)
	} else {
		cr, err = dav.ParseMkcol(body)
	}
	if err != nil {
		dav.WriteErr(w, err)
		return
	}

	var upd store.PropsUpdate
	var components []string
	hasType := false
	for _, p := range cr.Props {
		switch {
		case p.Name == dav.ResourceType:
			hasType = true
		case p.Name == dav.SupportedCalendarComponentSet:
			c, verr := componentSet(p.Node)
			if verr != nil {
				dav.WriteError(w, verr)
				return
			}
			components = c
		case settable(p.Name):
			if verr := propValue(p.Name, p.Node, false, &upd); verr != nil {
				dav.WriteError(w, verr)
				return
			}
		case refusedOnCreate(p.Name):
			dav.WriteError(w, dav.Forbidden(dav.CannotModifyProtectedProperty))
			return
		}
		// Other properties are skipped.
	}
	// A calendar that cannot hold events would be useless: every write to it
	// is refused. A set with VEVENT among others is created; only VEVENT is
	// advertised for it.
	// An existing name stays a 405 whatever the body says.
	if components != nil && !slices.Contains(components, "VEVENT") {
		if _, gerr := h.store.GetCollection(r.Context(), h.calendarRef(owner, t.calendar)); gerr == nil {
			h.createFailed(w, r, t, store.ErrExists)
			return
		}
		dav.WriteError(w, dav.Forbidden(dav.SupportedCalendarComponent))
		return
	}
	if r.Method == "MKCOL" && !hasType {
		// A calendar needs C:calendar in the resourcetype; only MKCALENDAR
		// creates one without it. An existing name stays a 405.
		if _, gerr := h.store.GetCollection(r.Context(), h.calendarRef(owner, t.calendar)); gerr == nil {
			h.createFailed(w, r, t, store.ErrExists)
			return
		}
		dav.WriteError(w, dav.Forbidden(dav.ValidResourceType))
		return
	}
	if hasType {
		ok := slices.Contains(cr.ResourceTypes, dav.Calendar)
		for _, rt := range cr.ResourceTypes {
			if rt != dav.Calendar && rt != dav.Collection {
				ok = false
			}
		}
		if !ok {
			dav.WriteError(w, dav.Forbidden(dav.ValidResourceType))
			return
		}
	}

	props := store.Props{Components: components}
	if upd.DisplayName != nil {
		props.DisplayName = *upd.DisplayName
	}
	if upd.Description != nil {
		props.Description = *upd.Description
	}
	if upd.Color != nil {
		props.Color = *upd.Color
	}
	if upd.Order != nil {
		props.Order = *upd.Order
	}
	if upd.Timezone != nil {
		props.Timezone = *upd.Timezone
	}

	if _, err := h.store.CreateCollection(r.Context(), h.calendarRef(owner, t.calendar), props); err != nil {
		h.createFailed(w, r, t, err)
		return
	}
	w.Header().Set("Location", h.calendarHref(t.user, t.calendar))
	w.WriteHeader(http.StatusCreated)
}

// createFailed answers a failed creation. Allow is advertised only for an
// existing name, where it says what the calendar does allow.
func (h *Handler) createFailed(w http.ResponseWriter, r *http.Request, t target, err error) {
	if errors.Is(err, store.ErrExists) {
		w.Header().Set("Allow", strings.Join(slices.DeleteFunc(allowed(t.kind), func(m string) bool {
			return m == "MKCALENDAR" || m == "MKCOL"
		}), ", "))
	}
	h.fail(w, r, t, err)
}

// proppatch sets or removes the display name, description, colour, order and
// time zone. Invalid values are answered 403, like the other refusals, on
// purpose: clients do not tell the codes of a failed property apart. It is
// atomic: if any property cannot be changed, none is, the offending
// properties get 403 and the others 424. The component set cannot
// be changed once the calendar exists.
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
	ref := h.calendarRef(owner, t.calendar)
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
		var one store.PropsUpdate
		bad := !settable(u.Name)
		if !bad {
			bad = propValue(u.Name, u.Node, u.Remove, &one) != nil
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
		switch u.Name {
		case dav.DisplayName:
			upd.DisplayName = one.DisplayName
		case dav.CalendarDescription:
			upd.Description = one.Description
		case dav.CalendarColor:
			upd.Color = one.Color
		case dav.CalendarOrder:
			upd.Order = one.Order
		case dav.CalendarTimezone:
			upd.Timezone = one.Timezone
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
	resp := dav.Response{Href: h.calendarHref(t.user, t.calendar), Propstats: stats}
	if err := dav.WriteMultiStatus(w, slices.Values([]dav.Response{resp}), ""); err != nil {
		h.log.Warn("cannot write PROPPATCH response", "collection", t.calendar, "error", err)
	}
}

// deleteCollection removes a calendar with its objects. Conditional headers
// and Depth are ignored.
func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request, t target, owner string) {
	if err := h.store.DeleteCollection(r.Context(), h.calendarRef(owner, t.calendar)); err != nil {
		h.fail(w, r, t, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
