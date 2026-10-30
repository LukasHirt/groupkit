// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strconv"
)

const xmlDeclaration = `<?xml version="1.0" encoding="utf-8"?>` + "\n"

// Prop is one property in a propstat. Write, when set, writes the content of
// the element; the element itself is written around it. A nil Write gives
// an empty element.
//
// Write runs while the 207 is being streamed, after the status line is
// committed, so it can only fail the whole body. Do I/O and anything that can
// fail in PropDef.Get, where an error becomes a 500 propstat, and keep Write
// pure: it should only write already-computed values. It must balance its
// own Start and End calls, and it must not panic.
type Prop struct {
	Name  xml.Name
	Write func(w *Writer)
}

// TextProp is a property whose value is text.
func TextProp(name xml.Name, text string) Prop {
	return Prop{Name: name, Write: func(w *Writer) { w.Text(text) }}
}

// HrefProp is a property whose value is a list of DAV:href elements, one per
// href. Pass hrefs already percent-encoded; they are only escaped for XML.
func HrefProp(name xml.Name, hrefs ...string) Prop {
	return Prop{Name: name, Write: func(w *Writer) {
		for _, h := range hrefs {
			w.Start(Href)
			w.Text(h)
			w.End()
		}
	}}
}

// EmptyProp is a property with no value, as in a 404 propstat.
func EmptyProp(name xml.Name) Prop { return Prop{Name: name} }

// TypesProp is the DAV:resourcetype property listing an empty element for
// each type.
func TypesProp(types ...xml.Name) Prop {
	if len(types) == 0 {
		return EmptyProp(ResourceType)
	}
	return Prop{Name: ResourceType, Write: func(w *Writer) {
		for _, t := range types {
			w.Empty(t)
		}
	}}
}

// Propstat groups properties that share a status. Error, when set, adds a
// DAV:error element for the failed precondition.
type Propstat struct {
	Status int
	Props  []Prop
	Error  *Error
}

// Response is one DAV:response. Href must already be percent-encoded; it is
// only escaped for XML. A non-zero Status with no Propstats is a status-only
// response, as sync-collection uses for removed members. A Response must
// have one or the other, and every Propstat needs a Status; any other shape
// is a programming error that WriteMultiStatus reports.
type Response struct {
	Href      string
	Status    int
	Propstats []Propstat
	// Error, when set with a Precondition, is written as DAV:error after the
	// status or propstats, for example a 507 status-only response carrying
	// DAV:number-of-matches-within-limits. It is allowed with either shape.
	Error *Error
}

// statusLine builds the text of a DAV:status element.
func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + http.StatusText(code)
}

// writeProp writes one property. Prop.Write must balance its own Start and
// End calls; the Writer enforces it. An End that would close the property
// itself or anything outside it records an error and is dropped, and elements
// left open are closed after the error is recorded, so one faulty Write does
// not break the markup around it.
func writeProp(w *Writer, p Prop) {
	if p.Write == nil {
		w.Empty(p.Name)
		return
	}
	w.Start(p.Name)
	if w.err != nil {
		return
	}
	depth := len(w.stack)
	outer := w.floor
	w.floor = depth
	p.Write(w)
	w.floor = outer
	if len(w.stack) != depth {
		w.fail("property content left elements open")
	}
	w.unwind(depth - 1)
}

func writeStatus(w *Writer, code int) {
	w.Start(Status)
	w.Text(statusLine(code))
	w.End()
}

// validate checks the shape of r before any of it is written.
func (r Response) validate() error {
	switch {
	case len(r.Propstats) == 0 && r.Status == 0:
		return errors.New("dav: response without status or propstats")
	case len(r.Propstats) > 0 && r.Status != 0:
		return errors.New("dav: response with both status and propstats")
	}
	for _, ps := range r.Propstats {
		if ps.Status == 0 {
			return errors.New("dav: propstat without status")
		}
		for _, p := range ps.Props {
			if !validXMLName(p.Name.Local, false) {
				return errors.New("dav: property with an invalid name")
			}
			if p.Name.Space != "" && !validNamespace(p.Name.Space) {
				return errors.New("dav: property with an invalid namespace")
			}
		}
	}
	return nil
}

// writeResponse writes r, which must have passed validate.
func writeResponse(w *Writer, r Response) {
	// Declare the custom namespaces the properties use once, on the
	// response element, so each property does not repeat the declaration.
	var spaces []string
	for _, ps := range r.Propstats {
		for _, p := range ps.Props {
			if _, known := prefixes[p.Name.Space]; !known && !slices.Contains(spaces, p.Name.Space) {
				spaces = append(spaces, p.Name.Space)
			}
		}
	}
	w.start(ResponseEl, nil, spaces)
	w.Start(Href)
	w.Text(r.Href)
	w.End()
	if len(r.Propstats) == 0 && r.Status != 0 {
		writeStatus(w, r.Status)
	}
	for _, ps := range r.Propstats {
		w.Start(PropstatEl)
		w.Start(PropEl)
		for _, p := range ps.Props {
			writeProp(w, p)
		}
		w.End()
		writeStatus(w, ps.Status)
		if ps.Error != nil && ps.Error.Precondition != (xml.Name{}) {
			w.Start(ErrorEl)
			writePrecondition(w, ps.Error)
			w.End()
		}
		w.End()
	}
	if r.Error != nil && r.Error.Precondition != (xml.Name{}) {
		w.Start(ErrorEl)
		writePrecondition(w, r.Error)
		w.End()
	}
	w.End()
}

// maxReportedErrors is how many invalid responses WriteMultiStatus names in
// the error it returns.
const maxReportedErrors = 10

// WriteMultiStatus sends a 207 Multi-Status response. It writes each response
// as the sequence yields it, without collecting them, and stops early when
// the connection fails. The status line and headers are sent before the first
// response, so an error returned here means the body was cut short and
// cannot be reported to the client. Write failures only show once the buffer
// is flushed, so the iterator should also watch the request context and stop
// when it ends. A response with an invalid shape is skipped before any of it
// is written and the others still go out; the error returned joins the
// first ten such problems and a count of the rest. A Prop.Write that does not
// balance its elements records a sticky Writer error instead: the body stops
// there, is closed to stay well-formed and the error is returned.
// syncToken, when not empty, is written last as DAV:sync-token.
func WriteMultiStatus(w http.ResponseWriter, responses iter.Seq[Response], syncToken string) error {
	return writeMultiStatus(w, responses, func() string { return syncToken })
}

// writeMultiStatus is WriteMultiStatus with the token chosen after the
// responses have been written, so it can depend on how the stream ended.
func writeMultiStatus(w http.ResponseWriter, responses iter.Seq[Response], syncToken func() string) error {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	xw := NewWriter(w)
	xw.Raw(xmlDeclaration)
	xw.Start(Multistatus, rootAttrs()...)
	var invalid []error
	skipped := 0
	if responses != nil {
		for r := range responses {
			if err := r.validate(); err != nil {
				// One engine bug costs one response, not the body.
				if len(invalid) < maxReportedErrors {
					invalid = append(invalid, err)
				} else {
					skipped++
				}
				continue
			}
			writeResponse(xw, r)
			if xw.Err() != nil {
				break
			}
		}
	}
	if skipped > 0 {
		invalid = append(invalid, fmt.Errorf("dav: %d more invalid responses", skipped))
	}
	if tok := syncToken(); tok != "" {
		xw.Start(SyncToken)
		xw.Text(tok)
		xw.End()
	}
	xw.End()
	xw.unwind(0) // after a misuse error, still end the document
	return errors.Join(append(invalid, xw.Flush())...)
}
