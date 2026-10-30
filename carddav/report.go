// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"errors"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/vcard"
)

// maxQueryObjects is the most objects one addressbook-query examines. A
// larger book is cut at this many and answered like a nresults truncation.
// It is a variable only so that tests can lower it.
var maxQueryObjects = 100_000

const (
	// maxMultigetHrefs is the most DAV:href elements one multiget accepts.
	maxMultigetHrefs = 1000
)

// report answers REPORT on an address book. The Depth header is ignored.
// The body picks the report by its root element.
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
	// The book must exist whatever the report.
	if _, err := h.store.GetCollection(r.Context(), h.bookRef(owner, t.book)); err != nil {
		h.fail(w, r, t, err)
		return
	}
	switch root.Name {
	case dav.AddressbookQuery:
		h.query(w, r, t, owner, root)
	case dav.AddressbookMultiget:
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
// Limitation: a CR:address-data property may carry CR:prop children and
// content-type or version attributes to ask for a subset of the card or a
// conversion. They are ignored and the stored bytes are returned whole.
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

// nresults reads CR:limit/CR:nresults. ok is false when the body sets no
// limit.
func nresults(root *dav.Node) (n int, ok bool, err error) {
	lim := root.Child(dav.CRLimit)
	if lim == nil {
		return 0, false, nil
	}
	nr := lim.Child(dav.CRNResults)
	if nr == nil {
		return 0, false, nil
	}
	v, perr := strconv.ParseUint(strings.TrimSpace(nr.Text), 10, 31)
	switch {
	case errors.Is(perr, strconv.ErrRange):
		// Larger than any book: no limit.
		return 0, false, nil
	case perr != nil:
		return 0, false, dav.BadRequest("invalid nresults")
	}
	return int(v), true, nil
}

// query answers an addressbook-query report (RFC 6352 section 8.6). Objects
// are read and matched while the response streams, one at a time; one that
// cannot be read or parsed is skipped.
func (h *Handler) query(w http.ResponseWriter, r *http.Request, t target, owner string, root *dav.Node) {
	var flt *vcard.Filter
	if fn := root.Child(dav.CRFilter); fn != nil {
		f, err := parseFilter(fn)
		if err != nil {
			dav.WriteError(w, filterError(err))
			return
		}
		if len(f.Props) > 0 {
			flt = f
		}
	}
	limit, limited, err := nresults(root)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	req, err := propRequestFromNode(root)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	ref := h.bookRef(owner, t.book)
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
	wantData := !req.PropName && slices.Contains(req.Props, dav.AddressData)
	// candidate reads and filters one object. Data is read only when the
	// filter or the response needs it, and then once: the response is built
	// from the same read that was matched, so it cannot mix versions.
	candidate := func(o store.Object) (dav.Response, bool) {
		var data []byte
		if flt != nil || wantData {
			full, err := h.store.GetObject(ctx, ref, o.Name)
			if err != nil {
				h.log.Warn("skipping unreadable object", "collection", t.book, "object", o.Name, "error", errKind(err))
				return dav.Response{}, false
			}
			o, data = full, full.Data
			// Data that is read is parsed even when no filter applies:
			// clients that parse every address-data they get would fail
			// on an object that is no card.
			card, err := vcard.Parse(data)
			if err != nil {
				h.log.Warn("skipping unparsable object", "collection", t.book, "object", o.Name, "error", errKind(err))
				return dav.Response{}, false
			}
			if flt != nil && !flt.Match(card) {
				return dav.Response{}, false
			}
		}
		res := h.objectResource(t.user, t.book, o, func() ([]byte, error) { return data, nil })
		return dav.PropFind(req, res), true
	}

	responses := func(yield func(dav.Response) bool) {
		sent := 0
		for _, o := range objs {
			if ctx.Err() != nil {
				return
			}
			resp, ok := candidate(o)
			if !ok {
				continue
			}
			if limited && sent >= limit {
				// One more match than allowed: the result is truncated.
				yield(truncated(h.bookHref(t.user, t.book)))
				return
			}
			if !yield(resp) {
				return
			}
			sent++
		}
		if capped {
			yield(truncated(h.bookHref(t.user, t.book)))
		}
	}
	h.writeReport(w, r, t, responses)
}

func truncated(href string) dav.Response {
	return dav.Response{
		Href:   href,
		Status: http.StatusInsufficientStorage,
		Error:  &dav.Error{Precondition: dav.NumberOfMatchesWithinLimits},
	}
}

func (h *Handler) writeReport(w http.ResponseWriter, r *http.Request, t target, responses iter.Seq[dav.Response]) {
	if err := dav.WriteMultiStatus(w, responses, ""); err != nil {
		h.log.Warn("report response cut short", "collection", t.book, "error", err)
	}
}

// errKind names the class of err without its text, which may hold paths.
func errKind(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "not found"
	case errors.Is(err, vcard.ErrInvalid):
		return "syntax"
	default:
		return "failure"
	}
}

// multiget answers an addressbook-multiget report (RFC 6352 section 8.7).
// Only hrefs inside the requested book of the requested user are read; any
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
	ref := h.bookRef(owner, t.book)
	ctx := r.Context()

	responses := func(yield func(dav.Response) bool) {
		seen := make(map[string]bool, len(hrefs))
		for _, hn := range hrefs {
			if ctx.Err() != nil {
				return
			}
			given := hn.Text
			name, ok := h.bookObjectName(given, t)
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
				if !yield(status(h.objectHref(t.user, t.book, name), http.StatusNotFound)) {
					return
				}
			case err != nil:
				h.log.Warn("cannot read object", "collection", t.book, "object", name, "error", errKind(err))
				if !yield(status(h.objectHref(t.user, t.book, name), http.StatusInternalServerError)) {
					return
				}
			default:
				data := o.Data
				res := h.objectResource(t.user, t.book, o, func() ([]byte, error) { return data, nil })
				if !yield(dav.PropFind(req, res)) {
					return
				}
			}
		}
	}
	h.writeReport(w, r, t, responses)
}

func status(href string, code int) dav.Response {
	return dav.Response{Href: href, Status: code}
}

// bookObjectName returns the object name an href addresses when the href is
// exactly an object of the requested book of the requested user. The href
// may be an absolute URL; only its path counts.
func (h *Handler) bookObjectName(href string, t target) (string, bool) {
	href = strings.TrimSpace(href)
	if href == "" || len(href) > 4096 || !utf8.ValidString(href) {
		return "", false
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), h.prefix+"/addressbooks/")
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
	if clean[0] != t.user || clean[1] != t.book {
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
