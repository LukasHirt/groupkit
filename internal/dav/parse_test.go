// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func wantBadRequest(t *testing.T, err error) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if de.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", de.Status)
	}
}

func TestParsePropfind(t *testing.T) {
	tests := []struct {
		name string
		body string
		want PropfindRequest
	}{
		{"empty", "", PropfindRequest{AllProp: true}},
		{"whitespace", " \r\n\t ", PropfindRequest{AllProp: true}},
		{"allprop", `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`, PropfindRequest{AllProp: true}},
		{"propname", `<?xml version="1.0" encoding="utf-8" ?>
<propfind xmlns="DAV:"><propname/></propfind>`, PropfindRequest{PropName: true}},
		{"prop", `<?xml version="1.0" encoding="utf-8" ?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"
  xmlns:CS="http://calendarserver.org/ns/">
  <D:prop>
    <D:resourcetype/><D:displayname/><C:calendar-home-set/><CS:getctag/>
    <D:displayname/>
  </D:prop>
</D:propfind>`, PropfindRequest{Props: []xml.Name{
			ResourceType, N(NSDAV, "displayname"), CalendarHomeSet, N(NSCS, "getctag"),
		}}},
		{"allprop include", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:allprop/>
  <D:include><D:supported-live-property-set/><C:calendar-data/></D:include>
</D:propfind>`, PropfindRequest{AllProp: true, Props: []xml.Name{
			N(NSDAV, "supported-live-property-set"), CalendarData,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePropfind([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParsePropfindErrors(t *testing.T) {
	many := &strings.Builder{}
	many.WriteString(`<D:propfind xmlns:D="DAV:" xmlns:X="urn:x"><D:prop>`)
	for i := 0; i <= 1000; i++ {
		fmt.Fprintf(many, "<X:p%d/>", i)
	}
	many.WriteString(`</D:prop></D:propfind>`)
	tests := map[string]string{
		"wrong root":     `<D:mkcol xmlns:D="DAV:"/>`,
		"foreign root":   `<propfind xmlns="urn:other"><allprop/></propfind>`,
		"malformed":      `<D:propfind xmlns:D="DAV:"><D:allprop>`,
		"allprop+prop":   `<D:propfind xmlns:D="DAV:"><D:allprop/><D:prop><D:displayname/></D:prop></D:propfind>`,
		"propname+prop":  `<D:propfind xmlns:D="DAV:"><D:propname/><D:prop/></D:propfind>`,
		"none":           `<D:propfind xmlns:D="DAV:"/>`,
		"unrelated only": `<D:propfind xmlns:D="DAV:"><D:other/></D:propfind>`,
		"too many":       many.String(),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePropfind([]byte(body))
			wantBadRequest(t, err)
		})
	}
	_, err := ParsePropfind([]byte("<a"))
	var de *Error
	if !errors.As(err, &de) || !strings.Contains(de.Message, ErrXML.Error()) {
		t.Fatalf("malformed message = %v, want the ErrXML text", err)
	}
	// The cap holds for exactly 1,000 names.
	body := strings.Replace(many.String(), `<X:p1000/>`, "", 1)
	if got, err := ParsePropfind([]byte(body)); err != nil || len(got.Props) != 1000 {
		t.Fatalf("1000 props: %d, %v", len(got.Props), err)
	}
}

const rfcPropPatch = `<?xml version="1.0" encoding="utf-8" ?>
<D:propertyupdate xmlns:D="DAV:" xmlns:Z="http://ns.example.com/standards/z39.50/">
  <D:set><D:prop><Z:Authors><Z:Author>Jim Whitehead</Z:Author></Z:Authors></D:prop></D:set>
  <D:remove><D:prop><Z:Copyright-Owner/></D:prop></D:remove>
</D:propertyupdate>`

func TestParsePropPatch(t *testing.T) {
	got, err := ParsePropPatch([]byte(rfcPropPatch))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("updates = %d", len(got))
	}
	z := "http://ns.example.com/standards/z39.50/"
	if got[0].Name != N(z, "Authors") || got[0].Remove || got[0].Node == nil {
		t.Errorf("first = %+v", got[0])
	}
	if a := got[0].Node.Child(N(z, "Author")); a == nil || a.Text != "Jim Whitehead" {
		t.Errorf("children not preserved: %+v", got[0].Node)
	}
	if got[1].Name != N(z, "Copyright-Owner") || !got[1].Remove {
		t.Errorf("second = %+v", got[1])
	}
}

func TestParsePropPatchAppleColor(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:" xmlns:B="http://apple.com/ns/ical/">
 <A:set><A:prop>
  <B:calendar-color symbolic-color="custom">#FF2968FF</B:calendar-color>
  <A:displayname>Work</A:displayname>
 </A:prop></A:set>
 <A:ignored/>
</A:propertyupdate>`
	got, err := ParsePropPatch([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != N(NSApple, "calendar-color") ||
		got[0].Node.Text != "#FF2968FF" || got[1].Node.Text != "Work" {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePropPatchOrder(t *testing.T) {
	body := `<D:propertyupdate xmlns:D="DAV:">
<D:remove><D:prop><D:a/></D:prop></D:remove>
<D:set><D:prop><D:b/></D:prop></D:set>
<D:remove><D:prop><D:c/><D:d/></D:prop></D:remove></D:propertyupdate>`
	got, err := ParsePropPatch([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, u := range got {
		s = append(s, fmt.Sprintf("%s:%v", u.Name.Local, u.Remove))
	}
	if want := "a:true b:false c:true d:true"; strings.Join(s, " ") != want {
		t.Fatalf("order = %v", s)
	}
}

func TestParsePropPatchErrors(t *testing.T) {
	many := &strings.Builder{}
	many.WriteString(`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:x"><D:set><D:prop>`)
	for i := 0; i <= 1000; i++ {
		fmt.Fprintf(many, "<X:p%d/>", i)
	}
	many.WriteString(`</D:prop></D:set></D:propertyupdate>`)
	tests := map[string]string{
		"empty":          ``,
		"wrong root":     `<D:propfind xmlns:D="DAV:"><D:prop/></D:propfind>`,
		"malformed":      `<D:propertyupdate xmlns:D="DAV:"><D:set>`,
		"no updates":     `<D:propertyupdate xmlns:D="DAV:"/>`,
		"set no prop":    `<D:propertyupdate xmlns:D="DAV:"><D:set/></D:propertyupdate>`,
		"set empty prop": `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop/></D:set></D:propertyupdate>`,
		"too many":       many.String(),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePropPatch([]byte(body))
			wantBadRequest(t, err)
		})
	}
}

const appleMkcalendar = `<?xml version="1.0" encoding="UTF-8"?>
<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">
 <D:set><D:prop>
  <D:displayname>Lisa's Events</D:displayname>
  <C:calendar-description xml:lang="en">Calendar restricted to events.</C:calendar-description>
  <C:supported-calendar-component-set>
    <C:comp name="VEVENT"/><C:comp name="VTODO"/>
  </C:supported-calendar-component-set>
 </D:prop></D:set>
</C:mkcalendar>`

func TestParseMkcalendar(t *testing.T) {
	got, err := ParseMkcalendar([]byte(appleMkcalendar))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Props) != 3 || len(got.ResourceTypes) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got.Props[0].Name != N(NSDAV, "displayname") || got.Props[0].Node.Text != "Lisa's Events" {
		t.Errorf("displayname = %+v", got.Props[0])
	}
	comps := got.Props[2].Node.Kids(CalComp)
	if got.Props[2].Name != N(NSCalDAV, "supported-calendar-component-set") ||
		len(comps) != 2 || comps[0].Attr("name") != "VEVENT" || comps[1].Attr("name") != "VTODO" {
		t.Errorf("component set = %+v", got.Props[2])
	}
	for _, p := range got.Props {
		if p.Remove {
			t.Errorf("Remove set on %v", p.Name)
		}
	}
}

func TestParseMkcol(t *testing.T) {
	body := `<?xml version="1.0" encoding="utf-8" ?>
<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop>
    <D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype>
    <D:displayname>Contacts</D:displayname>
  </D:prop></D:set>
</D:mkcol>`
	got, err := ParseMkcol([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []xml.Name{Collection, N(NSCardDAV, "addressbook")}
	if !reflect.DeepEqual(got.ResourceTypes, want) {
		t.Errorf("types = %v", got.ResourceTypes)
	}
	if len(got.Props) != 2 || got.Props[0].Name != ResourceType ||
		len(got.Props[0].Node.Children) != 2 {
		t.Errorf("props = %+v", got.Props)
	}
}

func TestParseCollectionEmptyAndErrors(t *testing.T) {
	for _, body := range []string{"", "  \n"} {
		if got, err := ParseMkcol([]byte(body)); err != nil || len(got.Props) != 0 ||
			got.ResourceTypes != nil {
			t.Errorf("mkcol %q = %+v, %v", body, got, err)
		}
		if got, err := ParseMkcalendar([]byte(body)); err != nil || len(got.Props) != 0 {
			t.Errorf("mkcalendar %q = %+v, %v", body, got, err)
		}
	}
	many := &strings.Builder{}
	many.WriteString(`<D:mkcol xmlns:D="DAV:" xmlns:X="urn:x"><D:set><D:prop>`)
	for i := 0; i <= 1000; i++ {
		fmt.Fprintf(many, "<X:p%d/>", i)
	}
	many.WriteString(`</D:prop></D:set></D:mkcol>`)
	mk := map[string]string{
		"wrong root": `<D:mkcalendar xmlns:D="DAV:"/>`,
		"malformed":  `<D:mkcol xmlns:D="DAV:"><D:set>`,
		"empty prop": `<D:mkcol xmlns:D="DAV:"><D:set><D:prop/></D:set></D:mkcol>`,
		"no prop":    `<D:mkcol xmlns:D="DAV:"><D:set/></D:mkcol>`,
		"remove":     `<D:mkcol xmlns:D="DAV:"><D:remove><D:prop><D:a/></D:prop></D:remove></D:mkcol>`,
		"too many":   many.String(),
	}
	for name, body := range mk {
		t.Run("mkcol "+name, func(t *testing.T) {
			_, err := ParseMkcol([]byte(body))
			wantBadRequest(t, err)
		})
	}
	cal := map[string]string{
		"wrong root": `<D:mkcol xmlns:D="DAV:"/>`,
		"malformed":  `<C:mkcalendar`,
		"remove": `<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">` +
			`<D:remove><D:prop><D:a/></D:prop></D:remove></C:mkcalendar>`,
		"empty prop": `<C:mkcalendar xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">` +
			`<D:set><D:prop/></D:set></C:mkcalendar>`,
	}
	for name, body := range cal {
		t.Run("mkcalendar "+name, func(t *testing.T) {
			_, err := ParseMkcalendar([]byte(body))
			wantBadRequest(t, err)
		})
	}
}

func FuzzParsePropfind(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte(`<D:propfind xmlns:D="DAV:"><D:allprop/><D:include><D:a/></D:include></D:propfind>`))
	f.Add([]byte(`<D:propfind xmlns:D="DAV:"><D:prop><D:a/><D:b/></D:prop></D:propfind>`))
	f.Add([]byte(rfcPropPatch))
	f.Add([]byte(appleMkcalendar))
	f.Add([]byte(`<D:mkcol xmlns:D="DAV:"><D:set><D:prop><D:resourcetype><D:collection/>` +
		`</D:resourcetype></D:prop></D:set></D:mkcol>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParsePropfind(data)
		_, _ = ParsePropPatch(data)
		_, _ = ParseMkcol(data)
		_, _ = ParseMkcalendar(data)
		if root, err := ParseXML(data); err == nil {
			_, _ = propfindNode(root)
			_, _ = propPatchNode(root)
			_, _ = collectionNode(root, MkCol)
			_, _ = collectionNode(root, MkCalendar)
		}
	})
}

func TestParsePropfindManyDuplicateNames(t *testing.T) {
	body := "<D:propfind xmlns:D=\"DAV:\"><D:prop>" +
		strings.Repeat("<D:a/>", 90000) + "</D:prop></D:propfind>"
	if len(body) > MaxXMLBytes {
		t.Fatalf("test body is %d bytes, over the limit", len(body))
	}
	got, err := ParsePropfind([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Props) != 1 || got.Props[0] != N(NSDAV, "a") {
		t.Errorf("props = %v", got.Props)
	}
}

func TestPropNamesKeepsFirstOrder(t *testing.T) {
	a, b, c := N("urn:x", "a"), N("urn:x", "b"), N("urn:y", "a")
	got, err := PropNames([]*Node{{Name: b}, {Name: a}, {Name: b}, {Name: c}, {Name: a}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []xml.Name{b, a, c}) {
		t.Errorf("names = %v", got)
	}
}

func TestParseMkcolDuplicateResourceTypes(t *testing.T) {
	body := `<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:set><D:prop><D:resourcetype>
    <D:collection/><CR:addressbook/><D:collection/><CR:addressbook/>
  </D:resourcetype></D:prop></D:set>
  <D:set><D:prop><D:resourcetype><CR:addressbook/><D:collection/></D:resourcetype></D:prop></D:set>
</D:mkcol>`
	got, err := ParseMkcol([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []xml.Name{Collection, N(NSCardDAV, "addressbook")}
	if !reflect.DeepEqual(got.ResourceTypes, want) {
		t.Errorf("types = %v", got.ResourceTypes)
	}
}
