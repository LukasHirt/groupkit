// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/LukasHirt/groupkit/ical"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// getObject answers GET and HEAD with the stored bytes, unchanged.
func (h *Handler) getObject(w http.ResponseWriter, r *http.Request, t target, owner string) {
	o, err := h.store.GetObject(r.Context(), h.calendarRef(owner, t.calendar), t.name)
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("ETag", dav.QuoteETag(o.ETag))
	hd.Set("Last-Modified", dav.HTTPDate(o.ModTime))
	// A malformed If-None-Match is ignored here, as RFC 9110 allows for GET.
	if inm := dav.ParseCond(r.Header.Get("If-None-Match")); inm.IsSet() && inm.MatchesWeak(o.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Type", icalContentType)
	hd.Set("Content-Length", strconv.Itoa(len(o.Data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(o.Data)
	}
}

// icalMediaType reports whether a Content-Type header names an iCalendar
// object. A missing header is accepted; parameters are ignored.
func icalMediaType(header string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(header)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return false
	}
	return mt == "text/calendar" || mt == "application/ics"
}

// currentState returns the ETag of an object and whether it exists. It reads
// the object; if that fails (a damaged object, say) it falls back to the
// listing, which needs no object data, so a client can still replace or remove
// the object.
func (h *Handler) currentState(ctx context.Context, ref store.CollectionRef, name string) (etag string, exists bool, err error) {
	o, err := h.store.GetObject(ctx, ref, name)
	if err == nil {
		return o.ETag, true, nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return "", false, nil
	}
	list, lerr := h.store.ListObjects(ctx, ref, store.ListOptions{})
	if lerr != nil {
		return "", false, err
	}
	for _, o := range list {
		if o.Name == name {
			return o.ETag, true, nil
		}
	}
	return "", false, nil
}

// putObject creates or replaces an object.
func (h *Handler) putObject(w http.ResponseWriter, r *http.Request, t target, owner string) {
	ctx := r.Context()
	ref := h.calendarRef(owner, t.calendar)
	if _, err := h.store.GetCollection(ctx, ref); err != nil {
		h.fail(w, r, t, err)
		return
	}

	if !icalMediaType(r.Header.Get("Content-Type")) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	body, err := dav.ReadBody(w, r, h.maxSize)
	if err != nil {
		var de *dav.Error
		if errors.As(err, &de) && de.Status == http.StatusRequestEntityTooLarge {
			err = dav.TooLarge(dav.CalMaxResourceSize)
		}
		dav.WriteErr(w, err)
		return
	}
	// Only VEVENT objects are accepted, whatever component set the calendar
	// reports.
	info, err := ical.Validate(body, []string{"VEVENT"})
	switch {
	case errors.Is(err, ical.ErrUnsupportedComponent):
		dav.WriteError(w, dav.Forbidden(dav.SupportedCalendarComponent))
		return
	case err != nil:
		// ErrInvalid and ErrUID both break valid-calendar-data.
		dav.WriteError(w, dav.Forbidden(dav.ValidCalendarData))
		return
	}

	// The state read here and the write below are not atomic. A request
	// without conditions can therefore get 201 where 204 is right, or the
	// reverse, when another writer is racing it. The stored data is
	// unaffected, and conditional writes are re-checked by the store.
	curETag, exists, err := h.currentState(ctx, ref, t.name)
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	ifMatch := dav.ParseCond(r.Header.Get("If-Match"))
	ifNoneMatch := dav.ParseCond(r.Header.Get("If-None-Match"))
	if perr := dav.CheckPreconditions(ifMatch, ifNoneMatch, curETag, exists); perr != nil {
		dav.WriteError(w, perr)
		return
	}

	opts := store.PutOptions{UID: info.UID}
	if ifMatch.Present || ifNoneMatch.Present {
		// Let the store repeat the check atomically.
		if exists {
			opts.IfMatch = curETag
		} else {
			opts.IfNoneMatch = true
		}
	}
	o, err := h.store.PutObject(ctx, ref, t.name, body, opts)
	if err != nil {
		var ce *store.ConflictError
		if errors.As(err, &ce) {
			dav.WriteError(w, dav.Conflict(dav.CalNoUIDConflict, h.objectHref(t.user, t.calendar, ce.Name)))
			return
		}
		h.fail(w, r, t, err)
		return
	}
	hd := w.Header()
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("ETag", dav.QuoteETag(o.ETag))
	if exists {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// deleteObject removes an object, honouring If-Match and If-None-Match.
func (h *Handler) deleteObject(w http.ResponseWriter, r *http.Request, t target, owner string) {
	ctx := r.Context()
	ref := h.calendarRef(owner, t.calendar)
	ifMatch := dav.ParseCond(r.Header.Get("If-Match"))
	ifNoneMatch := dav.ParseCond(r.Header.Get("If-None-Match"))
	var opts store.DeleteOptions
	if ifMatch.Present || ifNoneMatch.Present {
		curETag, exists, err := h.currentState(ctx, ref, t.name)
		if err != nil {
			h.fail(w, r, t, err)
			return
		}
		if !exists {
			dav.WriteError(w, dav.NotFound("not found"))
			return
		}
		if perr := dav.CheckPreconditions(ifMatch, ifNoneMatch, curETag, true); perr != nil {
			dav.WriteError(w, perr)
			return
		}
		if ifMatch.Present {
			opts.IfMatch = curETag
		}
	}
	if err := h.store.DeleteObject(ctx, ref, t.name, opts); err != nil {
		h.fail(w, r, t, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
