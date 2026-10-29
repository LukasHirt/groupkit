// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"encoding/xml"
	"io"
	"net/http"
)

// Precondition and postcondition elements that go inside a DAV:error body.
// SupportedCalendarData, SupportedAddressData, CalMaxResourceSize,
// CRMaxResourceSize and SupportedReport, which double as property or
// element names, are used as preconditions under the same names.
var (
	// ValidSyncToken is the RFC 6578 precondition for a sync-collection
	// request whose token is unknown or expired.
	ValidSyncToken = N(NSDAV, "valid-sync-token")
	// NumberOfMatchesWithinLimits is the RFC 6578 postcondition for a report
	// that matched more results than the server returns.
	NumberOfMatchesWithinLimits = N(NSDAV, "number-of-matches-within-limits")
	// CannotModifyProtectedProperty is the RFC 4918 (section 16) precondition for an
	// attempt to set a property the server controls.
	CannotModifyProtectedProperty = N(NSDAV, "cannot-modify-protected-property")

	// CalNoUIDConflict is the RFC 4791 precondition for a calendar object
	// whose UID is already used by another resource in the collection.
	CalNoUIDConflict = N(NSCalDAV, "no-uid-conflict")
	// SupportedCalendarComponent is the RFC 4791 precondition for a
	// calendar object with a component the collection does not allow.
	SupportedCalendarComponent = N(NSCalDAV, "supported-calendar-component")
	// ValidCalendarData is the RFC 4791 precondition for a body that is not
	// parseable iCalendar data.
	ValidCalendarData = N(NSCalDAV, "valid-calendar-data")
	// ValidCalendarObjectResource is the RFC 4791 precondition for
	// iCalendar data that breaks the calendar object resource rules (one
	// UID, one component type, consistent METHOD).
	ValidCalendarObjectResource = N(NSCalDAV, "valid-calendar-object-resource")
	// CalSupportedCollation is the RFC 4791 precondition for a text-match
	// with a collation the server does not support.
	CalSupportedCollation = N(NSCalDAV, "supported-collation")

	// CRNoUIDConflict is the RFC 6352 precondition for an address object
	// whose UID is already used by another resource in the collection.
	CRNoUIDConflict = N(NSCardDAV, "no-uid-conflict")
	// ValidAddressData is the RFC 6352 precondition for a body that is not
	// a parseable vCard of a supported version.
	ValidAddressData = N(NSCardDAV, "valid-address-data")
	// CRSupportedCollation is the RFC 6352 precondition for a text-match
	// with a collation the server does not support.
	CRSupportedCollation = N(NSCardDAV, "supported-collation")
)

// TooLarge returns a 413 naming the precondition that failed, normally
// CalMaxResourceSize or CRMaxResourceSize. ReadBody reports an oversized body
// as a plain-text 413; a PUT engine should answer with this instead.
func TooLarge(pre xml.Name) *Error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Precondition: pre}
}

// Error is an HTTP error a handler can return and WriteError can send. With
// a Precondition the body is a DAV:error document naming it; otherwise it is
// plain text.
type Error struct {
	// Status is the HTTP status code. Zero is treated as 500.
	Status int
	// Precondition is the element to place inside DAV:error. The zero Name
	// means a plain-text response.
	Precondition xml.Name
	// Hrefs, when set, are written as DAV:href children of the precondition
	// element. Pass them already percent-encoded.
	Hrefs []string
	// Message is the plain-text body used when Precondition is zero. It
	// defaults to the status text.
	Message string
}

func (e *Error) status() int {
	if e == nil || e.Status == 0 {
		return http.StatusInternalServerError
	}
	return e.Status
}

// Error returns a message safe for logs and for returning to clients: the
// status text and the precondition name or the Message. It never includes
// the hrefs.
func (e *Error) Error() string {
	if e == nil {
		return "dav: nil error"
	}
	s := http.StatusText(e.status())
	switch {
	case e.Precondition != (xml.Name{}):
		return s + ": " + e.Precondition.Local
	case e.Message != "":
		return s + ": " + e.Message
	}
	return s
}

// Forbidden returns a 403 naming the precondition that failed.
func Forbidden(pre xml.Name, hrefs ...string) *Error {
	return &Error{Status: http.StatusForbidden, Precondition: pre, Hrefs: hrefs}
}

// Conflict returns a 409 naming the precondition that failed.
func Conflict(pre xml.Name, hrefs ...string) *Error {
	return &Error{Status: http.StatusConflict, Precondition: pre, Hrefs: hrefs}
}

// NotFound returns a 404 with a plain-text message.
func NotFound(msg string) *Error { return &Error{Status: http.StatusNotFound, Message: msg} }

// PreconditionFailed returns a 412 with a plain-text message.
func PreconditionFailed(msg string) *Error {
	return &Error{Status: http.StatusPreconditionFailed, Message: msg}
}

// BadRequest returns a 400 with a plain-text message.
func BadRequest(msg string) *Error { return &Error{Status: http.StatusBadRequest, Message: msg} }

// WriteError sends e. A nil e or one without a status is a 500.
func WriteError(w http.ResponseWriter, e *Error) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	if e == nil || e.Precondition == (xml.Name{}) {
		msg := http.StatusText(e.status())
		if e != nil && e.Message != "" {
			msg = e.Message
		}
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(e.status())
		_, _ = io.WriteString(w, msg)
		return
	}
	h.Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(e.status())
	xw := NewWriter(w)
	xw.Raw(xmlDeclaration)
	xw.Start(ErrorEl, rootAttrs()...)
	writePrecondition(xw, e)
	xw.End()
	_ = xw.Flush()
}

// writePrecondition writes the precondition element of e with its hrefs.
func writePrecondition(w *Writer, e *Error) {
	if len(e.Hrefs) == 0 {
		w.Empty(e.Precondition)
		return
	}
	w.Start(e.Precondition)
	for _, h := range e.Hrefs {
		w.Start(Href)
		w.Text(h)
		w.End()
	}
	w.End()
}
