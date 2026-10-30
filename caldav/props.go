// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"context"
	"encoding/xml"
	"slices"
	"strconv"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// icalContentType is the content type of calendar objects.
const icalContentType = "text/calendar; charset=utf-8"

// defaultComponents is the component set of a calendar that stores none, and
// servedComponents the components the engine accepts and answers queries for.
var (
	defaultComponents = []string{"VEVENT"}
	servedComponents  = []string{"VEVENT"}
)

// advertisedComponents returns the supported-calendar-component-set of a
// calendar: the stored set limited to the components the engine serves, or
// the default when that leaves none. A client is never told it may write a
// component that every PUT would refuse.
func advertisedComponents(stored []string) []string {
	var out []string
	for _, c := range stored {
		if slices.Contains(servedComponents, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return defaultComponents
	}
	return out
}

// Privileges granted per kind of resource. The engine does no per-object
// access control, so these describe what the endpoint supports.
var (
	homePrivileges     = []xml.Name{dav.Read, dav.Bind}
	calendarPrivileges = []xml.Name{dav.Read, dav.Write, dav.WriteProperties, dav.WriteContent, dav.Bind, dav.Unbind}
	objectPrivileges   = []xml.Name{dav.Read, dav.Write, dav.WriteContent}
)

// calendarReports are the reports a calendar supports.
var calendarReports = []xml.Name{dav.CalendarQuery, dav.CalendarMultiget, dav.SyncCollection}

// principalHref returns the escaped URL of a user's principal resource.
func (h *Handler) principalHref(user string) string {
	return h.prefix + "/principals/" + dav.EscapeHref(user) + "/"
}

// calendarHref returns the escaped URL of a calendar, with a trailing slash.
func (h *Handler) calendarHref(user, calendar string) string {
	return h.prefix + "/calendars/" + dav.EscapeHref(user, calendar) + "/"
}

// objectHref returns the escaped URL of an object.
func (h *Handler) objectHref(user, calendar, name string) string {
	return h.prefix + "/calendars/" + dav.EscapeHref(user, calendar, name)
}

// fixed is a property whose value is already known.
func fixed(p dav.Prop) dav.PropDef {
	return dav.PropDef{AllProp: true, Get: func() (dav.Prop, error) { return p, nil }}
}

func text(name xml.Name, s string) dav.PropDef { return fixed(dav.TextProp(name, s)) }

// optionalText is a property that DAV:allprop lists only when it has a
// value. Asked for by name while empty, it is an empty element.
func optionalText(name xml.Name, s string) dav.PropDef {
	d := text(name, s)
	d.AllProp = s != ""
	return d
}

// withAllProp sets whether DAV:allprop lists the property.
func withAllProp(d dav.PropDef, all bool) dav.PropDef {
	d.AllProp = all
	return d
}

func privilegeSet(privs []xml.Name) dav.Prop {
	return dav.Prop{Name: dav.CurrentUserPrivilegeSet, Write: func(w *dav.Writer) {
		for _, p := range privs {
			w.Start(dav.Privilege)
			w.Empty(p)
			w.End()
		}
	}}
}

func reportSet(reports []xml.Name) dav.Prop {
	if len(reports) == 0 {
		return dav.EmptyProp(dav.SupportedReportSet)
	}
	return dav.Prop{Name: dav.SupportedReportSet, Write: func(w *dav.Writer) {
		for _, r := range reports {
			w.Start(dav.SupportedReport)
			w.Start(dav.Report)
			w.Empty(r)
			w.End()
			w.End()
		}
	}}
}

// commonProps are the properties every resource has.
func (h *Handler) commonProps(user string, privs, reports []xml.Name, types ...xml.Name) map[xml.Name]dav.PropDef {
	principal := h.principalHref(user)
	return map[xml.Name]dav.PropDef{
		dav.ResourceType:            fixed(dav.TypesProp(types...)),
		dav.CurrentUserPrincipal:    fixed(dav.HrefProp(dav.CurrentUserPrincipal, principal)),
		dav.Owner:                   fixed(dav.HrefProp(dav.Owner, principal)),
		dav.CurrentUserPrivilegeSet: fixed(privilegeSet(privs)),
		dav.SupportedReportSet:      fixed(reportSet(reports)),
	}
}

// homeResource describes a user's calendar home. The URL user is the display
// name.
func (h *Handler) homeResource(user string) dav.Resource {
	props := h.commonProps(user, homePrivileges, nil, dav.Collection)
	props[dav.DisplayName] = text(dav.DisplayName, user)
	return dav.Resource{Href: h.HomeHref(user), Props: props}
}

// calendarResource describes a calendar. The description, colour and time
// zone are properties only when set; the description is also an empty element
// when asked for while unset, the others are then not found. The order is
// different: store.Props.Order cannot tell 0 from unset, so it is always
// answered when asked for (0 included) and listed by allprop only when it is
// not 0.
func (h *Handler) calendarResource(user string, c store.Collection) dav.Resource {
	props := h.commonProps(user, calendarPrivileges, calendarReports, dav.Collection, dav.Calendar)
	name := c.Props.DisplayName
	if name == "" {
		name = c.Ref.Name
	}
	props[dav.DisplayName] = text(dav.DisplayName, name)
	props[dav.GetCTag] = text(dav.GetCTag, strconv.FormatUint(c.Revision, 10))
	props[dav.SyncToken] = text(dav.SyncToken, dav.FormatSyncToken(c))
	props[dav.CalendarDescription] = optionalText(dav.CalendarDescription, c.Props.Description)
	if c.Props.Color != "" {
		props[dav.CalendarColor] = text(dav.CalendarColor, c.Props.Color)
	}
	props[dav.CalendarOrder] = text(dav.CalendarOrder, strconv.Itoa(c.Props.Order))
	props[dav.CalendarOrder] = withAllProp(props[dav.CalendarOrder], c.Props.Order != 0)
	if c.Props.Timezone != "" {
		props[dav.CalendarTimezone] = text(dav.CalendarTimezone, c.Props.Timezone)
	}
	comps := advertisedComponents(c.Props.Components)
	props[dav.SupportedCalendarComponentSet] = fixed(dav.Prop{Name: dav.SupportedCalendarComponentSet, Write: func(w *dav.Writer) {
		for _, comp := range comps {
			w.Start(dav.CalComp, dav.Attr{Name: "name", Value: comp})
			w.End()
		}
	}})
	props[dav.SupportedCalendarData] = fixed(dav.Prop{Name: dav.SupportedCalendarData, Write: func(w *dav.Writer) {
		w.Start(dav.CalendarData,
			dav.Attr{Name: "content-type", Value: "text/calendar"},
			dav.Attr{Name: "version", Value: "2.0"})
		w.End()
	}})
	props[dav.CalMaxResourceSize] = text(dav.CalMaxResourceSize, strconv.FormatInt(h.maxSize, 10))
	return dav.Resource{Href: h.calendarHref(user, c.Ref.Name), Props: props}
}

// objectResource describes a calendar object from its listing entry. data
// fetches the object's bytes; it is only called when calendar-data is asked
// for by name, so a listing reads no object data.
func (h *Handler) objectResource(user, calendar string, o store.Object, data func() ([]byte, error)) dav.Resource {
	props := h.commonProps(user, objectPrivileges, nil)
	props[dav.DisplayName] = text(dav.DisplayName, o.Name)
	props[dav.GetETag] = text(dav.GetETag, dav.QuoteETag(o.ETag))
	props[dav.GetContentType] = text(dav.GetContentType, icalContentType)
	props[dav.GetContentLength] = text(dav.GetContentLength, strconv.FormatInt(o.Size, 10))
	props[dav.GetLastModified] = text(dav.GetLastModified, dav.HTTPDate(o.ModTime))
	props[dav.CalendarData] = dav.PropDef{ // AllProp is false: it is heavy
		Get: func() (dav.Prop, error) {
			b, err := data()
			if err != nil {
				return dav.Prop{}, err
			}
			s := string(b)
			return dav.Prop{Name: dav.CalendarData, Write: func(w *dav.Writer) { w.Text(s) }}, nil
		},
	}
	return dav.Resource{Href: h.objectHref(user, calendar, o.Name), Props: props}
}

// objectResponse builds the PROPFIND response for one object of a calendar.
// The object's data is read from the store only if the request names
// calendar-data.
func (h *Handler) objectResponse(ctx context.Context, owner, user, calendar string, o store.Object, req dav.PropfindRequest) dav.Response {
	ref := store.CollectionRef{Owner: owner, Kind: store.KindCalendar, Name: calendar}
	res := h.objectResource(user, calendar, o, func() ([]byte, error) {
		full, err := h.store.GetObject(ctx, ref, o.Name)
		if err != nil {
			h.log.Warn("cannot read object", "collection", calendar, "error", err)
			return nil, err
		}
		return full.Data, nil
	})
	return dav.PropFind(req, res)
}
