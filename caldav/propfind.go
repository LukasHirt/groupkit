// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"iter"
	"net/http"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// propfind answers PROPFIND on a home, a calendar or an object. Depth
// infinity and a missing Depth header are served as Depth 1.
func (h *Handler) propfind(w http.ResponseWriter, r *http.Request, t target, owner string) {
	body, err := dav.ReadBody(w, r, dav.MaxXMLBytes)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	req, err := dav.ParsePropfind(body)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	depth, err := dav.ParseDepth(r.Header.Get("Depth"), dav.Depth1)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}

	var responses iter.Seq[dav.Response]
	switch t.kind {
	case kindHome:
		responses, err = h.homeResponses(r, t, owner, req, depth)
	case kindCollection:
		responses, err = h.calendarResponses(r, t, owner, req, depth)
	default:
		responses, err = h.objectResponses(r, t, owner, req)
	}
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	if err := dav.WriteMultiStatus(w, responses, ""); err != nil {
		h.log.Warn("propfind response cut short", "method", r.Method, "collection", t.calendar, "error", err)
	}
}

// fail answers err and logs it when it is a server fault.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, t target, err error) {
	if dav.StoreError(err).Status >= http.StatusInternalServerError {
		h.log.Error("store failure", "method", r.Method, "collection", t.calendar, "error", err)
	}
	dav.WriteErr(w, err)
}

func (h *Handler) calendarRef(owner, calendar string) store.CollectionRef {
	return store.CollectionRef{Owner: owner, Kind: store.KindCalendar, Name: calendar}
}

func (h *Handler) homeResponses(r *http.Request, t target, owner string, req dav.PropfindRequest, depth dav.Depth) (iter.Seq[dav.Response], error) {
	var calendars []store.Collection
	if depth == dav.Depth1 {
		var err error
		calendars, err = h.store.ListCollections(r.Context(), owner, store.KindCalendar)
		if err != nil {
			return nil, err
		}
	}
	return func(yield func(dav.Response) bool) {
		if !yield(dav.PropFind(req, h.homeResource(t.user))) {
			return
		}
		for _, c := range calendars {
			if r.Context().Err() != nil || !yield(dav.PropFind(req, h.calendarResource(t.user, c))) {
				return
			}
		}
	}, nil
}

func (h *Handler) calendarResponses(r *http.Request, t target, owner string, req dav.PropfindRequest, depth dav.Depth) (iter.Seq[dav.Response], error) {
	ref := h.calendarRef(owner, t.calendar)
	c, err := h.store.GetCollection(r.Context(), ref)
	if err != nil {
		return nil, err
	}
	var objs []store.Object
	if depth == dav.Depth1 {
		// No data: listing a large calendar must not read any object.
		objs, err = h.store.ListObjects(r.Context(), ref, store.ListOptions{})
		if err != nil {
			return nil, err
		}
	}
	return func(yield func(dav.Response) bool) {
		if !yield(dav.PropFind(req, h.calendarResource(t.user, c))) {
			return
		}
		for _, o := range objs {
			if r.Context().Err() != nil || !yield(h.objectResponse(r.Context(), owner, t.user, t.calendar, o, req)) {
				return
			}
		}
	}, nil
}

func (h *Handler) objectResponses(r *http.Request, t target, owner string, req dav.PropfindRequest) (iter.Seq[dav.Response], error) {
	// One object is read whole; its bytes serve calendar-data too.
	o, err := h.store.GetObject(r.Context(), h.calendarRef(owner, t.calendar), t.name)
	if err != nil {
		return nil, err
	}
	data := o.Data
	res := h.objectResource(t.user, t.calendar, o, func() ([]byte, error) { return data, nil })
	return func(yield func(dav.Response) bool) { yield(dav.PropFind(req, res)) }, nil
}
