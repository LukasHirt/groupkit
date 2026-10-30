// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

const (
	homeURL = "/dav/calendars/alice/"
	newURL  = "/dav/calendars/alice/new/"
)

var newRef = store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "new"}

// emptyFixture is a handler over an empty store; alice resolves to "alice".
func emptyFixture(t *testing.T, st store.Store) *objFixture {
	t.Helper()
	return emptyFixtureMax(t, st, 1<<20)
}

func (f *objFixture) getCal(t *testing.T, ref store.CollectionRef) (store.Collection, error) {
	t.Helper()
	return f.st.GetCollection(context.Background(), ref)
}

const mkNS = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/" xmlns:CS="http://calendarserver.org/ns/"`

func mkcalBody(props string) []byte {
	return []byte(`<?xml version="1.0"?><C:mkcalendar ` + mkNS + `><D:set><D:prop>` + props + `</D:prop></D:set></C:mkcalendar>`)
}

func mkcolBody(props string) []byte {
	return []byte(`<?xml version="1.0"?><D:mkcol ` + mkNS + `><D:set><D:prop>` + props + `</D:prop></D:set></D:mkcol>`)
}

func patchBody(blocks string) []byte {
	return []byte(`<?xml version="1.0"?><D:propertyupdate ` + mkNS + ` xmlns:X="urn:x">` + blocks + `</D:propertyupdate>`)
}

func setProps(props string) []byte {
	return patchBody(`<D:set><D:prop>` + props + `</D:prop></D:set>`)
}

// tzText is the time zone text a client sends.
var tzText = "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//Apple Inc.//iCal 5.0//EN\nCALSCALE:GREGORIAN\n" +
	strings.ReplaceAll(pragueZone, "\r\n", "\n") + "END:VCALENDAR"

// noZone is a well-formed calendar without a VTIMEZONE.
const noZone = "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\nEND:VCALENDAR"

var methods = []string{"MKCALENDAR", "MKCOL"}

// create sends the creation method with a body made for it.
func (f *objFixture) create(method, path, props string) *httptest.ResponseRecorder {
	var body []byte
	switch {
	case method == "MKCALENDAR" && props == "":
	case method == "MKCALENDAR":
		body = mkcalBody(props)
	default:
		body = mkcolBody(`<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>` + props)
	}
	return f.doPath(method, path, body, nil)
}

func TestCreatePlain(t *testing.T) {
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				rec := f.create(m, newURL, "")
				wantStatus(t, rec, http.StatusCreated)
				if loc := rec.Header().Get("Location"); loc != newURL {
					t.Errorf("Location = %q", loc)
				}
				c, err := f.getCal(t, newRef)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(c.Props, store.Props{}) {
					t.Errorf("props = %+v", c.Props)
				}
				r := parseReply(t, f.doPath("PROPFIND", homeURL, nil, hdrs{"Depth": "1"}))
				if len(r.hrefs) != 2 || r.hrefs[1] != newURL {
					t.Errorf("hrefs = %v", r.hrefs)
				}
				wantStatus(t, f.doPath("PROPFIND", newURL, nil, hdrs{"Depth": "0"}), http.StatusMultiStatus)
			})
		})
	}
}

func TestCreateNoTrailingSlashAndEscapedLocation(t *testing.T) {
	for _, m := range methods {
		f := emptyFixture(t, memory.New())
		rec := f.create(m, "/dav/calendars/alice/my%20cal", "")
		wantStatus(t, rec, http.StatusCreated)
		if loc := rec.Header().Get("Location"); loc != "/dav/calendars/alice/my%20cal/" {
			t.Errorf("%s: Location = %q", m, loc)
		}
		if _, err := f.getCal(t, store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "my cal"}); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
}

func TestCreateWithProperties(t *testing.T) {
	props := `<D:displayname>Work</D:displayname><C:calendar-description>Team events</C:calendar-description>` +
		`<A:calendar-color>#FF8800FF</A:calendar-color><A:calendar-order>5</A:calendar-order>` +
		`<C:calendar-timezone>` + tzText + `</C:calendar-timezone>` +
		`<C:supported-calendar-component-set><C:comp name="VEVENT"/><C:comp name="VTODO"/></C:supported-calendar-component-set>`
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				wantStatus(t, f.create(m, newURL, props), http.StatusCreated)
				c, err := f.getCal(t, newRef)
				if err != nil {
					t.Fatal(err)
				}
				want := store.Props{
					DisplayName: "Work", Description: "Team events", Color: "#FF8800FF", Order: 5,
					Timezone: tzText, Components: []string{"VEVENT", "VTODO"},
				}
				if !reflect.DeepEqual(c.Props, want) {
					t.Errorf("props = %+v\nwant    %+v", c.Props, want)
				}
				// PROPFIND reports what was stored.
				r := parseReply(t, f.doPath("PROPFIND", newURL, []byte(propBody(
					`<D:displayname/><A:calendar-color/><A:calendar-order/><C:supported-calendar-component-set/>`)), hdrs{"Depth": "0"}))
				if r.must(t, 0, dav.CalendarColor).Text != "#FF8800FF" || r.must(t, 0, dav.CalendarOrder).Text != "5" {
					t.Errorf("props = %v", r.props[0])
				}
				// The stored set is kept; only VEVENT, which the engine serves,
				// is advertised.
				comps := r.must(t, 0, dav.SupportedCalendarComponentSet).Kids(dav.CalComp)
				if len(comps) != 1 || comps[0].Attr("name") != "VEVENT" {
					t.Errorf("advertised %d comps, want VEVENT only", len(comps))
				}
			})
		})
	}
}

// Raw bodies as the clients send them.
func TestCreateClientBodies(t *testing.T) {
	apple := `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav"><A:set xmlns:A="DAV:"><A:prop>` +
		`<D:calendar-color xmlns:D="http://apple.com/ns/ical/" symbolic-color="custom">#1BADF8FF</D:calendar-color>` +
		`<B:calendar-description>Home things</B:calendar-description>` +
		`<C:calendar-order xmlns:C="http://apple.com/ns/ical/">3</C:calendar-order>` +
		`<A:displayname>Home</A:displayname>` +
		`<B:supported-calendar-component-set><B:comp name="VEVENT"/></B:supported-calendar-component-set>` +
		`<B:calendar-timezone>` + tzText + `</B:calendar-timezone>` +
		`</A:prop></A:set></B:mkcalendar>`
	thunderbird := `<?xml version="1.0" encoding="UTF-8"?>
<mkcalendar xmlns="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:"><D:set><D:prop>
<D:displayname>Birthdays</D:displayname>
<calendar-color xmlns="http://apple.com/ns/ical/">#e6c800</calendar-color>
<supported-calendar-component-set><comp name="VEVENT"/></supported-calendar-component-set>
</D:prop></D:set></mkcalendar>`
	davx5 := `<?xml version="1.0" encoding="UTF-8" ?>
<mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/"><set><prop>` +
		`<resourcetype><collection/><C:calendar/></resourcetype>` +
		`<displayname>Tasks and events</displayname>` +
		`<C:calendar-description>Mixed</C:calendar-description>` +
		`<A:calendar-color>#FFA000</A:calendar-color>` +
		`<C:supported-calendar-component-set><C:comp name="VEVENT"/><C:comp name="VTODO"/></C:supported-calendar-component-set>` +
		`</prop></set></mkcol>`

	tests := []struct {
		name, method, body string
		want               store.Props
	}{
		{"apple", "MKCALENDAR", apple, store.Props{
			DisplayName: "Home", Description: "Home things", Color: "#1BADF8FF", Order: 3,
			Timezone: tzText, Components: []string{"VEVENT"}}},
		{"thunderbird", "MKCALENDAR", thunderbird, store.Props{
			DisplayName: "Birthdays", Color: "#e6c800", Components: []string{"VEVENT"}}},
		{"davx5 extended mkcol", "MKCOL", davx5, store.Props{
			DisplayName: "Tasks and events", Description: "Mixed", Color: "#FFA000", Components: []string{"VEVENT", "VTODO"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				wantStatus(t, f.doPath(tt.method, newURL, []byte(tt.body), hdrs{"Content-Type": "application/xml; charset=utf-8"}), http.StatusCreated)
				c, err := f.getCal(t, newRef)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(c.Props, tt.want) {
					t.Errorf("props = %+v\nwant    %+v", c.Props, tt.want)
				}
			})
		})
	}
}

// DAVx5 also creates calendars with MKCALENDAR and the same properties.
func TestCreateMkcalendarWithTwoComponents(t *testing.T) {
	f := emptyFixture(t, memory.New())
	body := mkcalBody(`<D:displayname>Both</D:displayname><C:supported-calendar-component-set>` +
		`<C:comp name="VEVENT"/><C:comp name="VTODO"/></C:supported-calendar-component-set>`)
	wantStatus(t, f.doPath("MKCALENDAR", newURL, body, nil), http.StatusCreated)
	c, _ := f.getCal(t, newRef)
	if !slices.Equal(c.Props.Components, []string{"VEVENT", "VTODO"}) {
		t.Errorf("components = %v", c.Props.Components)
	}
}

// A set without VEVENT would give a calendar that refuses every write, so
// it is refused up front and nothing is created.
func TestCreateWithoutEventsIsRefused(t *testing.T) {
	for _, m := range methods {
		for name, set := range map[string]string{
			"VTODO only":         `<C:comp name="VTODO"/>`,
			"VTODO and VJOURNAL": `<C:comp name="vtodo"/><C:comp name="VJOURNAL"/>`,
		} {
			t.Run(m+" "+name, func(t *testing.T) {
				eachStore(t, func(t *testing.T, st store.Store) {
					f := emptyFixture(t, st)
					rec := f.create(m, newURL, `<C:supported-calendar-component-set>`+set+`</C:supported-calendar-component-set>`)
					wantStatus(t, rec, http.StatusForbidden)
					if got := precondition(t, rec); got != "supported-calendar-component" {
						t.Errorf("precondition = %q", got)
					}
					if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
						t.Errorf("calendar exists after the refusal: %v", err)
					}
				})
			})
		}
	}
}

// An existing name is a 405 with Allow, whatever component set the body asks for.
func TestCreateWithoutEventsOnExistingNameIs405(t *testing.T) {
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			f := emptyFixture(t, memory.New())
			wantStatus(t, f.create(m, newURL, ""), http.StatusCreated)
			rec := f.create(m, newURL, `<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`)
			wantStatus(t, rec, http.StatusMethodNotAllowed)
			if a := rec.Header().Get("Allow"); a == "" || strings.Contains(a, m) {
				t.Errorf("Allow = %q", a)
			}
		})
	}
}

// DAVx5 asks for events, tasks and notes. The calendar is made and keeps
// the set, but advertises only what the engine serves.
func TestCreateWithEventsAndMoreAdvertisesEvents(t *testing.T) {
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			f := emptyFixture(t, memory.New())
			wantStatus(t, f.create(m, newURL, `<C:supported-calendar-component-set>`+
				`<C:comp name="VTODO"/><C:comp name="VEVENT"/><C:comp name="VJOURNAL"/></C:supported-calendar-component-set>`), http.StatusCreated)
			c, _ := f.getCal(t, newRef)
			if !slices.Equal(c.Props.Components, []string{"VTODO", "VEVENT", "VJOURNAL"}) {
				t.Errorf("stored %v", c.Props.Components)
			}
			r := parseReply(t, f.doPath("PROPFIND", newURL, []byte(propBody(`<C:supported-calendar-component-set/>`)), hdrs{"Depth": "0"}))
			comps := r.must(t, 0, dav.SupportedCalendarComponentSet).Kids(dav.CalComp)
			if len(comps) != 1 || comps[0].Attr("name") != "VEVENT" {
				t.Errorf("advertised %d comps, want VEVENT only", len(comps))
			}
		})
	}
}

func TestAdvertisedComponents(t *testing.T) {
	for _, tt := range []struct{ stored, want []string }{
		{nil, []string{"VEVENT"}},
		{[]string{"VTODO"}, []string{"VEVENT"}},
		{[]string{"VTODO", "VJOURNAL"}, []string{"VEVENT"}},
		{[]string{"VEVENT"}, []string{"VEVENT"}},
		{[]string{"VTODO", "VEVENT", "VJOURNAL"}, []string{"VEVENT"}},
	} {
		if got := advertisedComponents(tt.stored); !slices.Equal(got, tt.want) {
			t.Errorf("advertisedComponents(%v) = %v, want %v", tt.stored, got, tt.want)
		}
	}
}

func TestCreateColourForms(t *testing.T) {
	for _, colour := range []string{"#aabbcc", "#AABBCC", "#aAbBcC", "#aabbccdd", "#00000000", "#FFFFFFFF"} {
		f := emptyFixture(t, memory.New())
		wantStatus(t, f.create("MKCALENDAR", newURL, `<A:calendar-color>`+colour+`</A:calendar-color>`), http.StatusCreated)
		c, _ := f.getCal(t, newRef)
		if c.Props.Color != colour {
			t.Errorf("colour %q stored as %q", colour, c.Props.Color)
		}
	}
	// Surrounding whitespace is not part of the value.
	f := emptyFixture(t, memory.New())
	wantStatus(t, f.create("MKCALENDAR", newURL, "<A:calendar-color>\n  #123456\n</A:calendar-color>"), http.StatusCreated)
	if c, _ := f.getCal(t, newRef); c.Props.Color != "#123456" {
		t.Errorf("colour = %q", c.Props.Color)
	}
}

// A blank colour is no colour, as a blank name is no name.
func TestBlankColourIsUnset(t *testing.T) {
	f := emptyFixture(t, memory.New())
	wantStatus(t, f.create("MKCALENDAR", newURL, `<A:calendar-color/>`), http.StatusCreated)
	if c, _ := f.getCal(t, newRef); c.Props.Color != "" {
		t.Errorf("colour = %q", c.Props.Color)
	}
	g := patchFixture(t, memory.New())
	wantStatus(t, g.doPath("PROPPATCH", objBase, setProps(`<A:calendar-color>  </A:calendar-color>`), nil), http.StatusMultiStatus)
	if c, _ := g.getCal(t, objRef); c.Props.Color != "" {
		t.Errorf("colour = %q", c.Props.Color)
	}
}

func TestCreateOrderBounds(t *testing.T) {
	for in, want := range map[string]int{"0": 0, "7": 7, "-3": -3, "1000000": 1000000, "-1000000": -1000000, " 12 ": 12} {
		f := emptyFixture(t, memory.New())
		wantStatus(t, f.create("MKCALENDAR", newURL, `<A:calendar-order>`+in+`</A:calendar-order>`), http.StatusCreated)
		if c, _ := f.getCal(t, newRef); c.Props.Order != want {
			t.Errorf("order %q stored as %d", in, c.Props.Order)
		}
	}
}

func TestCreateComponentNames(t *testing.T) {
	f := emptyFixture(t, memory.New())
	wantStatus(t, f.create("MKCALENDAR", newURL, `<C:supported-calendar-component-set>`+
		`<C:comp name="vevent"/><C:comp name="VtOdO"/><C:comp name="VEVENT"/></C:supported-calendar-component-set>`), http.StatusCreated)
	c, _ := f.getCal(t, newRef)
	if !slices.Equal(c.Props.Components, []string{"VEVENT", "VTODO"}) {
		t.Errorf("components = %v, want upper-cased and without duplicates", c.Props.Components)
	}
	// Eight components are fine.
	g := emptyFixture(t, memory.New())
	var eight strings.Builder
	for _, n := range []string{"VEVENT", "VTODO", "VJOURNAL", "VFREEBUSY", "VAVAILABILITY", "VPOLL", "VLOCATION", "VRESOURCE"} {
		eight.WriteString(`<C:comp name="` + n + `"/>`)
	}
	wantStatus(t, g.create("MKCALENDAR", newURL, `<C:supported-calendar-component-set>`+eight.String()+`</C:supported-calendar-component-set>`), http.StatusCreated)
}

func TestCreateResourceTypes(t *testing.T) {
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			f := emptyFixture(t, memory.New())
			body := mkcolBody(`<D:resourcetype><C:calendar/></D:resourcetype>`)
			if m == "MKCALENDAR" {
				body = mkcalBody(`<D:resourcetype><C:calendar/></D:resourcetype>`)
			}
			wantStatus(t, f.doPath(m, newURL, body, nil), http.StatusCreated)
		})
	}
}

func TestMkcolRejectedResourceTypes(t *testing.T) {
	tests := map[string]string{
		"collection only":        `<D:resourcetype><D:collection/></D:resourcetype>`,
		"empty type":             `<D:resourcetype/>`,
		"addressbook":            `<D:resourcetype><D:collection/><CR:addressbook xmlns:CR="urn:ietf:params:xml:ns:carddav"/></D:resourcetype>`,
		"calendar and principal": `<D:resourcetype><C:calendar/><D:principal/></D:resourcetype>`,
		"calendar in other ns":   `<D:resourcetype><D:collection/><X:calendar xmlns:X="urn:x"/></D:resourcetype>`,
	}
	for name, props := range tests {
		for _, m := range methods {
			t.Run(name+"/"+m, func(t *testing.T) {
				eachStore(t, func(t *testing.T, st store.Store) {
					f := emptyFixture(t, st)
					body := mkcolBody(props)
					if m == "MKCALENDAR" {
						body = mkcalBody(props)
					}
					wantPrecondition(t, f.doPath(m, newURL, body, nil), http.StatusForbidden, "valid-resourcetype")
					if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
						t.Errorf("calendar exists after rejection: %v", err)
					}
				})
			})
		}
	}
}

func TestCreateProtectedProperties(t *testing.T) {
	tests := map[string]string{
		"getetag":              `<D:getetag>"x"</D:getetag>`,
		"ctag":                 `<CS:getctag>1</CS:getctag>`,
		"sync-token":           `<D:sync-token>x</D:sync-token>`,
		"getcontenttype":       `<D:getcontenttype>x</D:getcontenttype>`,
		"creationdate":         `<D:creationdate>x</D:creationdate>`,
		"owner":                `<D:owner><D:href>/x</D:href></D:owner>`,
		"unknown DAV property": `<D:thing>1</D:thing>`,
		"valid beside getetag": `<D:displayname>A</D:displayname><D:getetag>"x"</D:getetag>`,
		"max-resource-size":    `<C:max-resource-size>1</C:max-resource-size>`,
		"supported-data":       `<C:supported-calendar-data/>`,
		"min-date-time":        `<C:min-date-time>x</C:min-date-time>`,
		"home-set":             `<C:calendar-home-set><D:href>/x</D:href></C:calendar-home-set>`,
		"report-set":           `<D:supported-report-set/>`,
	}
	for name, props := range tests {
		for _, m := range methods {
			t.Run(name+"/"+m, func(t *testing.T) {
				eachStore(t, func(t *testing.T, st store.Store) {
					f := emptyFixture(t, st)
					wantPrecondition(t, f.create(m, newURL, props), http.StatusForbidden, "cannot-modify-protected-property")
					if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
						t.Errorf("calendar exists after rejection: %v", err)
					}
				})
			})
		}
	}
}

// Properties the engine does not handle, outside DAV:, are skipped when a
// calendar is created; the handled ones are still stored.
func TestCreateSkipsUnhandledProperties(t *testing.T) {
	skipped := `<C:schedule-calendar-transp><C:opaque/></C:schedule-calendar-transp>` +
		`<C:calendar-free-busy-set/><A:refreshrate>P1D</A:refreshrate>` +
		`<CS:subscribed-strip-alarms/><C:calendar-timezone-id>Europe/Prague</C:calendar-timezone-id>` +
		`<X:thing xmlns:X="urn:x">1</X:thing>`
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				wantStatus(t, f.create(m, newURL, skipped+`<D:displayname>Kept</D:displayname><A:calendar-color>#010203</A:calendar-color>`), http.StatusCreated)
				c, err := f.getCal(t, newRef)
				if err != nil {
					t.Fatal(err)
				}
				if want := (store.Props{DisplayName: "Kept", Color: "#010203"}); !reflect.DeepEqual(c.Props, want) {
					t.Errorf("props = %+v, want %+v", c.Props, want)
				}
			})
		})
	}
}

// The body Apple Calendar sends for a new calendar.
func TestCreateAppleCalendarBody(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav"><A:set xmlns:A="DAV:"><A:prop>
<B:calendar-free-busy-set xmlns:B="urn:ietf:params:xml:ns:caldav"/>
<B:schedule-calendar-transp><B:opaque/></B:schedule-calendar-transp>
<D:calendar-color xmlns:D="http://apple.com/ns/ical/" symbolic-color="blue">#0E61B9FF</D:calendar-color>
<C:calendar-order xmlns:C="http://apple.com/ns/ical/">7</C:calendar-order>
<A:displayname>Apple</A:displayname>
<B:supported-calendar-component-set><B:comp name="VEVENT"/></B:supported-calendar-component-set>
<E:refreshrate xmlns:E="http://apple.com/ns/ical/">P1D</E:refreshrate>
<F:subscribed-strip-alarms xmlns:F="http://calendarserver.org/ns/"/>
</A:prop></A:set></B:mkcalendar>`
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		wantStatus(t, f.doPath("MKCALENDAR", newURL, []byte(body), nil), http.StatusCreated)
		c, _ := f.getCal(t, newRef)
		want := store.Props{DisplayName: "Apple", Color: "#0E61B9FF", Order: 7, Components: []string{"VEVENT"}}
		if !reflect.DeepEqual(c.Props, want) {
			t.Errorf("props = %+v, want %+v", c.Props, want)
		}
	})
}

// RFC 7809 calendar-timezone-id, as DAVx5 may send it.
func TestCreateDAVx5TimezoneID(t *testing.T) {
	body := `<?xml version="1.0"?><mkcol xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><set><prop>` +
		`<resourcetype><collection/><C:calendar/></resourcetype><displayname>D</displayname>` +
		`<C:calendar-timezone-id>Europe/Vienna</C:calendar-timezone-id></prop></set></mkcol>`
	f := emptyFixture(t, memory.New())
	wantStatus(t, f.doPath("MKCOL", newURL, []byte(body), nil), http.StatusCreated)
	if c, _ := f.getCal(t, newRef); c.Props.DisplayName != "D" || c.Props.Timezone != "" {
		t.Errorf("props = %+v", c.Props)
	}
}

// A calendar needs C:calendar in the resourcetype of a MKCOL.
func TestPlainMkcolIsRefused(t *testing.T) {
	bodies := map[string][]byte{
		"no body":           nil,
		"props, no type":    mkcolBody(`<D:displayname>A</D:displayname>`),
		"skipped prop only": mkcolBody(`<A:refreshrate>P1D</A:refreshrate>`),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				wantPrecondition(t, f.doPath("MKCOL", newURL, body, nil), http.StatusForbidden, "valid-resourcetype")
				if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("calendar exists: %v", err)
				}
			})
		})
	}
}

// Allow is advertised for an existing name only, not for other failures.
func TestCreateAllowOnlyForExistingName(t *testing.T) {
	f := emptyFixture(t, memory.New())
	f.h.store = createFailStore{f.st}
	for _, m := range methods {
		rec := f.create(m, newURL, "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if a := rec.Header().Get("Allow"); a != "" {
			t.Errorf("%s: 500 carries Allow %q", m, a)
		}
	}
	f.h.store = invalidNameCreateStore{f.st}
	for _, m := range methods {
		rec := f.create(m, newURL, "")
		wantStatus(t, rec, http.StatusBadRequest)
		if a := rec.Header().Get("Allow"); a != "" {
			t.Errorf("%s: 400 carries Allow %q", m, a)
		}
	}
}

type invalidNameCreateStore struct{ store.Store }

func (invalidNameCreateStore) CreateCollection(context.Context, store.CollectionRef, store.Props) (store.Collection, error) {
	return store.Collection{}, fmt.Errorf("wrapped: %w", store.ErrInvalidName)
}

func TestCreateTimezoneShape(t *testing.T) {
	zone := strings.ReplaceAll(pragueZone, "\r\n", "\n")
	wrap := func(inner string) string {
		return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:x\n" + inner + "END:VCALENDAR"
	}
	ev := "BEGIN:VEVENT\nUID:x\nDTSTAMP:20260101T000000Z\nEND:VEVENT\n"
	other := strings.Replace(zone, "Europe/Prague", "Europe/Vienna", 1)
	for name, tz := range map[string]string{
		"two timezones":        wrap(zone + other),
		"event next to a zone": wrap(zone + ev),
		"zone and a todo":      wrap(zone + "BEGIN:VTODO\nUID:x\nEND:VTODO\n"),
	} {
		for _, m := range methods {
			t.Run(name+"/"+m, func(t *testing.T) {
				f := emptyFixture(t, memory.New())
				wantStatus(t, f.create(m, newURL, `<C:calendar-timezone>`+tz+`</C:calendar-timezone>`), http.StatusBadRequest)
			})
		}
		t.Run(name+"/proppatch", func(t *testing.T) {
			f := patchFixture(t, memory.New())
			r := parseReply(t, f.doPath("PROPPATCH", objBase, setProps(`<C:calendar-timezone>`+tz+`</C:calendar-timezone>`), nil))
			if len(r.props[0][403]) != 1 {
				t.Errorf("props = %v", r.props[0])
			}
		})
	}
	f := emptyFixture(t, memory.New())
	wantStatus(t, f.create("MKCALENDAR", newURL, `<C:calendar-timezone>`+wrap(zone)+`</C:calendar-timezone>`), http.StatusCreated)
}

func TestCreateBadValues(t *testing.T) {
	longColour := "#" + strings.Repeat("a", 5000)
	tests := map[string]string{
		"colour name":           `<A:calendar-color>red</A:calendar-color>`,
		"colour five digits":    `<A:calendar-color>#12345</A:calendar-color>`,
		"colour seven digits":   `<A:calendar-color>#1234567</A:calendar-color>`,
		"colour nine digits":    `<A:calendar-color>#123456789</A:calendar-color>`,
		"colour not hex":        `<A:calendar-color>#GGGGGG</A:calendar-color>`,
		"colour no hash":        `<A:calendar-color>123456</A:calendar-color>`,
		"colour css function":   `<A:calendar-color>rgb(1,2,3)</A:calendar-color>`,
		"colour injection":      `<A:calendar-color>#123456;x</A:calendar-color>`,
		"colour long":           `<A:calendar-color>` + longColour + `</A:calendar-color>`,
		"colour child":          `<A:calendar-color>#123456<X:y xmlns:X="urn:x"/></A:calendar-color>`,
		"order text":            `<A:calendar-order>abc</A:calendar-order>`,
		"order big":             `<A:calendar-order>1e9</A:calendar-order>`,
		"order above":           `<A:calendar-order>1000001</A:calendar-order>`,
		"order below":           `<A:calendar-order>-1000001</A:calendar-order>`,
		"order huge":            `<A:calendar-order>99999999999999999999999</A:calendar-order>`,
		"order float":           `<A:calendar-order>1.5</A:calendar-order>`,
		"order empty":           `<A:calendar-order/>`,
		"timezone not ical":     `<C:calendar-timezone>hello</C:calendar-timezone>`,
		"timezone no vtimezone": `<C:calendar-timezone>` + noZone + `</C:calendar-timezone>`,
		"timezone event only":   `<C:calendar-timezone>` + strings.TrimSuffix(string(event("x")), "\r\n") + `</C:calendar-timezone>`,
		"timezone too long":     `<C:calendar-timezone>` + tzText[:len(tzText)-len("END:VCALENDAR")] + strings.Repeat("X-PAD:"+strings.Repeat("x", 100)+"\n", 700) + `END:VCALENDAR</C:calendar-timezone>`,
		"timezone child":        `<C:calendar-timezone>` + tzText + `<X:y xmlns:X="urn:x"/></C:calendar-timezone>`,
		"name too long":         `<D:displayname>` + strings.Repeat("x", 256) + `</D:displayname>`,
		"name child":            `<D:displayname>a<X:y xmlns:X="urn:x">x</X:y></D:displayname>`,
		"description too long":  `<C:calendar-description>` + strings.Repeat("x", 4097) + `</C:calendar-description>`,
		"description child":     `<C:calendar-description>a<X:y xmlns:X="urn:x">x</X:y></C:calendar-description>`,
		"components other":      `<C:supported-calendar-component-set><X:comp xmlns:X="urn:x" name="VEVENT"/></C:supported-calendar-component-set>`,
		"components non-comp":   `<C:supported-calendar-component-set><C:comp name="VEVENT"/><C:prop name="UID"/></C:supported-calendar-component-set>`,
		"components only other": `<C:supported-calendar-component-set><C:prop name="UID"/></C:supported-calendar-component-set>`,
		"components none":       `<C:supported-calendar-component-set/>`,
		"components no name":    `<C:supported-calendar-component-set><C:comp/></C:supported-calendar-component-set>`,
		"components empty name": `<C:supported-calendar-component-set><C:comp name=""/></C:supported-calendar-component-set>`,
		"components digits":     `<C:supported-calendar-component-set><C:comp name="VEV3NT"/></C:supported-calendar-component-set>`,
		"components token":      `<C:supported-calendar-component-set><C:comp name="VEVENT VTODO"/></C:supported-calendar-component-set>`,
		"components ctl":        `<C:supported-calendar-component-set><C:comp name="VEVENT&#10;X"/></C:supported-calendar-component-set>`,
		"components nine":       `<C:supported-calendar-component-set>` + strings.Repeat(`<C:comp name="A"/>`, 4) + strings.Repeat(`<C:comp name="B"/>`, 5) + `</C:supported-calendar-component-set>`,
		"components long name":  `<C:supported-calendar-component-set><C:comp name="` + strings.Repeat("V", 200) + `"/></C:supported-calendar-component-set>`,
	}
	// Nine distinct names are over the limit; duplicates of two are not.
	tests["components nine"] = func() string {
		var b strings.Builder
		b.WriteString(`<C:supported-calendar-component-set>`)
		for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
			b.WriteString(`<C:comp name="` + n + `"/>`)
		}
		return b.String() + `</C:supported-calendar-component-set>`
	}()
	for name, props := range tests {
		for _, m := range methods {
			t.Run(name+"/"+m, func(t *testing.T) {
				f := emptyFixture(t, memory.New())
				wantStatus(t, f.create(m, newURL, props), http.StatusBadRequest)
				if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("calendar exists after rejection: %v", err)
				}
			})
		}
	}
}

func TestCreateHugeColourBody(t *testing.T) {
	f := emptyFixture(t, memory.New())
	body := mkcalBody(`<A:calendar-color>#` + strings.Repeat("a", 10<<20) + `</A:calendar-color>`)
	wantStatus(t, f.doPath("MKCALENDAR", newURL, body, nil), http.StatusRequestEntityTooLarge)
	if _, err := f.getCal(t, newRef); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("calendar exists: %v", err)
	}
}

func TestCreateBadRequests(t *testing.T) {
	tests := []struct {
		name, method string
		body         []byte
		path         string
	}{
		{"malformed", "MKCALENDAR", []byte("<C:mkcalendar"), newURL},
		{"wrong root", "MKCALENDAR", []byte(`<a xmlns="DAV:"/>`), newURL},
		{"mkcol root for mkcalendar", "MKCALENDAR", mkcolBody(`<D:displayname>x</D:displayname>`), newURL},
		{"mkcalendar root for mkcol", "MKCOL", mkcalBody(`<D:displayname>x</D:displayname>`), newURL},
		{"remove", "MKCALENDAR", []byte(`<C:mkcalendar ` + mkNS + `><D:remove><D:prop><D:displayname/></D:prop></D:remove></C:mkcalendar>`), newURL},
		{"doctype", "MKCALENDAR", []byte(`<!DOCTYPE x [<!ENTITY a "b">]><C:mkcalendar ` + mkNS + `/>`), newURL},
		{"dotdot", "MKCALENDAR", nil, "/dav/calendars/alice/.."},
		{"encoded dotdot", "MKCALENDAR", nil, "/dav/calendars/alice/%2e%2e/"},
		{"encoded slash", "MKCALENDAR", nil, "/dav/calendars/alice/a%2Fb/"},
		{"300 bytes", "MKCALENDAR", nil, "/dav/calendars/alice/" + strings.Repeat("a", 300) + "/"},
		{"mkcol dotdot", "MKCOL", nil, "/dav/calendars/alice/%2e%2e/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := emptyFixture(t, memory.New())
			wantStatus(t, f.doPath(tt.method, tt.path, tt.body, nil), http.StatusBadRequest)
			list, _ := f.st.ListCollections(context.Background(), "alice", store.KindCalendar)
			if len(list) != 0 {
				t.Errorf("created %v", list)
			}
		})
	}
}

func TestCreateBoundaryLengths(t *testing.T) {
	f := emptyFixture(t, memory.New())
	props := `<D:displayname>` + strings.Repeat("é", 127) + `x</D:displayname>` +
		`<C:calendar-description>` + strings.Repeat("d", 4096) + `</C:calendar-description>`
	wantStatus(t, f.create("MKCALENDAR", newURL, props), http.StatusCreated)
	c, _ := f.getCal(t, newRef)
	if len(c.Props.DisplayName) != 255 || len(c.Props.Description) != 4096 {
		t.Errorf("lengths %d, %d", len(c.Props.DisplayName), len(c.Props.Description))
	}
	// 64 KiB of time zone text is accepted, one byte more is not.
	pad := func(n int) string {
		base := tzText[:len(tzText)-len("END:VCALENDAR")]
		fill := n - len(base) - len("END:VCALENDAR") - len("X-P:\n")
		return base + "X-P:" + strings.Repeat("x", fill) + "\nEND:VCALENDAR"
	}
	for n, want := range map[int]int{64 << 10: http.StatusCreated, 64<<10 + 1: http.StatusBadRequest} {
		g := emptyFixture(t, memory.New())
		text := pad(n)
		if len(text) != n {
			t.Fatalf("len = %d", len(text))
		}
		wantStatus(t, g.create("MKCALENDAR", newURL, `<C:calendar-timezone>`+text+`</C:calendar-timezone>`), want)
	}
}

func TestCreateExistsHomeObjectAndTooLarge(t *testing.T) {
	f := memFixture(t)
	for _, m := range methods {
		rec := f.doPath(m, objBase, nil, nil)
		wantStatus(t, rec, http.StatusMethodNotAllowed)
		a := rec.Header().Get("Allow")
		if strings.Contains(a, m) || !strings.Contains(a, "PROPFIND") || !strings.Contains(a, "REPORT") {
			t.Errorf("%s Allow = %q", m, a)
		}
		for _, p := range []string{homeURL, objBase + "x.ics"} {
			rec := f.doPath(m, p, nil, nil)
			wantStatus(t, rec, http.StatusMethodNotAllowed)
			if a := rec.Header().Get("Allow"); strings.Contains(a, m) || a == "" {
				t.Errorf("%s %s Allow = %q", m, p, a)
			}
		}
		big := bytes.Repeat([]byte(" "), dav.MaxXMLBytes+1)
		wantStatus(t, f.doPath(m, newURL, big, nil), http.StatusRequestEntityTooLarge)
	}
	// Properties in the body do not matter for an existing calendar: nothing changes.
	before, _ := f.getCal(t, objRef)
	wantStatus(t, f.doPath("MKCALENDAR", objBase, mkcalBody(`<D:displayname>Changed</D:displayname>`), nil), http.StatusMethodNotAllowed)
	after, _ := f.getCal(t, objRef)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("existing calendar changed: %+v -> %+v", before, after)
	}
}

func TestCreateConcurrent32(t *testing.T) {
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				count := runRace(32, func(i int) int {
					return f.create(m, newURL, fmt.Sprintf(`<D:displayname>n%d</D:displayname>`, i)).Code
				})
				if count[http.StatusCreated] != 1 || count[http.StatusMethodNotAllowed] != 31 {
					t.Errorf("codes = %v, want one 201 and 31 405", count)
				}
				list, _ := st.ListCollections(context.Background(), "alice", store.KindCalendar)
				if len(list) != 1 {
					t.Errorf("%d calendars", len(list))
				}
			})
		})
	}
}

func TestCreateMixedMethodsConcurrent(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		count := runRace(32, func(i int) int { return f.create(methods[i%2], newURL, "").Code })
		if count[http.StatusCreated] != 1 || count[http.StatusMethodNotAllowed] != 31 {
			t.Errorf("codes = %v", count)
		}
	})
}

func TestCreateUsesResolvedOwner(t *testing.T) {
	for _, m := range methods {
		st := memory.New()
		h, err := New(Config{Store: st, Prefix: "/dav", Resolve: func(_ *http.Request, u string) (string, error) {
			return "owner-" + u, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		var body []byte
		if m == "MKCOL" {
			body = mkcolBody(`<D:resourcetype><D:collection/><C:calendar/></D:resourcetype>`)
		}
		h.ServeHTTP(rec, httptest.NewRequest(m, "/dav/calendars/bob/b/", bytes.NewReader(body)))
		wantStatus(t, rec, http.StatusCreated)
		if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "owner-bob", Kind: store.KindCalendar, Name: "b"}); err != nil {
			t.Errorf("%s: not under the resolved owner: %v", m, err)
		}
		if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "bob", Kind: store.KindCalendar, Name: "b"}); err == nil {
			t.Errorf("%s: created under the URL user", m)
		}
		if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "owner-bob", Kind: store.KindAddressBook, Name: "b"}); err == nil {
			t.Errorf("%s: created as an address book", m)
		}
		// PROPPATCH and DELETE use the resolved owner too.
		rec = httptest.NewRecorder()
		req := httptest.NewRequest("PROPPATCH", "/dav/calendars/bob/b/", bytes.NewReader(setProps(`<D:displayname>B</D:displayname>`)))
		h.ServeHTTP(rec, req)
		wantStatus(t, rec, http.StatusMultiStatus)
		if c, _ := st.GetCollection(context.Background(), store.CollectionRef{Owner: "owner-bob", Kind: store.KindCalendar, Name: "b"}); c.Props.DisplayName != "B" {
			t.Errorf("%s: PROPPATCH did not reach the resolved owner: %+v", m, c.Props)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/dav/calendars/bob/b/", nil))
		wantStatus(t, rec, http.StatusNoContent)
		if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "owner-bob", Kind: store.KindCalendar, Name: "b"}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: calendar still there: %v", m, err)
		}
	}
}

func TestCreateStoreFailureDoesNotLeak(t *testing.T) {
	f := emptyFixture(t, memory.New())
	f.h.store = createFailStore{f.st}
	for _, m := range methods {
		rec := f.create(m, newURL, "")
		wantStatus(t, rec, http.StatusInternalServerError)
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("leaked %q", rec.Body.String())
		}
	}
}

type createFailStore struct{ store.Store }

func (createFailStore) CreateCollection(context.Context, store.CollectionRef, store.Props) (store.Collection, error) {
	return store.Collection{}, errSecret
}
func (createFailStore) UpdateCollection(context.Context, store.CollectionRef, store.PropsUpdate) (store.Collection, error) {
	return store.Collection{}, errSecret
}
func (createFailStore) DeleteCollection(context.Context, store.CollectionRef) error { return errSecret }

// patchFixture is a fixture whose calendar "work" has every property set.
func patchFixture(t *testing.T, st store.Store) *objFixture {
	t.Helper()
	f := newObjFixture(t, st, 1<<20)
	d, e, c, o, z := "Old", "Old desc", "#112233FF", 4, tzText
	if _, err := st.UpdateCollection(context.Background(), objRef, store.PropsUpdate{
		DisplayName: &d, Description: &e, Color: &c, Order: &o, Timezone: &z,
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func ctag(t *testing.T, f *objFixture) string {
	t.Helper()
	r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<CS:getctag/>`)), hdrs{"Depth": "0"}))
	return r.must(t, 0, dav.GetCTag).Text
}

func TestProppatchSetAndRemove(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := patchFixture(t, st)
		before := ctag(t, f)
		rec := f.doPath("PROPPATCH", objBase, patchBody(
			`<D:set><D:prop><D:displayname>New</D:displayname><A:calendar-color>#ABCDEF</A:calendar-color>`+
				`<A:calendar-order>9</A:calendar-order></D:prop></D:set>`+
				`<D:remove><D:prop><C:calendar-description/><C:calendar-timezone/></D:prop></D:remove>`), nil)
		r := parseReply(t, rec)
		if len(r.hrefs) != 1 || r.hrefs[0] != objBase {
			t.Fatalf("hrefs = %v", r.hrefs)
		}
		if len(r.props[0]) != 1 || len(r.props[0][200]) != 5 {
			t.Fatalf("props = %v", r.props)
		}
		for _, n := range []xml.Name{dav.DisplayName, dav.CalendarColor, dav.CalendarOrder, dav.CalendarDescription, dav.CalendarTimezone} {
			if r.find(t, 0, 200, n) == nil {
				t.Errorf("missing 200 prop %v", n)
			}
		}
		c, _ := f.getCal(t, objRef)
		want := store.Props{DisplayName: "New", Color: "#ABCDEF", Order: 9}
		if !reflect.DeepEqual(c.Props, want) {
			t.Errorf("props = %+v, want %+v", c.Props, want)
		}
		if after := ctag(t, f); after == before {
			t.Errorf("ctag unchanged: %s", after)
		}
		g := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/><A:calendar-color/><A:calendar-order/>`)), hdrs{"Depth": "0"}))
		if g.must(t, 0, dav.DisplayName).Text != "New" || g.must(t, 0, dav.CalendarColor).Text != "#ABCDEF" || g.must(t, 0, dav.CalendarOrder).Text != "9" {
			t.Errorf("PROPFIND after PROPPATCH: %v", g.props[0])
		}
	})
}

func TestProppatchRemoveEverything(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := patchFixture(t, st)
		wantStatus(t, f.doPath("PROPPATCH", objBase, patchBody(`<D:remove><D:prop><D:displayname/><C:calendar-description/>`+
			`<A:calendar-color/><A:calendar-order/><C:calendar-timezone/></D:prop></D:remove>`), nil), http.StatusMultiStatus)
		c, _ := f.getCal(t, objRef)
		if !reflect.DeepEqual(c.Props, store.Props{}) {
			t.Errorf("props = %+v", c.Props)
		}
		r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/><A:calendar-order/><A:calendar-color/>`)), hdrs{"Depth": "0"}))
		if got := r.must(t, 0, dav.DisplayName).Text; got != "work" {
			t.Errorf("displayname = %q, want the fallback", got)
		}
		if got := r.must(t, 0, dav.CalendarOrder).Text; got != "0" {
			t.Errorf("order = %q, want 0", got)
		}
		if r.find(t, 0, 404, dav.CalendarColor) == nil {
			t.Errorf("colour should be gone: %v", r.props[0])
		}
	})
}

func TestProppatchSetOrderZero(t *testing.T) {
	f := patchFixture(t, memory.New())
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(`<A:calendar-order>0</A:calendar-order>`), nil), http.StatusMultiStatus)
	if c, _ := f.getCal(t, objRef); c.Props.Order != 0 {
		t.Errorf("order = %d", c.Props.Order)
	}
}

// Apple Calendar renames and recolours a calendar straight after creating it.
func TestProppatchRightAfterMkcalendar(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		wantStatus(t, f.doPath("MKCALENDAR", newURL, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<B:mkcalendar xmlns:B="urn:ietf:params:xml:ns:caldav"><A:set xmlns:A="DAV:"><A:prop>
<B:supported-calendar-component-set><B:comp name="VEVENT"/></B:supported-calendar-component-set>
<A:displayname>untitled</A:displayname></A:prop></A:set></B:mkcalendar>`), nil), http.StatusCreated)
		before := ctagAt(t, f, newURL)
		rec := f.doPath("PROPPATCH", newURL, []byte(`<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:"><A:set><A:prop>
<D:calendar-color xmlns:D="http://apple.com/ns/ical/" symbolic-color="purple">#711A76FF</D:calendar-color>
<A:displayname>Holidays</A:displayname>
</A:prop></A:set></A:propertyupdate>`), nil)
		r := parseReply(t, rec)
		if len(r.props[0][200]) != 2 {
			t.Fatalf("props = %v", r.props[0])
		}
		c, _ := f.getCal(t, newRef)
		if c.Props.DisplayName != "Holidays" || c.Props.Color != "#711A76FF" || !slices.Equal(c.Props.Components, []string{"VEVENT"}) {
			t.Errorf("props = %+v", c.Props)
		}
		if ctagAt(t, f, newURL) == before {
			t.Error("ctag unchanged")
		}
	})
}

func ctagAt(t *testing.T, f *objFixture, path string) string {
	t.Helper()
	r := parseReply(t, f.doPath("PROPFIND", path, []byte(propBody(`<CS:getctag/>`)), hdrs{"Depth": "0"}))
	return r.must(t, 0, dav.GetCTag).Text
}

// updateCountingStore counts UpdateCollection calls.
type updateCountingStore struct {
	store.Store
	updates atomic.Int64
	last    store.PropsUpdate
	mu      sync.Mutex
}

func (u *updateCountingStore) UpdateCollection(ctx context.Context, ref store.CollectionRef, upd store.PropsUpdate) (store.Collection, error) {
	u.updates.Add(1)
	u.mu.Lock()
	u.last = upd
	u.mu.Unlock()
	return u.Store.UpdateCollection(ctx, ref, upd)
}

func TestProppatchIsOneUpdate(t *testing.T) {
	us := &updateCountingStore{Store: memory.New()}
	f := patchFixture(t, us)
	us.updates.Store(0)
	wantStatus(t, f.doPath("PROPPATCH", objBase, patchBody(
		`<D:set><D:prop><D:displayname>A</D:displayname><A:calendar-color>#000000</A:calendar-color><A:calendar-order>1</A:calendar-order></D:prop></D:set>`+
			`<D:remove><D:prop><C:calendar-description/></D:prop></D:remove>`), nil), http.StatusMultiStatus)
	if n := us.updates.Load(); n != 1 {
		t.Errorf("UpdateCollection called %d times, want 1", n)
	}
	if us.last.Components != nil || us.last.Timezone != nil {
		t.Errorf("untouched properties were part of the update: %+v", us.last)
	}
	if us.last.DisplayName == nil || us.last.Description == nil || us.last.Color == nil || us.last.Order == nil {
		t.Errorf("update = %+v", us.last)
	}
	// A denied request never reaches the store.
	us.updates.Store(0)
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(`<D:displayname>B</D:displayname><X:y>1</X:y>`), nil), http.StatusMultiStatus)
	if n := us.updates.Load(); n != 0 {
		t.Errorf("a denied PROPPATCH called UpdateCollection %d times", n)
	}
}

func TestProppatchDenied(t *testing.T) {
	tests := []struct {
		name   string
		blocks string
		denied []string
		failed []string
	}{
		{"protected beside valid",
			`<D:set><D:prop><D:displayname>X</D:displayname><CS:getctag>9</CS:getctag></D:prop></D:set>`,
			[]string{"getctag"}, []string{"displayname"}},
		{"component set",
			`<D:set><D:prop><C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set></D:prop></D:set>`,
			[]string{"supported-calendar-component-set"}, nil},
		{"component set beside valid",
			`<D:set><D:prop><D:displayname>X</D:displayname><C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>` +
				`<A:calendar-color>#000000</A:calendar-color></D:prop></D:set>`,
			[]string{"supported-calendar-component-set"}, []string{"displayname", "calendar-color"}},
		{"component set removed",
			`<D:remove><D:prop><C:supported-calendar-component-set/></D:prop></D:remove>`,
			[]string{"supported-calendar-component-set"}, nil},
		{"resourcetype",
			`<D:set><D:prop><D:resourcetype><C:calendar/></D:resourcetype><C:calendar-description>X</C:calendar-description></D:prop></D:set>`,
			[]string{"resourcetype"}, []string{"calendar-description"}},
		{"foreign property",
			`<D:set><D:prop><X:thing>1</X:thing><D:displayname>X</D:displayname></D:prop></D:set><D:remove><D:prop><C:calendar-description/></D:prop></D:remove>`,
			[]string{"thing"}, []string{"displayname", "calendar-description"}},
		{"sync-token", `<D:remove><D:prop><D:sync-token/></D:prop></D:remove>`, []string{"sync-token"}, nil},
		{"max-resource-size", `<D:set><D:prop><C:max-resource-size>1</C:max-resource-size></D:prop></D:set>`, []string{"max-resource-size"}, nil},
		{"too long name",
			`<D:set><D:prop><D:displayname>` + strings.Repeat("x", 256) + `</D:displayname><C:calendar-description>ok</C:calendar-description></D:prop></D:set>`,
			[]string{"displayname"}, []string{"calendar-description"}},
		{"colour red", `<D:set><D:prop><A:calendar-color>red</A:calendar-color><D:displayname>ok</D:displayname></D:prop></D:set>`,
			[]string{"calendar-color"}, []string{"displayname"}},
		{"colour short", `<D:set><D:prop><A:calendar-color>#12345</A:calendar-color></D:prop></D:set>`, []string{"calendar-color"}, nil},
		{"colour not hex", `<D:set><D:prop><A:calendar-color>#GGGGGG</A:calendar-color></D:prop></D:set>`, []string{"calendar-color"}, nil},
		{"colour huge", `<D:set><D:prop><A:calendar-color>#` + strings.Repeat("a", 100000) + `</A:calendar-color></D:prop></D:set>`, []string{"calendar-color"}, nil},
		{"order text", `<D:set><D:prop><A:calendar-order>abc</A:calendar-order><D:displayname>ok</D:displayname></D:prop></D:set>`,
			[]string{"calendar-order"}, []string{"displayname"}},
		{"order big", `<D:set><D:prop><A:calendar-order>1e9</A:calendar-order></D:prop></D:set>`, []string{"calendar-order"}, nil},
		{"order out of range", `<D:set><D:prop><A:calendar-order>1000001</A:calendar-order></D:prop></D:set>`, []string{"calendar-order"}, nil},
		{"timezone not ical", `<D:set><D:prop><C:calendar-timezone>nope</C:calendar-timezone><D:displayname>ok</D:displayname></D:prop></D:set>`,
			[]string{"calendar-timezone"}, []string{"displayname"}},
		{"timezone without vtimezone",
			`<D:set><D:prop><C:calendar-timezone>` + noZone + `</C:calendar-timezone></D:prop></D:set>`,
			[]string{"calendar-timezone"}, nil},
		{"description with child",
			`<D:set><D:prop><C:calendar-description>a<X:b>c</X:b></C:calendar-description><D:displayname>ok</D:displayname></D:prop></D:set>`,
			[]string{"calendar-description"}, []string{"displayname"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := patchFixture(t, st)
				before, _ := f.getCal(t, objRef)
				rec := f.doPath("PROPPATCH", objBase, patchBody(tt.blocks), nil)
				r := parseReply(t, rec)
				stats := r.props[0]
				got := func(code int) []string {
					var out []string
					for _, n := range stats[code] {
						out = append(out, n.Name.Local)
					}
					return out
				}
				if !reflect.DeepEqual(got(403), tt.denied) || !reflect.DeepEqual(got(424), tt.failed) || len(stats[200]) != 0 {
					t.Errorf("403 = %v, 424 = %v, 200 = %v", got(403), got(424), got(200))
				}
				body := rec.Body.String()
				if len(tt.failed) > 0 && strings.Index(body, "403") > strings.Index(body, "424") {
					t.Errorf("propstat order: %s", body)
				}
				if strings.Contains(body, ":error") {
					t.Errorf("unexpected error element: %s", body)
				}
				after, _ := f.getCal(t, objRef)
				if !reflect.DeepEqual(before, after) {
					t.Errorf("store changed (including revision):\nbefore %+v\nafter  %+v", before, after)
				}
			})
		})
	}
}

func TestProppatchBadRequests(t *testing.T) {
	tests := map[string][]byte{
		"empty body":    nil,
		"malformed":     []byte("<D:propertyupdate"),
		"wrong root":    []byte(`<a xmlns="DAV:"/>`),
		"empty update":  patchBody(``),
		"empty set":     patchBody(`<D:set><D:prop/></D:set>`),
		"set without p": patchBody(`<D:set/>`),
		"doctype":       []byte(`<!DOCTYPE x [<!ENTITY a "b">]><D:propertyupdate xmlns:D="DAV:"/>`),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			f := patchFixture(t, memory.New())
			before, _ := f.getCal(t, objRef)
			wantStatus(t, f.doPath("PROPPATCH", objBase, body, nil), http.StatusBadRequest)
			after, _ := f.getCal(t, objRef)
			if !reflect.DeepEqual(before, after) {
				t.Error("store changed")
			}
		})
	}
}

func TestProppatchMissingHomeObject(t *testing.T) {
	f := patchFixture(t, memory.New())
	body := setProps(`<D:displayname>X</D:displayname>`)
	wantStatus(t, f.doPath("PROPPATCH", "/dav/calendars/alice/nope/", body, nil), http.StatusNotFound)
	// Even a request that would fail is 404 for a missing calendar.
	wantStatus(t, f.doPath("PROPPATCH", "/dav/calendars/alice/nope/", setProps(`<X:y>1</X:y>`), nil), http.StatusNotFound)
	wantStatus(t, f.doPath("PROPPATCH", homeURL, body, nil), http.StatusMethodNotAllowed)
	wantStatus(t, f.doPath("PROPPATCH", objBase+"x.ics", body, nil), http.StatusMethodNotAllowed)
}

func TestProppatchOnlyCalendarsOfOwnKind(t *testing.T) {
	st := memory.New()
	f := emptyFixture(t, st)
	book := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "new"}
	if _, err := st.CreateCollection(context.Background(), book, store.Props{}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.doPath("PROPPATCH", newURL, setProps(`<D:displayname>X</D:displayname>`), nil), http.StatusNotFound)
	wantStatus(t, f.doPath("DELETE", newURL, nil, nil), http.StatusNotFound)
	if _, err := st.GetCollection(context.Background(), book); err != nil {
		t.Errorf("address book touched: %v", err)
	}
}

func TestProppatchTrimsAndBlankIsUnset(t *testing.T) {
	f := patchFixture(t, memory.New())
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(
		"<D:displayname> \n </D:displayname><C:calendar-description>  New desc </C:calendar-description><A:calendar-color> #AABBCC\n</A:calendar-color>"), nil), http.StatusMultiStatus)
	c, _ := f.getCal(t, objRef)
	if c.Props.DisplayName != "" || c.Props.Description != "New desc" || c.Props.Color != "#AABBCC" {
		t.Errorf("props = %+v", c.Props)
	}
	r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/>`)), hdrs{"Depth": "0"}))
	if got := r.must(t, 0, dav.DisplayName).Text; got != "work" {
		t.Errorf("displayname = %q, want the fallback", got)
	}
	// An empty time zone clears it.
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(`<C:calendar-timezone/>`), nil), http.StatusMultiStatus)
	if c, _ := f.getCal(t, objRef); c.Props.Timezone != "" {
		t.Errorf("timezone = %q", c.Props.Timezone)
	}
}

func TestProppatchTimezone(t *testing.T) {
	f := newObjFixture(t, memory.New(), 1<<20)
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(`<C:calendar-timezone>`+tzText+`</C:calendar-timezone>`), nil), http.StatusMultiStatus)
	if c, _ := f.getCal(t, objRef); c.Props.Timezone != tzText {
		t.Errorf("timezone = %q", c.Props.Timezone)
	}
	r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<C:calendar-timezone/>`)), hdrs{"Depth": "0"}))
	if got := r.must(t, 0, dav.CalendarTimezone).Text; got != tzText {
		t.Errorf("PROPFIND timezone = %q", got)
	}
}

func TestProppatchDuplicateNamesLastWinsListedOnce(t *testing.T) {
	f := patchFixture(t, memory.New())
	rec := f.doPath("PROPPATCH", objBase, patchBody(
		`<D:set><D:prop><D:displayname>One</D:displayname><C:calendar-description>D1</C:calendar-description></D:prop></D:set>`+
			`<D:set><D:prop><D:displayname>Two</D:displayname><A:calendar-order>1</A:calendar-order></D:prop></D:set>`+
			`<D:remove><D:prop><C:calendar-description/></D:prop></D:remove>`+
			`<D:set><D:prop><A:calendar-order>2</A:calendar-order></D:prop></D:set>`), nil)
	r := parseReply(t, rec)
	if n := len(r.props[0][200]); n != 3 {
		t.Errorf("200 propstat lists %d properties, want 3: %v", n, r.props[0][200])
	}
	c, _ := f.getCal(t, objRef)
	if c.Props.DisplayName != "Two" || c.Props.Description != "" || c.Props.Order != 2 {
		t.Errorf("props = %+v", c.Props)
	}
	// A denied duplicate is listed once and the property is not also "dependent".
	rec = f.doPath("PROPPATCH", objBase, patchBody(
		`<D:set><D:prop><D:displayname>`+strings.Repeat("x", 256)+`</D:displayname></D:prop></D:set>`+
			`<D:set><D:prop><D:displayname>fine</D:displayname></D:prop></D:set>`), nil)
	r = parseReply(t, rec)
	if len(r.props[0][403]) != 1 || len(r.props[0][424]) != 0 {
		t.Errorf("403 = %v, 424 = %v", r.props[0][403], r.props[0][424])
	}
}

func TestCtagChangesMonotonic(t *testing.T) {
	f := patchFixture(t, memory.New())
	a := ctag(t, f)
	wantStatus(t, f.doPath("PROPPATCH", objBase, setProps(`<D:displayname>Z</D:displayname>`), nil), 207)
	b := ctag(t, f)
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	if y <= x {
		t.Errorf("ctag %s -> %s", a, b)
	}
}

func TestDeleteCollection(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), http.StatusCreated)
		wantStatus(t, f.do("PUT", "b.ics", event("b"), icsType), http.StatusCreated)
		rec := f.doPath("DELETE", objBase, nil, hdrs{"If-Match": `"nothing"`, "Depth": "0"})
		wantStatus(t, rec, http.StatusNoContent)
		wantStatus(t, f.doPath("PROPFIND", objBase, nil, hdrs{"Depth": "0"}), http.StatusNotFound)
		wantStatus(t, f.do("GET", "a.ics", nil, nil), http.StatusNotFound)
		if _, err := st.GetCollection(context.Background(), objRef); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetCollection err = %v", err)
		}
		for _, n := range []string{"a.ics", "b.ics"} {
			if _, err := st.GetObject(context.Background(), objRef, n); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("GetObject(%s) err = %v", n, err)
			}
		}
		if list, _ := st.ListCollections(context.Background(), "alice", store.KindCalendar); len(list) != 0 {
			t.Errorf("calendars left: %v", list)
		}
		wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNotFound)
		// The name can be created again, empty.
		wantStatus(t, f.doPath("MKCALENDAR", objBase, nil, nil), http.StatusCreated)
		if objs, err := st.ListObjects(context.Background(), objRef, store.ListOptions{}); err != nil || len(objs) != 0 {
			t.Errorf("re-created calendar holds %d objects, err %v", len(objs), err)
		}
	})
}

// deleteCountingStore records DeleteCollection calls.
type deleteCountingStore struct {
	store.Store
	calls []store.CollectionRef
}

func (d *deleteCountingStore) DeleteCollection(ctx context.Context, ref store.CollectionRef) error {
	d.calls = append(d.calls, ref)
	return d.Store.DeleteCollection(ctx, ref)
}

func TestDeleteCollectionCallsTheStore(t *testing.T) {
	ds := &deleteCountingStore{Store: memory.New()}
	f := newObjFixture(t, ds, 1<<20)
	wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNoContent)
	if len(ds.calls) != 1 || ds.calls[0] != objRef {
		t.Errorf("DeleteCollection calls = %v", ds.calls)
	}
}

func TestDeleteLeavesOtherCalendars(t *testing.T) {
	f := memFixture(t)
	ctx := context.Background()
	other := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "keep"}
	otherUser := store.CollectionRef{Owner: "bob", Kind: store.KindCalendar, Name: "work"}
	for _, ref := range []store.CollectionRef{other, otherUser} {
		if _, err := f.st.CreateCollection(ctx, ref, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.PutObject(ctx, ref, "k.ics", event("k"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNoContent)
	for _, ref := range []store.CollectionRef{other, otherUser} {
		if _, err := f.st.GetObject(ctx, ref, "k.ics"); err != nil {
			t.Errorf("%v lost an object: %v", ref, err)
		}
	}
}

func TestDeleteCollectionHomeIs405(t *testing.T) {
	f := memFixture(t)
	rec := f.doPath("DELETE", homeURL, nil, nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	if strings.Contains(rec.Header().Get("Allow"), "DELETE") {
		t.Errorf("Allow = %q", rec.Header().Get("Allow"))
	}
}

func TestDeleteLargeCalendar(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	eachStore(t, func(t *testing.T, st store.Store) {
		// The file store syncs every write, so it gets a smaller calendar.
		n := 3000
		if _, ok := st.(*memory.Store); !ok {
			n = 300
		}
		f := newObjFixture(t, st, 1<<20)
		ctx := context.Background()
		other := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "keep"}
		if _, err := st.CreateCollection(ctx, other, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutObject(ctx, other, "k.ics", event("k"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		for i := range n {
			if _, err := st.PutObject(ctx, objRef, fmt.Sprintf("p%d.ics", i), event(fmt.Sprint("u", i)), store.PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNoContent)
		wantStatus(t, f.doPath("PROPFIND", objBase, nil, hdrs{"Depth": "0"}), http.StatusNotFound)
		wantStatus(t, f.do("GET", fmt.Sprintf("p%d.ics", n-1), nil, nil), http.StatusNotFound)
		if _, err := st.GetCollection(ctx, objRef); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetCollection err = %v", err)
		}
		if _, err := st.GetObject(ctx, objRef, "p0.ics"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetObject err = %v", err)
		}
		if _, err := st.GetObject(ctx, other, "k.ics"); err != nil {
			t.Errorf("another calendar lost an object: %v", err)
		}
		wantStatus(t, f.doPath("MKCALENDAR", objBase, nil, nil), http.StatusCreated)
		objs, err := st.ListObjects(ctx, objRef, store.ListOptions{})
		if err != nil || len(objs) != 0 {
			t.Errorf("re-created calendar holds %d objects, err %v", len(objs), err)
		}
	})
}

func TestProppatchRacesDelete(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		for round := range 10 {
			f := newObjFixture(t, st, 1<<20)
			wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), http.StatusCreated)
			body := setProps(fmt.Sprintf(`<D:displayname>R%d</D:displayname>`, round))
			var wg sync.WaitGroup
			start := make(chan struct{})
			var patchCodes [4]int
			delCode := 0
			for i := range patchCodes {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					patchCodes[i] = f.doPath("PROPPATCH", objBase, body, nil).Code
				}()
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				delCode = f.doPath("DELETE", objBase, nil, nil).Code
			}()
			close(start)
			wg.Wait()
			if delCode != http.StatusNoContent {
				t.Errorf("round %d: DELETE = %d", round, delCode)
			}
			for _, c := range patchCodes {
				if c != http.StatusMultiStatus && c != http.StatusNotFound {
					t.Errorf("round %d: PROPPATCH = %d", round, c)
				}
			}
			wantStatus(t, f.doPath("PROPFIND", objBase, nil, hdrs{"Depth": "0"}), http.StatusNotFound)
		}
	})
}
