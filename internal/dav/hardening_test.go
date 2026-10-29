// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/store"
)

func okResponse(href string) Response {
	return Response{Href: href, Propstats: []Propstat{{Status: 200, Props: []Prop{TextProp(DisplayName, "n")}}}}
}

func writeSeq(rs ...Response) (string, error) {
	rec := httptest.NewRecorder()
	err := WriteMultiStatus(rec, seq(rs...), "")
	return rec.Body.String(), err
}

func wellFormed(t *testing.T, body string) {
	t.Helper()
	if _, err := ParseXML([]byte(body)); err != nil {
		t.Errorf("body is not well-formed: %v\n%s", err, body)
	}
}

func TestPropWriteExtraEnd(t *testing.T) {
	bad := Response{Href: "/bad", Propstats: []Propstat{{Status: 200, Props: []Prop{{
		Name: DisplayName, Write: func(w *Writer) { w.Text("x"); w.End(); w.End() },
	}}}}}
	body, err := writeSeq(okResponse("/a"), bad, okResponse("/c"))
	if err == nil {
		t.Fatal("extra End must be reported")
	}
	wellFormed(t, body)
	if !strings.Contains(body, ">/a<") || strings.Contains(body, ">/c<") {
		t.Errorf("a sticky error must stop the body after the bad response: %s", body)
	}
}

func TestPropWriteMissingEnd(t *testing.T) {
	bad := Response{Href: "/bad", Propstats: []Propstat{{Status: 200, Props: []Prop{{
		Name: DisplayName, Write: func(w *Writer) { w.Start(Href); w.Start(Href) },
	}}}}}
	body, err := writeSeq(okResponse("/a"), bad)
	if err == nil {
		t.Fatal("missing End must be reported")
	}
	wellFormed(t, body)
}

func TestWriterFloorDoesNotCloseOuter(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(Multistatus, rootAttrs()...)
	writeProp(w, Prop{Name: DisplayName, Write: func(w *Writer) { w.End() }})
	if w.Err() == nil {
		t.Fatal("End below the floor must be an error")
	}
	if len(w.stack) != 1 {
		t.Errorf("the outer element must stay open, stack = %v", w.stack)
	}
}

func TestWriteMultiStatusSkipsInvalidResponses(t *testing.T) {
	rs := []Response{
		okResponse("/a"),
		{Href: "/none"},
		okResponse("/b"),
		{Href: "/both", Status: 404, Propstats: []Propstat{{Status: 200}}},
		{Href: "/nostatus", Propstats: []Propstat{{}}},
		{Href: "/badname", Propstats: []Propstat{{Status: 200, Props: []Prop{{Name: xml.Name{Local: "a b"}}}}}},
		okResponse("/c"),
	}
	body, err := writeSeq(rs...)
	if err == nil {
		t.Fatal("invalid responses must be reported")
	}
	for _, h := range []string{"/a", "/b", "/c"} {
		if !strings.Contains(body, ">"+h+"<") {
			t.Errorf("valid response %s missing: %s", h, body)
		}
	}
	for _, h := range []string{"/none", "/both", "/nostatus", "/badname"} {
		if strings.Contains(body, ">"+h+"<") {
			t.Errorf("invalid response %s written: %s", h, body)
		}
	}
	wellFormed(t, body)
}

func TestWriteMultiStatusClipsErrors(t *testing.T) {
	var rs []Response
	for i := 0; i < 25; i++ {
		rs = append(rs, Response{Href: fmt.Sprintf("/x%d", i)})
	}
	rs = append(rs, okResponse("/ok"))
	body, err := writeSeq(rs...)
	if err == nil {
		t.Fatal("want error")
	}
	if got := strings.Count(err.Error(), "response without status"); got != 10 {
		t.Errorf("reported %d errors, want 10", got)
	}
	if !strings.Contains(err.Error(), "15 more") {
		t.Errorf("error does not count the clipped ones: %v", err)
	}
	if !strings.Contains(body, ">/ok<") {
		t.Errorf("valid response after the invalid ones missing")
	}
}

func TestFlushKeepsCompleteOutputAfterMisuse(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(Multistatus, rootAttrs()...)
	w.End()
	w.End() // misuse
	if err := w.Flush(); err == nil {
		t.Fatal("Flush must return the misuse error")
	}
	if !strings.HasSuffix(b.String(), "</D:multistatus>") {
		t.Errorf("buffered output not flushed: %q", b.String())
	}
}

func TestResponseError507(t *testing.T) {
	r := Response{
		Href: "/cal/", Status: 507,
		Error: &Error{Precondition: NumberOfMatchesWithinLimits},
	}
	body, err := writeSeq(r)
	if err != nil {
		t.Fatal(err)
	}
	want := xmlDecl + `<D:multistatus ` + rootNS + `><D:response><D:href>/cal/</D:href>` +
		`<D:status>HTTP/1.1 507 Insufficient Storage</D:status>` +
		`<D:error><D:number-of-matches-within-limits/></D:error></D:response></D:multistatus>`
	if body != want {
		t.Errorf("got  %s\nwant %s", body, want)
	}
}

func TestResponseErrorAfterPropstats(t *testing.T) {
	r := okResponse("/a")
	r.Error = &Error{Precondition: ValidSyncToken}
	body, err := writeSeq(r)
	if err != nil {
		t.Fatal(err)
	}
	i, j := strings.Index(body, "</D:propstat>"), strings.Index(body, "<D:error>")
	if i < 0 || j < i || !strings.HasSuffix(body, "</D:error></D:response></D:multistatus>") {
		t.Errorf("error must follow the propstats: %s", body)
	}
}

func TestCheckPreconditions(t *testing.T) {
	c := ParseCond
	tests := []struct {
		name        string
		ifMatch     string
		ifNoneMatch string
		current     string
		exists      bool
		fail        bool
	}{
		{"no headers, absent", "", "", "", false, false},
		{"no headers, exists", "", "", "a", true, false},
		{"if-match star exists", "*", "", "a", true, false},
		{"if-match star absent", "*", "", "", false, true},
		{"if-match strong hit", `"a"`, "", "a", true, false},
		{"if-match list hit", `"x", "a"`, "", "a", true, false},
		{"if-match miss", `"b"`, "", "a", true, true},
		{"if-match weak never matches", `W/"a"`, "", "a", true, true},
		{"if-match absent", `"a"`, "", "", false, true},
		{"if-match malformed", `"unterminated`, "", "a", true, true},
		{"if-match malformed absent", `,`, "", "", false, true},
		{"if-none-match star exists", "", "*", "a", true, true},
		{"if-none-match star absent", "", "*", "", false, false},
		{"if-none-match hit", "", `"a"`, "a", true, true},
		{"if-none-match weak hit", "", `W/"a"`, "a", true, true},
		{"if-none-match miss", "", `"b"`, "a", true, false},
		{"if-none-match absent", "", `"a"`, "", false, false},
		{"if-none-match malformed", "", `"x`, "a", true, true},
		{"if-none-match malformed absent", "", `"x`, "", false, true},
		{"both pass", `"a"`, `"b"`, "a", true, false},
		{"match passes, none-match fails", `"a"`, `"a"`, "a", true, true},
		{"match fails, none-match passes", `"b"`, `"c"`, "a", true, true},
	}
	for _, tt := range tests {
		e := CheckPreconditions(c(tt.ifMatch), c(tt.ifNoneMatch), tt.current, tt.exists)
		if (e != nil) != tt.fail {
			t.Errorf("%s: got %v, want fail=%v", tt.name, e, tt.fail)
		}
		if e != nil && (e.Status != 412 || e.Message != "precondition failed") {
			t.Errorf("%s: wrong error %+v", tt.name, e)
		}
	}
}

func TestTooLarge(t *testing.T) {
	e := TooLarge(CalMaxResourceSize)
	if e.Status != 413 || e.Precondition != CalMaxResourceSize {
		t.Errorf("%+v", e)
	}
	rec := httptest.NewRecorder()
	WriteError(rec, e)
	if !strings.Contains(rec.Body.String(), "<C:max-resource-size/>") {
		t.Error(rec.Body.String())
	}
}

func TestWriteErr(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{NotFound("x"), 404},
		{fmt.Errorf("wrap: %w", Conflict(CalNoUIDConflict)), 409},
		{store.ErrNotFound, 404},
		{errors.New("boom"), 500},
		{nil, 500},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		WriteErr(rec, tt.err)
		if rec.Code != tt.want {
			t.Errorf("%v: code %d, want %d", tt.err, rec.Code, tt.want)
		}
	}
	rec := httptest.NewRecorder()
	WriteErr(rec, errors.New("secret detail"))
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("leaked detail")
	}
	var typedNil *Error
	rec = httptest.NewRecorder()
	WriteErr(rec, typedNil)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("typed nil: %d", rec.Code)
	}
}

func TestNewNames(t *testing.T) {
	for n, want := range map[xml.Name]xml.Name{
		CalValidFilter:          N(NSCalDAV, "valid-filter"),
		CRValidFilter:           N(NSCardDAV, "valid-filter"),
		CalSupportedFilter:      N(NSCalDAV, "supported-filter"),
		CRSupportedFilter:       N(NSCardDAV, "supported-filter"),
		ValidResourceType:       N(NSDAV, "valid-resourcetype"),
		CRAddressDataType:       N(NSCardDAV, "address-data-type"),
		CalExpand:               N(NSCalDAV, "expand"),
		CalLimitRecurrenceSet:   N(NSCalDAV, "limit-recurrence-set"),
		CalTimezone:             N(NSCalDAV, "timezone"),
		CRSupportedCollationSet: N(NSCardDAV, "supported-collation-set"),
	} {
		if n != want {
			t.Errorf("%v != %v", n, want)
		}
	}
}

func TestParseXMLReservedNamespaces(t *testing.T) {
	bad := []string{
		`<a xmlns:p="http://www.w3.org/2000/xmlns/"/>`,
		`<a xmlns="http://www.w3.org/2000/xmlns/"/>`,
		`<a xmlns:p="http://www.w3.org/XML/1998/namespace"/>`,
		`<a xmlns="http://www.w3.org/XML/1998/namespace"/>`,
		`<a xmlns:xml="urn:other"/>`,
		`<a xmlns:xmlns="urn:other"/>`,
		`<a xmlns:p="urn:a b"/>`,
		`<a xmlns:p="urn:a&#9;b"/>`,
		`<a xmlns="urn:a&#10;b"/>`,
	}
	for _, in := range bad {
		if _, err := ParseXML([]byte(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
	good := []string{
		`<a xmlns:xml="http://www.w3.org/XML/1998/namespace" xml:lang="en"/>`,
		`<a xmlns:p="urn:ok"/>`,
	}
	for _, in := range good {
		if _, err := ParseXML([]byte(in)); err != nil {
			t.Errorf("rejected %s: %v", in, err)
		}
	}
}

func TestWriterRefusesBadNamespaceURI(t *testing.T) {
	for _, space := range []string{"urn:a b", "urn:a\tb", "http://www.w3.org/2000/xmlns/", "urn:a\x01b"} {
		var b bytes.Buffer
		w := NewWriter(&b)
		w.Start(xml.Name{Space: space, Local: "x"})
		if w.Err() == nil {
			t.Errorf("accepted namespace %q", space)
		}
		_ = w.Flush()
		if b.Len() != 0 {
			t.Errorf("wrote %q", b.String())
		}
	}
}

func TestParseCondSpaceAfterWeakPrefix(t *testing.T) {
	c := ParseCond(`W/ "x"`)
	if c.IsSet() || !c.Malformed() || c.Matches("x") {
		t.Errorf(`W/ "x" must be malformed: %+v`, c)
	}
	c = ParseCond(`W/`)
	if c.IsSet() {
		t.Errorf("bare W/ must not be set: %+v", c)
	}
}

func TestParseCondManyBareTokens(t *testing.T) {
	c := ParseCond(strings.Repeat("a ", 100000))
	if c.IsSet() {
		t.Errorf("several bare tokens are not a usable tag: %+v", c)
	}
}
