// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"encoding/xml"
	"errors"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var (
	pName  = N(NSDAV, "displayname")
	pType  = ResourceType
	pTag   = N(NSCS, "getctag")
	pColor = N(NSApple, "calendar-color")
	pData  = CalendarData // heavy: not part of allprop
)

func testResource() Resource {
	def := func(n xml.Name, all bool) PropDef {
		return PropDef{AllProp: all, Get: func() (Prop, error) { return TextProp(n, "v"), nil }}
	}
	return Resource{Href: "/cal/work/", Props: map[xml.Name]PropDef{
		pName:  def(pName, true),
		pType:  def(pType, true),
		pTag:   def(pTag, true),
		pColor: def(pColor, true),
		pData:  def(pData, false),
	}}
}

func names(ps []Prop) []xml.Name {
	var out []xml.Name
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func statuses(r Response) []int {
	var out []int
	for _, ps := range r.Propstats {
		out = append(out, ps.Status)
	}
	return out
}

func TestPropFindAllProp(t *testing.T) {
	r := PropFind(PropfindRequest{AllProp: true}, testResource())
	if r.Href != "/cal/work/" || r.Status != 0 || !slices.Equal(statuses(r), []int{200}) {
		t.Fatalf("response = %+v", r)
	}
	// Sorted by namespace, then local name.
	want := []xml.Name{pName, pType, pColor, pTag}
	if got := names(r.Propstats[0].Props); !slices.Equal(got, want) {
		t.Fatalf("allprop = %v, want %v", got, want)
	}
	for _, p := range r.Propstats[0].Props {
		if p.Write == nil {
			t.Errorf("%v has no value", p.Name)
		}
	}
}

func TestPropFindAllPropInclude(t *testing.T) {
	req := PropfindRequest{AllProp: true, Props: []xml.Name{pName, pData, N("urn:x", "none")}}
	r := PropFind(req, testResource())
	if !slices.Equal(statuses(r), []int{200, 404}) {
		t.Fatalf("statuses = %v", statuses(r))
	}
	want := []xml.Name{pName, pType, pColor, pTag, pData}
	if got := names(r.Propstats[0].Props); !slices.Equal(got, want) {
		t.Fatalf("200 = %v, want %v", got, want)
	}
	if got := names(r.Propstats[1].Props); !slices.Equal(got, []xml.Name{N("urn:x", "none")}) {
		t.Fatalf("404 = %v", got)
	}
}

func TestPropFindPropName(t *testing.T) {
	r := PropFind(PropfindRequest{PropName: true}, testResource())
	if !slices.Equal(statuses(r), []int{200}) {
		t.Fatalf("statuses = %v", statuses(r))
	}
	ps := r.Propstats[0].Props
	if len(ps) != 5 {
		t.Fatalf("propname = %v", names(ps))
	}
	for _, p := range ps {
		if p.Write != nil {
			t.Errorf("%v is not empty", p.Name)
		}
	}
	if ps[0].Name != pName || ps[4].Name != pData {
		t.Errorf("order = %v", names(ps))
	}
}

func TestPropFindProps(t *testing.T) {
	missing := N("urn:x", "missing")
	other := N("urn:x", "other")
	req := PropfindRequest{Props: []xml.Name{pTag, missing, pName, other}}
	r := PropFind(req, testResource())
	if !slices.Equal(statuses(r), []int{200, 404}) {
		t.Fatalf("statuses = %v", statuses(r))
	}
	if got := names(r.Propstats[0].Props); !slices.Equal(got, []xml.Name{pTag, pName}) {
		t.Errorf("200 = %v", got)
	}
	nf := r.Propstats[1].Props
	if !slices.Equal(names(nf), []xml.Name{missing, other}) {
		t.Errorf("404 = %v", names(nf))
	}
	for _, p := range nf {
		if p.Write != nil {
			t.Errorf("404 %v has a value", p.Name)
		}
	}
}

func TestPropFindGetError(t *testing.T) {
	res := testResource()
	res.Props[pTag] = PropDef{AllProp: true, Get: func() (Prop, error) {
		return Prop{}, errors.New("secret backend detail")
	}}
	res.Props[pColor] = PropDef{AllProp: true} // nil Get
	req := PropfindRequest{Props: []xml.Name{pName, pTag, N("urn:x", "m"), pColor}}
	r := PropFind(req, res)
	if !slices.Equal(statuses(r), []int{200, 404, 500}) {
		t.Fatalf("statuses = %v", statuses(r))
	}
	if got := names(r.Propstats[0].Props); !slices.Equal(got, []xml.Name{pName}) {
		t.Errorf("200 = %v", got)
	}
	e := r.Propstats[2].Props
	if !slices.Equal(names(e), []xml.Name{pTag, pColor}) || e[0].Write != nil || e[1].Write != nil {
		t.Errorf("500 = %+v", e)
	}
	// An error during allprop still lets the other properties through.
	r = PropFind(PropfindRequest{AllProp: true}, res)
	if !slices.Equal(statuses(r), []int{200, 500}) || len(r.Propstats[0].Props) != 2 {
		t.Errorf("allprop with errors = %+v", r.Propstats)
	}
}

func TestPropFindEmptyResource(t *testing.T) {
	req := PropfindRequest{Props: []xml.Name{pName, pTag}}
	r := PropFind(req, Resource{Href: "/x"})
	if !slices.Equal(statuses(r), []int{404}) || len(r.Propstats[0].Props) != 2 {
		t.Fatalf("got %+v", r)
	}
	for _, req := range []PropfindRequest{{AllProp: true}, {PropName: true}, {}} {
		r := PropFind(req, Resource{Href: "/x"})
		if r.Status != 0 || len(r.Propstats) != 1 || r.Propstats[0].Status != 200 ||
			len(r.Propstats[0].Props) != 0 {
			t.Errorf("%+v -> %+v", req, r)
		}
	}
}

func TestPropFindKeyWins(t *testing.T) {
	res := Resource{Href: "/x", Props: map[xml.Name]PropDef{
		pName: {AllProp: true, Get: func() (Prop, error) { return TextProp(pTag, "v"), nil }},
	}}
	for _, req := range []PropfindRequest{{AllProp: true}, {Props: []xml.Name{pName}}} {
		r := PropFind(req, res)
		if got := names(r.Propstats[0].Props); !slices.Equal(got, []xml.Name{pName}) {
			t.Errorf("%+v: name = %v", req, got)
		}
	}
}

func TestPropFindDeterministic(t *testing.T) {
	var first Response
	var firstNames [][]xml.Name
	for i := range 50 {
		r := PropFind(PropfindRequest{AllProp: true, Props: []xml.Name{pData}}, testResource())
		var got [][]xml.Name
		for _, ps := range r.Propstats {
			got = append(got, names(ps.Props))
		}
		if i == 0 {
			first, firstNames = r, got
			continue
		}
		if !reflect.DeepEqual(got, firstNames) || len(r.Propstats) != len(first.Propstats) {
			t.Fatalf("run %d: %v != %v", i, got, firstNames)
		}
		pn := PropFind(PropfindRequest{PropName: true}, testResource())
		if !slices.Equal(names(pn.Propstats[0].Props), names(PropFind(
			PropfindRequest{PropName: true}, testResource()).Propstats[0].Props)) {
			t.Fatalf("propname order varies")
		}
	}
}

func writeResponses(t *testing.T, rs ...Response) string {
	t.Helper()
	rec := httptest.NewRecorder()
	seq := slices.Values(rs)
	if err := WriteMultiStatus(rec, seq, ""); err != nil {
		t.Fatal(err)
	}
	return rec.Body.String()
}

func TestPropFindMultistatusGolden(t *testing.T) {
	req := PropfindRequest{Props: []xml.Name{pName, N("urn:x", "none")}}
	got := writeResponses(t, PropFind(req, testResource()))
	want := xmlDeclaration +
		`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" ` +
		`xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/" ` +
		`xmlns:A="http://apple.com/ns/ical/"><D:response xmlns:ns1="urn:x">` +
		`<D:href>/cal/work/</D:href><D:propstat><D:prop><D:displayname>v</D:displayname>` +
		`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat><D:propstat><D:prop>` +
		`<ns1:none/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>` +
		`</D:response></D:multistatus>`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	root, err := ParseXML([]byte(got))
	if err != nil {
		t.Fatal(err)
	}
	resp := root.Child(ResponseEl)
	if resp == nil || len(resp.Kids(PropstatEl)) != 2 ||
		!strings.Contains(resp.Child(Href).Text, "/cal/work/") {
		t.Fatalf("tree = %+v", resp)
	}
	if n := resp.Kids(PropstatEl)[0].Child(PropEl).Child(pName); n == nil || n.Text != "v" {
		t.Errorf("200 value = %+v", n)
	}
}

func TestPropFindMultistatusShapes(t *testing.T) {
	// Every request mode must give a valid response shape, even with no props.
	for _, req := range []PropfindRequest{{AllProp: true}, {PropName: true}, {}, {Props: []xml.Name{pName}}} {
		for _, res := range []Resource{{Href: "/a"}, testResource()} {
			rec := httptest.NewRecorder()
			err := WriteMultiStatus(rec, slices.Values([]Response{PropFind(req, res)}), "")
			if err != nil {
				t.Errorf("%+v: %v", req, err)
			}
		}
	}
}

func TestPropFindNilGetIsServerError(t *testing.T) {
	res := Resource{Href: "/x", Props: map[xml.Name]PropDef{pName: {AllProp: true}}}
	r := PropFind(PropfindRequest{Props: []xml.Name{pName}}, res)
	if !slices.Equal(statuses(r), []int{500}) {
		t.Fatalf("statuses = %v", statuses(r))
	}
	got := r.Propstats[0].Props
	if len(got) != 1 || got[0].Name != pName || got[0].Write != nil {
		t.Errorf("500 = %+v", got)
	}
}
