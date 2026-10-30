// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"errors"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit/ical"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// maxQueryObjects is the most objects one calendar-query examines. A larger
// calendar is cut at this many and answered like a truncation (507). It is a
// variable only so that tests can lower it.
var maxQueryObjects = 100_000

const (
	// maxMultigetHrefs is the most DAV:href elements one multiget accepts.
	maxMultigetHrefs = 1000
)

// report answers REPORT on a calendar. The Depth header is ignored. The body
// picks the report by its root element.
func (h *Handler) report(w http.ResponseWriter, r *http.Request, t target, owner string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	root, err := dav.ParseXML(body)
	if err != nil {
		dav.WriteErr(w, dav.BadRequest("malformed XML"))
		return
	}
	// The calendar must exist whatever the report.
	if _, err := h.store.GetCollection(r.Context(), h.calendarRef(owner, t.calendar)); err != nil {
		h.fail(w, r, t, err)
		return
	}
	switch root.Name {
	case dav.CalendarQuery:
		h.query(w, r, t, owner, root)
	case dav.CalendarMultiget:
		h.multiget(w, r, t, owner, root)
	case dav.SyncCollection:
		h.syncCollection(w, r, t, owner, root)
	default:
		dav.WriteError(w, dav.Forbidden(dav.SupportedReport))
	}
}

// propRequestFromNode reads the property selection of a report body: a
// DAV:prop, DAV:allprop (with an optional DAV:include) or DAV:propname child
// of root. A body that selects nothing means allprop.
//
// Limitation: a C:calendar-data property may carry C:comp, C:expand and
// C:limit-recurrence-set children, and content-type or version attributes,
// to ask for a subset of the object or a conversion. They are ignored and
// the stored bytes are returned whole.
func propRequestFromNode(root *dav.Node) (dav.PropfindRequest, error) {
	var req dav.PropfindRequest
	if root.Child(dav.PropName) != nil {
		req.PropName = true
		return req, nil
	}
	var err error
	if p := root.Child(dav.PropEl); p != nil {
		req.Props, err = dav.PropNames(p.Children)
		return req, err
	}
	req.AllProp = true
	if inc := root.Child(dav.Include); inc != nil {
		req.Props, err = dav.PropNames(inc.Children)
	}
	return req, err
}

// query answers a calendar-query report (RFC 4791 section 7.8). Objects are
// read and matched while the response streams, one at a time; one that
// cannot be read or parsed is skipped.
func (h *Handler) query(w http.ResponseWriter, r *http.Request, t target, owner string, root *dav.Node) {
	var flt *ical.CompFilter
	if fn := root.Child(dav.CalFilter); fn != nil {
		f, err := parseCompFilter(fn)
		if err != nil {
			dav.WriteError(w, filterError(err))
			return
		}
		if !matchesAll(f) {
			flt = f
		}
	}
	req, err := propRequestFromNode(root)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	ref := h.calendarRef(owner, t.calendar)
	// No data: only candidates that need filtering are read, one by one.
	objs, err := h.store.ListObjects(r.Context(), ref, store.ListOptions{})
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	capped := len(objs) > maxQueryObjects
	if capped {
		objs = objs[:maxQueryObjects]
	}

	ctx := r.Context()
	wantData := !req.PropName && slices.Contains(req.Props, dav.CalendarData)
	// candidate reads and filters one object. Data is read only when the
	// filter or the response needs it, and then once: the response is built
	// from the same read that was matched, so it cannot mix versions.
	candidate := func(o store.Object) (dav.Response, bool) {
		var data []byte
		if flt != nil || wantData {
			full, err := h.store.GetObject(ctx, ref, o.Name)
			if err != nil {
				h.log.Warn("skipping unreadable object", "collection", t.calendar, "object", o.Name, "error", errKind(err))
				return dav.Response{}, false
			}
			o, data = full, full.Data
			// Data that is read is parsed even when the filter selects
			// everything: an object that is no calendar matches no
			// VCALENDAR comp-filter, and clients that parse every
			// calendar-data they get would fail on it.
			cal, err := ical.Parse(data)
			if err != nil {
				h.log.Warn("skipping unparsable object", "collection", t.calendar, "object", o.Name, "error", errKind(err))
				return dav.Response{}, false
			}
			if flt != nil {
				// Floating times are read in UTC.
				ok, err := cal.Matches(flt, time.UTC)
				if err != nil {
					// The filter was validated, so this is a fault.
					h.log.Error("calendar query failed", "collection", t.calendar, "object", o.Name, "error", errKind(err))
					return dav.Response{}, false
				}
				if !ok {
					return dav.Response{}, false
				}
			}
		}
		res := h.objectResource(t.user, t.calendar, o, func() ([]byte, error) { return data, nil })
		return dav.PropFind(req, res), true
	}

	responses := func(yield func(dav.Response) bool) {
		for _, o := range objs {
			if ctx.Err() != nil {
				return
			}
			resp, ok := candidate(o)
			if !ok {
				continue
			}
			if !yield(resp) {
				return
			}
		}
		if capped {
			yield(truncated(h.calendarHref(t.user, t.calendar)))
		}
	}
	h.writeReport(w, t, responses)
}

// matchesAll reports whether a filter selects every object, so that nothing
// need be read to evaluate it: no filter at all, or a bare VCALENDAR
// comp-filter without conditions.
func matchesAll(f *ical.CompFilter) bool {
	return f == nil || (!f.IsNotDefined && f.TimeRange == nil && len(f.Props) == 0 && len(f.Comps) == 0)
}

func truncated(href string) dav.Response {
	return dav.Response{
		Href:   href,
		Status: http.StatusInsufficientStorage,
		Error:  &dav.Error{Precondition: dav.NumberOfMatchesWithinLimits},
	}
}

func (h *Handler) writeReport(w http.ResponseWriter, t target, responses iter.Seq[dav.Response]) {
	if err := dav.WriteMultiStatus(w, responses, ""); err != nil {
		h.log.Warn("report response cut short", "collection", t.calendar, "error", err)
	}
}

// errKind names the class of err without its text, which may hold paths.
func errKind(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "not found"
	case errors.Is(err, ical.ErrInvalid):
		return "syntax"
	case errors.Is(err, ical.ErrFilter):
		return "filter"
	default:
		return "failure"
	}
}

// multiget answers a calendar-multiget report (RFC 4791 section 7.9). Only
// hrefs inside the requested calendar of the requested user are read; any
// other href is a 404 response and never reaches the store.
func (h *Handler) multiget(w http.ResponseWriter, r *http.Request, t target, owner string, root *dav.Node) {
	hrefs := root.Kids(dav.Href)
	if len(hrefs) > maxMultigetHrefs {
		dav.WriteErr(w, dav.BadRequest("too many hrefs"))
		return
	}
	req, err := propRequestFromNode(root)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	ref := h.calendarRef(owner, t.calendar)
	ctx := r.Context()

	responses := func(yield func(dav.Response) bool) {
		seen := make(map[string]bool, len(hrefs))
		for _, hn := range hrefs {
			if ctx.Err() != nil {
				return
			}
			given := hn.Text
			name, ok := h.calendarObjectName(given, t)
			key := given
			if ok {
				key = "\x00" + name // the same object under two spellings
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			if !ok {
				if !yield(status(safeHref(given), http.StatusNotFound)) {
					return
				}
				continue
			}
			o, err := h.store.GetObject(ctx, ref, name)
			switch {
			case errors.Is(err, store.ErrNotFound):
				if !yield(status(h.objectHref(t.user, t.calendar, name), http.StatusNotFound)) {
					return
				}
			case err != nil:
				h.log.Warn("cannot read object", "collection", t.calendar, "object", name, "error", errKind(err))
				if !yield(status(h.objectHref(t.user, t.calendar, name), http.StatusInternalServerError)) {
					return
				}
			default:
				data := o.Data
				res := h.objectResource(t.user, t.calendar, o, func() ([]byte, error) { return data, nil })
				if !yield(dav.PropFind(req, res)) {
					return
				}
			}
		}
	}
	h.writeReport(w, t, responses)
}

func status(href string, code int) dav.Response {
	return dav.Response{Href: href, Status: code}
}

// calendarObjectName returns the object name an href addresses when the href
// is exactly an object of the requested calendar of the requested user. The
// href may be an absolute URL; only its path counts.
func (h *Handler) calendarObjectName(href string, t target) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || len(href) > 4096 || !utf8.ValidString(href) {
		return "", false
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), h.prefix+"/calendars/")
	if !ok {
		return "", false
	}
	segs := strings.Split(rest, "/")
	if len(segs) != 3 {
		return "", false
	}
	var clean [3]string
	for i, s := range segs {
		c, err := dav.CleanSegment(s)
		if err != nil {
			return "", false
		}
		clean[i] = c
	}
	if clean[0] != t.user || clean[1] != t.calendar {
		return "", false
	}
	return clean[2], true
}

// safeHref echoes a client's href in a response. It is returned as given when
// that is plain text, and percent-encoded otherwise.
func safeHref(s string) string {
	if utf8.ValidString(s) && len(s) <= 4096 {
		clean := true
		for i := 0; i < len(s); i++ {
			if s[i] < 0x20 || s[i] == 0x7f {
				clean = false
				break
			}
		}
		if clean {
			return s
		}
	}
	if len(s) > 4096 {
		s = s[:4096]
	}
	return url.PathEscape(strings.ToValidUTF8(s, "�"))
}
