// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	xmlDecl = `<?xml version="1.0" encoding="utf-8"?>` + "\n"
	rootNS  = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"` +
		` xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/"` +
		` xmlns:A="http://apple.com/ns/ical/"`
)

func seq(rs ...Response) func(yield func(Response) bool) {
	return func(yield func(Response) bool) {
		for _, r := range rs {
			if !yield(r) {
				return
			}
		}
	}
}

func multistatus(t *testing.T, syncToken string, rs ...Response) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := WriteMultiStatus(rec, seq(rs...), syncToken); err != nil {
		t.Fatalf("WriteMultiStatus: %v", err)
	}
	return rec
}

func TestMultiStatusGolden(t *testing.T) {
	rec := multistatus(t, "",
		Response{
			Href: "/dav/cal/a.ics",
			Propstats: []Propstat{
				{Status: 200, Props: []Prop{
					TextProp(GetETag, `"abc"`),
					HrefProp(Owner, "/dav/principals/u/"),
					EmptyProp(GetContentType),
				}},
				{Status: 404, Props: []Prop{EmptyProp(CalendarColor)}},
			},
		})
	want := xmlDecl + `<D:multistatus ` + rootNS + `>` +
		`<D:response><D:href>/dav/cal/a.ics</D:href>` +
		`<D:propstat><D:prop><D:getetag>&#34;abc&#34;</D:getetag>` +
		`<D:owner><D:href>/dav/principals/u/</D:href></D:owner><D:getcontenttype/></D:prop>` +
		`<D:status>HTTP/1.1 200 OK</D:status></D:propstat>` +
		`<D:propstat><D:prop><A:calendar-color/></D:prop>` +
		`<D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>` +
		`</D:response></D:multistatus>`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body:\n got %s\nwant %s", got, want)
	}
	if rec.Code != 207 {
		t.Errorf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
}

func TestMultiStatusStatusOnlyAndSyncToken(t *testing.T) {
	rec := multistatus(t, "http://example.test/sync/7",
		Response{Href: "/dav/cal/gone.ics", Status: 404})
	want := xmlDecl + `<D:multistatus ` + rootNS + `>` +
		`<D:response><D:href>/dav/cal/gone.ics</D:href>` +
		`<D:status>HTTP/1.1 404 Not Found</D:status></D:response>` +
		`<D:sync-token>http://example.test/sync/7</D:sync-token></D:multistatus>`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body:\n got %s\nwant %s", got, want)
	}
}

func TestMultiStatusEmpty(t *testing.T) {
	rec := multistatus(t, "")
	want := xmlDecl + `<D:multistatus ` + rootNS + `></D:multistatus>`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body:\n got %s\nwant %s", got, want)
	}
	rec = httptest.NewRecorder()
	if err := WriteMultiStatus(rec, nil, ""); err != nil || rec.Code != 207 {
		t.Fatalf("nil sequence: err=%v code=%d", err, rec.Code)
	}
}

func TestMultiStatusUnknownStatusCode(t *testing.T) {
	rec := multistatus(t, "", Response{Href: "/x", Status: 799})
	if !strings.Contains(rec.Body.String(), `<D:status>HTTP/1.1 799 </D:status>`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestMultiStatusPropstatError(t *testing.T) {
	rec := multistatus(t, "", Response{Href: "/x", Propstats: []Propstat{{
		Status: 403, Props: []Prop{EmptyProp(GetETag)},
		Error: Forbidden(CannotModifyProtectedProperty),
	}}})
	want := `<D:status>HTTP/1.1 403 Forbidden</D:status>` +
		`<D:error><D:cannot-modify-protected-property/></D:error></D:propstat>`
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestMultiStatusEmptyPropstat(t *testing.T) {
	rec := multistatus(t, "", Response{Href: "/x", Propstats: []Propstat{{Status: 200}}})
	if !strings.Contains(rec.Body.String(),
		`<D:propstat><D:prop></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestMultiStatusHostileText(t *testing.T) {
	hostile := "]]><&\"'\x01\x00\xff\u0085x￾"
	rec := multistatus(t, "", Response{
		Href: "/a&b<c>\"\x02\xfe",
		Propstats: []Propstat{{Status: 200, Props: []Prop{
			TextProp(DisplayName, hostile),
			HrefProp(Owner, "/o\x03&"),
		}}},
	})
	body := rec.Body.String()
	for _, want := range []string{
		`<D:href>/a&amp;b&lt;c&gt;&#34;` + "�" + `</D:href>`,
		`<D:displayname>]]&gt;&lt;&amp;&#34;&#39;` + "�\u0085x" + `</D:displayname>`,
		`<D:owner><D:href>/o&amp;</D:href></D:owner>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %q", want, body)
		}
	}
	root, err := ParseXML([]byte(body))
	if err != nil {
		t.Fatalf("output not well-formed: %v", err)
	}
	got := root.Child(ResponseEl).Child(Href).Text
	if got != "/a&b<c>\"�" {
		t.Errorf("href round trip = %q", got)
	}
	dn := root.Child(ResponseEl).Child(PropstatEl).Child(PropEl).Child(DisplayName).Text
	if dn != "]]><&\"'�\u0085x" {
		t.Errorf("displayname round trip = %q", dn)
	}
}

func TestWhitespaceEscapedAsCharacterReferences(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(DisplayName, Attr{"xmlns:D", NSDAV}, Attr{"x", "a\tb\nc\rd"})
	w.Text("a\tb\nc\rd")
	w.End()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `<D:displayname xmlns:D="DAV:" x="a&#x9;b&#xA;c&#xD;d">a&#x9;b&#xA;c&#xD;d</D:displayname>`
	if b.String() != want {
		t.Fatalf("got %q want %q", b.String(), want)
	}
	root, err := ParseXML(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if root.Text != "a\tb\nc\rd" || root.Attr("x") != "a\tb\nc\rd" {
		t.Fatalf("round trip text=%q attr=%q", root.Text, root.Attr("x"))
	}
}

func TestHrefPropTwoHrefs(t *testing.T) {
	rec := multistatus(t, "", Response{Href: "/x", Propstats: []Propstat{{Status: 200, Props: []Prop{
		HrefProp(CalendarHomeSet, "/a/", "/b/"),
	}}}})
	want := `<C:calendar-home-set><D:href>/a/</D:href><D:href>/b/</D:href></C:calendar-home-set>`
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestTypesProp(t *testing.T) {
	rec := multistatus(t, "", Response{Href: "/x", Propstats: []Propstat{{Status: 200, Props: []Prop{
		TypesProp(Collection, Calendar),
		TypesProp(),
	}}}})
	want := `<D:resourcetype><D:collection/><C:calendar/></D:resourcetype><D:resourcetype/>`
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestMultiStatusRoundTrip(t *testing.T) {
	rec := multistatus(t, "tok",
		Response{Href: "/1", Propstats: []Propstat{
			{Status: 200, Props: []Prop{
				TextProp(GetETag, "e1"),
				TypesProp(Collection, Addressbook),
				{Name: CalendarData, Write: func(w *Writer) { w.Text("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n") }},
			}},
			{Status: 404, Props: []Prop{EmptyProp(GetCTag)}},
		}},
		Response{Href: "/2", Status: 404},
	)
	root, err := ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	if root.Name != Multistatus {
		t.Fatalf("root = %v", root.Name)
	}
	rs := root.Kids(ResponseEl)
	if len(rs) != 2 {
		t.Fatalf("responses = %d", len(rs))
	}
	ps := rs[0].Kids(PropstatEl)
	if len(ps) != 2 || ps[0].Child(Status).Text != "HTTP/1.1 200 OK" {
		t.Fatalf("propstats = %+v", ps)
	}
	prop := ps[0].Child(PropEl)
	if prop.Child(GetETag).Text != "e1" {
		t.Errorf("etag = %q", prop.Child(GetETag).Text)
	}
	if rt := prop.Child(ResourceType); rt.Child(Collection) == nil || rt.Child(Addressbook) == nil {
		t.Errorf("resourcetype = %+v", rt)
	}
	if got := prop.Child(CalendarData).Text; got != "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n" {
		t.Errorf("calendar-data = %q", got)
	}
	if ps[1].Child(PropEl).Child(GetCTag) == nil {
		t.Error("getctag missing in 404 propstat")
	}
	if rs[1].Child(Status).Text != "HTTP/1.1 404 Not Found" || len(rs[1].Kids(PropstatEl)) != 0 {
		t.Errorf("status-only response = %+v", rs[1])
	}
	if root.Child(SyncToken).Text != "tok" || root.Children[len(root.Children)-1].Name != SyncToken {
		t.Error("sync-token must be last")
	}
}

// failWriter is a ResponseWriter whose body writes fail, as after a client
// disconnect.
type failWriter struct {
	h    http.Header
	code int
}

func (f *failWriter) Header() http.Header       { return f.h }
func (f *failWriter) WriteHeader(c int)         { f.code = c }
func (f *failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestMultiStatusStopsOnWriteError(t *testing.T) {
	var yielded int
	big := strings.Repeat("x", 1000)
	rs := func(yield func(Response) bool) {
		for range 10000 {
			yielded++
			if !yield(Response{Href: "/" + big, Status: 404}) {
				return
			}
		}
	}
	f := &failWriter{h: http.Header{}}
	err := WriteMultiStatus(f, rs, "")
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("err = %v", err)
	}
	if yielded >= 100 {
		t.Errorf("iteration continued after write error: %d responses", yielded)
	}
	if f.code != 207 {
		t.Errorf("code = %d", f.code)
	}
}

func TestWriterUnknownNamespace(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	other := N("urn:example:x", "thing")
	w.Start(Multistatus, Attr{"xmlns:D", NSDAV})
	w.Start(other, Attr{"a", "1"})
	w.Empty(N("urn:example:y", "leaf"))
	w.Text("t")
	w.End()
	w.Empty(other)
	w.End()
	if err := w.Flush(); err != nil || w.Err() != nil {
		t.Fatalf("err = %v / %v", err, w.Err())
	}
	want := `<D:multistatus xmlns:D="DAV:"><ns1:thing xmlns:ns1="urn:example:x" a="1">` +
		`<ns2:leaf xmlns:ns2="urn:example:y"/>t</ns1:thing><ns3:thing xmlns:ns3="urn:example:x"/>` +
		`</D:multistatus>`
	if b.String() != want {
		t.Fatalf("got  %s\nwant %s", b.String(), want)
	}
	root, err := ParseXML(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if root.Children[0].Name != other || root.Children[0].Children[0].Name != N("urn:example:y", "leaf") ||
		root.Children[1].Name != other {
		t.Fatalf("tree = %+v", root)
	}
}

func TestWriterEndOnEmptyStack(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.End()
	if w.Err() == nil {
		t.Fatal("End on empty stack must record an error")
	}
	w.Start(GetETag)
	_ = w.Flush()
	if b.Len() != 0 {
		t.Errorf("writes after an error must be dropped, got %q", b.String())
	}
}

func TestWriterRaw(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(GetETag)
	w.Raw("<!-- ok -->")
	w.End()
	_ = w.Flush()
	if b.String() != `<D:getetag><!-- ok --></D:getetag>` {
		t.Fatalf("got %q", b.String())
	}
}

func TestWriterBadNameNoPanic(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(N("DAV:", "a b<"))
	w.Start(N("DAV:", ""))
	w.Empty(N("DAV:", "x>"))
	w.Text("x")
	w.End()
	_ = w.Flush()
	if w.Err() == nil {
		t.Fatal("invalid names must record an error")
	}
	if strings.Contains(b.String(), "<") {
		t.Errorf("invalid name leaked into output: %q", b.String())
	}
}

func TestWriteErrorPlain(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, NotFound("no such calendar"))
	if rec.Code != 404 || rec.Body.String() != "no such calendar" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
	rec = httptest.NewRecorder()
	WriteError(rec, BadRequest(""))
	if rec.Code != 400 || rec.Body.String() != "Bad Request" {
		t.Fatalf("default message: code=%d body=%q", rec.Code, rec.Body)
	}
}

func TestWriteErrorNilAndZeroStatus(t *testing.T) {
	for _, e := range []*Error{nil, {}, {Message: "boom"}} {
		rec := httptest.NewRecorder()
		WriteError(rec, e)
		if rec.Code != 500 {
			t.Errorf("%+v: code = %d", e, rec.Code)
		}
	}
}

func TestWriteErrorValidSyncToken(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, Forbidden(ValidSyncToken))
	want := xmlDecl + `<D:error ` + rootNS + `><D:valid-sync-token/></D:error>`
	if rec.Code != 403 || rec.Body.String() != want {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml; charset=utf-8" {
		t.Errorf("content type = %q", ct)
	}
}

func TestWriteErrorNoUIDConflictWithHref(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, Conflict(CalNoUIDConflict, "/dav/cal/a&b.ics"))
	want := xmlDecl + `<D:error ` + rootNS + `><C:no-uid-conflict>` +
		`<D:href>/dav/cal/a&amp;b.ics</D:href></C:no-uid-conflict></D:error>`
	if rec.Code != 409 || rec.Body.String() != want {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	root, err := ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if root.Child(CalNoUIDConflict).Child(Href).Text != "/dav/cal/a&b.ics" {
		t.Errorf("tree = %+v", root)
	}
}

func TestErrorMessageIsSafe(t *testing.T) {
	e := Conflict(CRNoUIDConflict, "/secret/path")
	msg := e.Error()
	if strings.Contains(msg, "/secret") || !strings.Contains(msg, "no-uid-conflict") ||
		!strings.Contains(msg, "Conflict") {
		t.Errorf("msg = %q", msg)
	}
	if got := NotFound("gone").Error(); got != "Not Found: gone" {
		t.Errorf("msg = %q", got)
	}
	var nilErr *Error
	if nilErr.Error() == "" {
		t.Error("nil Error must still describe itself")
	}
	var target *Error
	if !errors.As(error(PreconditionFailed("etag")), &target) || target.Status != 412 {
		t.Error("Error must work with errors.As")
	}
}

func TestPreconditionNames(t *testing.T) {
	cases := map[xml.Name]string{
		ValidSyncToken:                NSDAV + " valid-sync-token",
		NumberOfMatchesWithinLimits:   NSDAV + " number-of-matches-within-limits",
		CannotModifyProtectedProperty: NSDAV + " cannot-modify-protected-property",
		CalNoUIDConflict:              NSCalDAV + " no-uid-conflict",
		CRNoUIDConflict:               NSCardDAV + " no-uid-conflict",
		SupportedCalendarComponent:    NSCalDAV + " supported-calendar-component",
		ValidCalendarData:             NSCalDAV + " valid-calendar-data",
		ValidCalendarObjectResource:   NSCalDAV + " valid-calendar-object-resource",
		ValidAddressData:              NSCardDAV + " valid-address-data",
		CalSupportedCollation:         NSCalDAV + " supported-collation",
		CRSupportedCollation:          NSCardDAV + " supported-collation",
	}
	for n, want := range cases {
		if got := n.Space + " " + n.Local; got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func FuzzWriterText(f *testing.F) {
	for _, s := range []string{"", "plain", "]]><&\"'", "\x00\x01\x1f", "\xff\xfe", " ￾￿",
		"a\r\nb\tc", "\U0010ffff", "\xed\xa0\x80"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		rec := httptest.NewRecorder()
		resp := Response{Href: s, Propstats: []Propstat{{Status: 200, Props: []Prop{
			TextProp(DisplayName, s),
			HrefProp(Owner, s),
			{Name: CalendarData, Write: func(w *Writer) { w.Text(s) }},
		}}}}
		if err := WriteMultiStatus(rec, seq(resp), s); err != nil {
			t.Fatalf("write: %v", err)
		}
		root, err := ParseXML(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("not well-formed for %q: %v\n%s", s, err, rec.Body)
		}
		if got, want := root.Child(ResponseEl).Child(Href).Text, cleanText(s); got != want {
			t.Fatalf("href %q != %q", got, want)
		}
	})
}

func TestPropfindNamesRoundTrip(t *testing.T) {
	req := `<D:propfind xmlns:D="DAV:" xmlns:x="urn:x"><D:prop>` +
		`<x:été/><x:a·b/><x:a-b/><x:a.b/><x:a1/><x:_u/><x:名前/><x:b́/>` +
		`</D:prop></D:propfind>`
	root, err := ParseXML([]byte(strings.ReplaceAll(req, `́`, "́")))
	if err != nil {
		t.Fatal(err)
	}
	var props []Prop
	var names []xml.Name
	for _, c := range root.Child(PropEl).Children {
		props = append(props, EmptyProp(c.Name))
		names = append(names, c.Name)
	}
	if len(names) != 8 {
		t.Fatalf("parsed %d props", len(names))
	}
	rec := multistatus(t, "", Response{Href: "/x", Propstats: []Propstat{{Status: 404, Props: props}}})
	out, err := ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("output: %v\n%s", err, rec.Body)
	}
	got := out.Child(ResponseEl).Child(PropstatEl).Child(PropEl).Children
	if len(got) != len(names) {
		t.Fatalf("got %d props", len(got))
	}
	for i, n := range names {
		if got[i].Name != n {
			t.Errorf("prop %d = %v, want %v", i, got[i].Name, n)
		}
	}
	// The namespace is declared once, on the response element.
	if c := strings.Count(rec.Body.String(), `xmlns:ns`); c != 1 {
		t.Errorf("declarations = %d\n%s", c, rec.Body)
	}
}

func TestInvalidNamesStillRejected(t *testing.T) {
	for _, n := range []string{"1a", "-a", ".a", "a:b", "a b", "a\xff", "×", "áb>"} {
		var b bytes.Buffer
		w := NewWriter(&b)
		w.Empty(N("urn:x", n))
		if n == "áb>" || w.Err() == nil {
			if w.Err() == nil {
				t.Errorf("%q accepted", n)
			}
		}
	}
}

func TestWriterXMLNamespace(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	w.Start(Multistatus, Attr{"xmlns:D", NSDAV}, Attr{"xml:lang", "en"})
	w.Empty(N(xmlNamespace, "foo"))
	w.End()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `<D:multistatus xmlns:D="DAV:" xml:lang="en"><xml:foo/></D:multistatus>`
	if b.String() != want {
		t.Fatalf("got %s", b.String())
	}
	root, err := ParseXML(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if root.Children[0].Name != N(xmlNamespace, "foo") || root.Attrs[N(xmlNamespace, "lang")] != "en" {
		t.Fatalf("tree = %+v", root)
	}
}

func TestMalformedResponses(t *testing.T) {
	cases := map[string]Response{
		"no status or propstats": {Href: "/x"},
		"both":                   {Href: "/x", Status: 404, Propstats: []Propstat{{Status: 200}}},
		"propstat without code":  {Href: "/x", Propstats: []Propstat{{Props: []Prop{EmptyProp(GetETag)}}}},
	}
	for name, r := range cases {
		rec := httptest.NewRecorder()
		if err := WriteMultiStatus(rec, seq(r), ""); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestNamespacePrefixReuse(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	x := N("urn:x", "a")
	w.Start(Multistatus, Attr{"xmlns:D", NSDAV})
	w.Start(x)
	w.Empty(x)
	w.Start(x)
	w.End()
	w.End()
	w.End()
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	want := `<D:multistatus xmlns:D="DAV:"><ns1:a xmlns:ns1="urn:x"><ns1:a/><ns1:a></ns1:a></ns1:a>` +
		`</D:multistatus>`
	if b.String() != want {
		t.Fatalf("got %s", b.String())
	}
	if _, err := ParseXML(b.Bytes()); err != nil {
		t.Fatal(err)
	}
	// Strict stdlib decoder agrees.
	d := xml.NewDecoder(&b)
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			break
		}
	}
}
