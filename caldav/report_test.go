// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

const (
	calURL = "/dav/calendars/alice/work/"
	nsDecl = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"`
)

var workRef = store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}

// report sends a REPORT with a raw body.
func (f *fixture) report(target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("REPORT", target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// put writes an object straight to the store, bypassing validation.
func (f *fixture) put(t *testing.T, name, data string) {
	t.Helper()
	if _, err := f.mem.PutObject(context.Background(), workRef, name, []byte(data), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func bases(r reply) []string {
	var out []string
	for _, h := range r.hrefs {
		out = append(out, path.Base(h))
	}
	return out
}

func wrapCal(blocks ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + strings.Join(blocks, "") + "END:VCALENDAR\r\n"
}

// vev builds a VEVENT block; the UID is e1 unless a line sets one.
func vev(lines ...string) string {
	uid := "UID:e1\r\n"
	for _, l := range lines {
		if strings.HasPrefix(l, "UID:") {
			uid = ""
		}
	}
	return "BEGIN:VEVENT\r\n" + uid + "DTSTAMP:20260101T000000Z\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

const (
	prague = "TZID=Europe/Prague"
	winTZ  = "TZID=W. Europe Standard Time"
)

const pragueVTZ = "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

func cdate(y int, m time.Month, d, h, mi int) string {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC).Format("20060102T150405Z")
}

func trAttrs(start, end string) string {
	var b strings.Builder
	if start != "" {
		b.WriteString(` start="` + start + `"`)
	}
	if end != "" {
		b.WriteString(` end="` + end + `"`)
	}
	return b.String()
}

// timeQuery is a VEVENT time-range calendar-query asking for the ETag.
func timeQuery(start, end string) string {
	return query(`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range` +
		trAttrs(start, end) + `/></C:comp-filter></C:comp-filter></C:filter>`)
}

func query(inner string) string {
	return `<?xml version="1.0"?><C:calendar-query ` + nsDecl + `><D:prop><D:getetag/></D:prop>` + inner + `</C:calendar-query>`
}

// evFilter wraps conditions in VCALENDAR/VEVENT comp-filters.
func evFilter(inner string) string {
	return `<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` + inner +
		`</C:comp-filter></C:comp-filter></C:filter>`
}

func propFilter(name, attrs, inner string) string {
	return `<C:prop-filter name="` + name + `" ` + attrs + `>` + inner + `</C:prop-filter>`
}

func textMatch(attrs, v string) string {
	return `<C:text-match ` + attrs + `>` + v + `</C:text-match>`
}

func multiget(inner string) string {
	return `<?xml version="1.0"?><C:calendar-multiget ` + nsDecl + `><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		inner + `</C:calendar-multiget>`
}

func hrefEl(h string) string { return `<D:href>` + h + `</D:href>` }

// precondition returns the name of the single precondition element of an
// error response.
func precondition(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	root, err := dav.ParseXML(rec.Body.Bytes())
	if err != nil || len(root.Children) != 1 {
		return rec.Body.String()
	}
	return root.Children[0].Name.Local
}

// TestQueryEdgeCases runs the iCalendar edge-case table through the HTTP
// layer: one stored object, one UTC window.
func TestQueryEdgeCases(t *testing.T) {
	at := func(y int, m time.Month, d, h, mi int) string { return cdate(y, m, d, h, mi) }
	weekly := []string{
		"DTSTART;" + prague + ":20260316T090000",
		"DTEND;" + prague + ":20260316T100000",
		"RRULE:FREQ=WEEKLY;BYDAY=MO",
	}
	exdated := vev(append(slices.Clone(weekly), "EXDATE;"+prague+":20260323T090000")...)
	moved := vev(weekly...) + vev("RECURRENCE-ID;"+prague+":20260323T090000",
		"DTSTART;"+prague+":20260324T140000", "DTEND;"+prague+":20260324T150000")
	allDay := vev("DTSTART;VALUE=DATE:20260310", "DTEND;VALUE=DATE:20260311")
	floating := vev("DTSTART:20260310T090000", "DTEND:20260310T100000")
	count3 := vev(weekly[0], weekly[1], "RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3")
	daily := vev("DTSTART;"+prague+":20260327T090000", "DTEND;"+prague+":20260327T100000", "RRULE:FREQ=DAILY")
	until := vev("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
		"RRULE:FREQ=DAILY;UNTIL=20260319T080000Z")
	duration := vev("DTSTART:20260310T090000Z", "DURATION:PT1H30M")
	noEnd := vev("DTSTART;" + prague + ":20260310T090000")
	day31 := vev("DTSTART;"+prague+":20260131T090000", "DURATION:PT1H", "RRULE:FREQ=MONTHLY;BYMONTHDAY=31")
	hour := vev("DTSTART:20260310T090000Z", "DTEND:20260310T100000Z")
	long := vev("DTSTART:20260310T090000Z", "DTEND:20260315T090000Z")
	threeDays := vev("DTSTART;VALUE=DATE:20260302", "DTEND;VALUE=DATE:20260305", "RRULE:FREQ=WEEKLY;COUNT=3")
	unknownTZ := vev("DTSTART;TZID=Custom/Unknown:20260310T090000", "DTEND;TZID=Custom/Unknown:20260310T100000")
	windows := vev("DTSTART;"+winTZ+":20260310T090000", "DTEND;"+winTZ+":20260310T100000")
	windowsVTZ := strings.Replace(pragueVTZ, "TZID:Europe/Prague", "TZID:W. Europe Standard Time", 1)

	tests := []struct {
		name       string
		data       string
		start, end string
		want       bool
		needZoneDB bool
	}{
		{"weekly EXDATE removes the date", wrapCal(pragueVTZ, exdated), at(2026, 3, 23, 8, 0), at(2026, 3, 23, 9, 0), false, false},
		{"weekly EXDATE neighbour", wrapCal(pragueVTZ, exdated), at(2026, 3, 30, 7, 0), at(2026, 3, 30, 8, 0), true, false},
		{"moved occurrence at its new time", wrapCal(pragueVTZ, moved), at(2026, 3, 24, 13, 0), at(2026, 3, 24, 14, 0), true, false},
		{"moved occurrence not at its old time", wrapCal(pragueVTZ, moved), at(2026, 3, 23, 8, 0), at(2026, 3, 23, 9, 0), false, false},
		{"weekly across DST, summer time", wrapCal(pragueVTZ, vev(weekly...)), at(2026, 3, 30, 7, 0), at(2026, 3, 30, 8, 0), true, false},
		{"weekly across DST, not the winter hour", wrapCal(pragueVTZ, vev(weekly...)), at(2026, 3, 30, 8, 0), at(2026, 3, 30, 9, 0), false, false},
		{"all-day on its day", wrapCal(allDay), at(2026, 3, 10, 0, 0), at(2026, 3, 11, 0, 0), true, false},
		{"all-day not the next day", wrapCal(allDay), at(2026, 3, 11, 0, 0), at(2026, 3, 12, 0, 0), false, false},
		{"floating read as UTC", wrapCal(floating), at(2026, 3, 10, 8, 30), at(2026, 3, 10, 9, 30), true, false},
		{"floating is not Prague", wrapCal(floating), at(2026, 3, 10, 7, 30), at(2026, 3, 10, 8, 30), false, false},
		{"COUNT third", wrapCal(pragueVTZ, count3), at(2026, 3, 30, 7, 0), at(2026, 3, 30, 8, 0), true, false},
		{"COUNT fourth", wrapCal(pragueVTZ, count3), at(2026, 4, 6, 7, 0), at(2026, 4, 6, 8, 0), false, false},
		{"DST day", wrapCal(pragueVTZ, daily), at(2026, 3, 29, 7, 0), at(2026, 3, 29, 7, 10), true, false},
		{"DST day not an hour later", wrapCal(pragueVTZ, daily), at(2026, 3, 29, 8, 10), at(2026, 3, 29, 8, 20), false, false},
		{"UNTIL in UTC, last day", wrapCal(pragueVTZ, until), at(2026, 3, 19, 8, 0), at(2026, 3, 19, 9, 0), true, false},
		{"UNTIL in UTC, next day", wrapCal(pragueVTZ, until), at(2026, 3, 20, 0, 0), at(2026, 3, 21, 0, 0), false, false},
		{"DURATION inside", wrapCal(duration), at(2026, 3, 10, 10, 0), at(2026, 3, 10, 10, 10), true, false},
		{"DURATION after", wrapCal(duration), at(2026, 3, 10, 10, 30), at(2026, 3, 10, 11, 0), false, false},
		{"no DTEND around the instant", wrapCal(pragueVTZ, noEnd), at(2026, 3, 10, 7, 59), at(2026, 3, 10, 8, 1), true, false},
		{"no DTEND window ends at the start", wrapCal(pragueVTZ, noEnd), at(2026, 3, 10, 7, 0), at(2026, 3, 10, 8, 0), false, false},
		{"day 31 skips February", wrapCal(pragueVTZ, day31), at(2026, 2, 1, 0, 0), at(2026, 3, 1, 0, 0), false, false},
		{"day 31 in March", wrapCal(pragueVTZ, day31), at(2026, 3, 31, 7, 0), at(2026, 3, 31, 8, 0), true, false},
		{"starts at the window start", wrapCal(hour), at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), true, false},
		{"ends at the window start", wrapCal(hour), at(2026, 3, 10, 10, 0), at(2026, 3, 10, 11, 0), false, false},
		{"starts at the window end", wrapCal(hour), at(2026, 3, 10, 8, 0), at(2026, 3, 10, 9, 0), false, false},
		{"window inside a long occurrence", wrapCal(long), at(2026, 3, 12, 10, 0), at(2026, 3, 12, 11, 0), true, false},
		{"three-day all-day, second day", wrapCal(threeDays), at(2026, 3, 10, 10, 0), at(2026, 3, 10, 11, 0), true, false},
		{"three-day all-day, end exclusive", wrapCal(threeDays), at(2026, 3, 12, 0, 0), at(2026, 3, 13, 0, 0), false, false},
		{"three-day all-day, second week", wrapCal(threeDays), at(2026, 3, 10, 0, 0), at(2026, 3, 11, 0, 0), true, false},
		{"unknown TZID as UTC", wrapCal(unknownTZ), at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), true, false},
		{"unknown TZID is not Prague", wrapCal(unknownTZ), at(2026, 3, 10, 8, 0), at(2026, 3, 10, 9, 0), false, false},
		{"Windows TZID with VTIMEZONE", wrapCal(windowsVTZ, windows), at(2026, 3, 10, 8, 0), at(2026, 3, 10, 8, 30), true, false},
		{"Windows TZID with VTIMEZONE is not UTC", wrapCal(windowsVTZ, windows), at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), false, false},
		{"Windows TZID without VTIMEZONE", wrapCal(windows), at(2026, 3, 10, 8, 0), at(2026, 3, 10, 8, 30), true, true},
		{"Windows TZID without VTIMEZONE is not UTC", wrapCal(windows), at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), false, true},
		{"open start", wrapCal(hour), "", at(2026, 3, 10, 9, 1), true, false},
		{"open end", wrapCal(hour), at(2026, 3, 10, 9, 59), "", true, false},
		{"open end after the event", wrapCal(hour), at(2026, 3, 10, 10, 0), "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needZoneDB {
				if _, err := time.LoadLocation("Europe/Berlin"); err != nil {
					t.Skip("no time zone database")
				}
			}
			f := newFixture(t)
			f.put(t, "e.ics", tt.data)
			r := parseReply(t, f.report(calURL, timeQuery(tt.start, tt.end)))
			got := slices.Equal(bases(r), []string{"e.ics"})
			if got != tt.want {
				t.Errorf("matched = %v (%v), want %v", got, bases(r), tt.want)
			}
		})
	}
}

func TestQueryClientBodies(t *testing.T) {
	f := newFixture(t)
	f.put(t, "march.ics", wrapCal(vev("UID:march", "DTSTART:20260310T090000Z", "DTEND:20260310T100000Z", "SUMMARY:Team meeting")))
	f.put(t, "april.ics", wrapCal("BEGIN:VEVENT\r\nUID:april\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260410T090000Z\r\nSUMMARY:Later\r\nEND:VEVENT\r\n"))
	f.put(t, "todo.ics", wrapCal("BEGIN:VTODO\r\nUID:todo\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:Buy milk\r\nEND:VTODO\r\n"))

	// Thunderbird: a month of events.
	thunderbird := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <C:filter>
    <C:comp-filter name="VCALENDAR">
      <C:comp-filter name="VEVENT">
        <C:time-range start="20260301T000000Z" end="20260401T000000Z"/>
      </C:comp-filter>
    </C:comp-filter>
  </C:filter>
</C:calendar-query>`
	r := parseReply(t, f.report(calURL, thunderbird))
	if !slices.Equal(bases(r), []string{"march.ics"}) {
		t.Fatalf("month: got %v", bases(r))
	}
	r.must(t, 0, dav.GetETag)
	if got := r.must(t, 0, dav.CalendarData).Text; !strings.Contains(got, "UID:march") || !strings.Contains(got, "\r\n") {
		t.Errorf("calendar-data = %q", got)
	}

	// UID lookup.
	uid := `<?xml version="1.0"?>
<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">
  <D:prop><D:getetag/><C:calendar-data/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
    <C:prop-filter name="UID"><C:text-match collation="i;octet">april</C:text-match></C:prop-filter>
  </C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`
	r = parseReply(t, f.report(calURL, uid))
	if !slices.Equal(bases(r), []string{"april.ics"}) {
		t.Fatalf("uid: got %v", bases(r))
	}

	// A VTODO query matches nothing in an event-only filter, and the VTODO
	// itself when asked for.
	todo := `<?xml version="1.0"?>
<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">
  <D:prop><D:getetag/></D:prop>
  <C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VTODO"><C:time-range start="20260301T000000Z" end="20260401T000000Z"/></C:comp-filter></C:comp-filter></C:filter>
</C:calendar-query>`
	r = parseReply(t, f.report(calURL, todo))
	if len(r.hrefs) != 0 {
		t.Errorf("VTODO time-range: got %v", bases(r))
	}
	todoByName := strings.Replace(todo, `<C:time-range start="20260301T000000Z" end="20260401T000000Z"/>`, "", 1)
	r = parseReply(t, f.report(calURL, todoByName))
	if !slices.Equal(bases(r), []string{"todo.ics"}) {
		t.Errorf("VTODO by name: got %v", bases(r))
	}

	// Apple: other prefixes, the calendar-data property, a prop-filter.
	apple := `<?xml version="1.0" encoding="UTF-8"?>
<A:calendar-query xmlns:A="urn:ietf:params:xml:ns:caldav" xmlns:B="DAV:">
  <B:prop><B:getetag/><A:calendar-data/></B:prop>
  <A:filter><A:comp-filter name="VCALENDAR"><A:comp-filter name="VEVENT">
    <A:prop-filter name="SUMMARY"><A:text-match>MEETING</A:text-match></A:prop-filter>
  </A:comp-filter></A:comp-filter></A:filter>
</A:calendar-query>`
	r = parseReply(t, f.report(calURL, apple))
	if !slices.Equal(bases(r), []string{"march.ics"}) {
		t.Fatalf("apple: got %v", bases(r))
	}
	r.must(t, 0, dav.CalendarData)

	// Expand, limit-recurrence-set and partial calendar-data are ignored.
	partial := `<C:calendar-query ` + nsDecl + `><D:prop><D:getetag/><C:calendar-data>
<C:expand start="20260301T000000Z" end="20260401T000000Z"/><C:comp name="VCALENDAR"><C:prop name="VERSION"/></C:comp>
<C:limit-recurrence-set start="20260301T000000Z" end="20260401T000000Z"/></C:calendar-data></D:prop>` +
		`<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="20260301T000000Z" end="20260401T000000Z"/></C:comp-filter></C:comp-filter></C:filter>` +
		`<C:timezone>BEGIN:VCALENDAR</C:timezone></C:calendar-query>`
	r = parseReply(t, f.report(calURL, partial))
	if len(r.hrefs) != 1 {
		t.Fatalf("partial: got %v", r.hrefs)
	}
	stored, _ := f.mem.GetObject(context.Background(), workRef, "march.ics")
	if r.must(t, 0, dav.CalendarData).Text != string(stored.Data) {
		t.Error("calendar-data is not the stored bytes")
	}
}

func TestQueryDefaultsToAllprop(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics")
	r := parseReply(t, f.report(calURL, `<C:calendar-query `+nsDecl+`/>`))
	if len(r.hrefs) != 2 {
		t.Fatalf("got %v", r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	if r.find(t, 0, 200, dav.CalendarData) != nil {
		t.Error("allprop returned calendar-data")
	}
	r = parseReply(t, f.report(calURL, `<C:calendar-query `+nsDecl+`><D:propname/></C:calendar-query>`))
	r.must(t, 0, dav.GetETag)
}

func TestQueryTextMatch(t *testing.T) {
	f := newFixture(t)
	f.put(t, "meeting.ics", wrapCal(vev("UID:m", "DTSTART:20260310T090000Z", "SUMMARY:Team meeting", "LOCATION:Room 1",
		"ATTENDEE;PARTSTAT=ACCEPTED;CN=Ada:mailto:ada@example.org", "ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@example.org")))
	f.put(t, "lunch.ics", wrapCal(vev("UID:l", "DTSTART:20260310T120000Z", "SUMMARY:Lunch")))
	f.put(t, "uber.ics", wrapCal(vev("UID:u", "DTSTART:20260310T150000Z", "SUMMARY:Überraschung")))
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{"default collation is case-insensitive", evFilter(propFilter("SUMMARY", "", textMatch("", "MEETING"))), []string{"meeting.ics"}},
		{"explicit ascii-casemap", evFilter(propFilter("SUMMARY", "", textMatch(`collation="i;ascii-casemap"`, "MEETING"))), []string{"meeting.ics"}},
		{"octet is case-sensitive", evFilter(propFilter("SUMMARY", "", textMatch(`collation="i;octet"`, "MEETING"))), nil},
		{"octet hit", evFilter(propFilter("SUMMARY", "", textMatch(`collation="i;octet"`, "meeting"))), []string{"meeting.ics"}},
		{"non-ASCII does not fold by default", evFilter(propFilter("SUMMARY", "", textMatch("", "überraschung"))), nil},
		{"non-ASCII same case", evFilter(propFilter("SUMMARY", "", textMatch("", "Überraschung"))), []string{"uber.ics"}},
		{"unicode-casemap folds non-ASCII", evFilter(propFilter("SUMMARY", "", textMatch(`collation="i;unicode-casemap"`, "überraschung"))), []string{"uber.ics"}},
		{"negate", evFilter(propFilter("SUMMARY", "", textMatch(`negate-condition="yes"`, "meeting"))), []string{"lunch.ics", "uber.ics"}},
		{"negate no", evFilter(propFilter("SUMMARY", "", textMatch(`negate-condition="no"`, "meeting"))), []string{"meeting.ics"}},
		{"contains, not equals", evFilter(propFilter("SUMMARY", "", textMatch(`match-type="equals"`, "meet"))), []string{"meeting.ics"}},
		{"param-filter PARTSTAT", evFilter(propFilter("ATTENDEE", "", `<C:param-filter name="PARTSTAT">`+textMatch("", "accepted")+`</C:param-filter>`)), []string{"meeting.ics"}},
		{"param-filter PARTSTAT miss", evFilter(propFilter("ATTENDEE", "", `<C:param-filter name="PARTSTAT">`+textMatch("", "declined")+`</C:param-filter>`)), nil},
		{"param-filter is-not-defined", evFilter(propFilter("ATTENDEE", "", `<C:param-filter name="CN"><C:is-not-defined/></C:param-filter>`)), []string{"meeting.ics"}},
		{"is-not-defined LOCATION", evFilter(propFilter("LOCATION", "", `<C:is-not-defined/>`)), []string{"lunch.ics", "uber.ics"}},
		{"prop exists", evFilter(propFilter("LOCATION", "", "")), []string{"meeting.ics"}},
		{"prop time-range", evFilter(propFilter("DTSTART", "", `<C:time-range start="20260310T110000Z" end="20260310T130000Z"/>`)), []string{"lunch.ics"}},
		{"comp is-not-defined", `<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VTODO"><C:is-not-defined/></C:comp-filter></C:comp-filter></C:filter>`, []string{"lunch.ics", "meeting.ics", "uber.ics"}},
		{"VCALENDAR only", `<C:filter><C:comp-filter name="VCALENDAR"/></C:filter>`, []string{"lunch.ics", "meeting.ics", "uber.ics"}},
		{"VCALENDAR is-not-defined", `<C:filter><C:comp-filter name="VCALENDAR"><C:is-not-defined/></C:comp-filter></C:filter>`, nil},
		{"empty filter", `<C:filter/>`, []string{"lunch.ics", "meeting.ics", "uber.ics"}},
		{"no filter", ``, []string{"lunch.ics", "meeting.ics", "uber.ics"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := parseReply(t, f.report(calURL, query(tt.filter)))
			if got := bases(r); len(got) != len(tt.want) || (len(got) > 0 && !slices.Equal(got, tt.want)) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryFilterErrors(t *testing.T) {
	f := newFixture(t, "a.ics")
	tm := func(inner string) string { return evFilter(propFilter("SUMMARY", "", inner)) }
	manyProps := evFilter(strings.Repeat(`<C:prop-filter name="X"/>`, 101))
	manyTM := tm(strings.Repeat(`<C:text-match>a</C:text-match>`, 101))
	manyParams := evFilter(propFilter("X", "", strings.Repeat(`<C:param-filter name="P"/>`, 101)))
	trs := strings.Repeat(`<C:comp-filter name="VEVENT"><C:time-range start="20260301T000000Z"/></C:comp-filter>`, 17)
	nest := func(n int) string {
		return `<C:filter>` + strings.Repeat(`<C:comp-filter name="VCALENDAR">`, n) + strings.Repeat(`</C:comp-filter>`, n) + `</C:filter>`
	}
	tests := []struct {
		name, filter string
		status       int
		pre          string
	}{
		{"unknown collation", tm(textMatch(`collation="i;bogus"`, "a")), 403, "supported-collation"},
		{"ascii-numeric is unsupported", tm(textMatch(`collation="i;ascii-numeric"`, "1")), 403, "supported-collation"},
		{"bad negate", tm(textMatch(`negate-condition="maybe"`, "a")), 403, "valid-filter"},
		{"outer filter not VCALENDAR", `<C:filter><C:comp-filter name="VEVENT"/></C:filter>`, 403, "valid-filter"},
		{"two outer filters", `<C:filter><C:comp-filter name="VCALENDAR"/><C:comp-filter name="VCALENDAR"/></C:filter>`, 403, "valid-filter"},
		{"unknown filter child", `<C:filter><C:bogus/></C:filter>`, 403, "valid-filter"},
		{"comp-filter without name", `<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter/></C:comp-filter></C:filter>`, 403, "valid-filter"},
		{"prop-filter without name", evFilter(`<C:prop-filter/>`), 403, "valid-filter"},
		{"param-filter without name", evFilter(propFilter("X", "", `<C:param-filter/>`)), 403, "valid-filter"},
		{"unknown comp child", evFilter(`<C:bogus/>`), 403, "valid-filter"},
		{"unknown prop child", tm(`<C:bogus/>`), 403, "valid-filter"},
		{"unknown param child", evFilter(propFilter("X", "", `<C:param-filter name="P"><C:bogus/></C:param-filter>`)), 403, "valid-filter"},
		{"is-not-defined with text-match", tm(`<C:is-not-defined/>` + textMatch("", "a")), 403, "valid-filter"},
		{"comp is-not-defined with props", `<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:is-not-defined/><C:prop-filter name="X"/></C:comp-filter></C:comp-filter></C:filter>`, 403, "valid-filter"},
		{"two text-matches in a prop-filter", tm(textMatch("", "a") + textMatch("", "b")), 403, "valid-filter"},
		{"two time-ranges in a comp-filter", evFilter(`<C:time-range start="20260301T000000Z"/><C:time-range start="20260301T000000Z"/>`), 403, "valid-filter"},
		{"time-range ends before it starts", evFilter(`<C:time-range start="20260302T000000Z" end="20260301T000000Z"/>`), 403, "valid-filter"},
		{"too many prop-filters", manyProps, 403, "valid-filter"},
		{"too many text-matches", manyTM, 403, "valid-filter"},
		{"too many param-filters", manyParams, 403, "valid-filter"},
		{"17 comp-filter time ranges", `<C:filter><C:comp-filter name="VCALENDAR">` + trs + `</C:comp-filter></C:filter>`, 403, "valid-filter"},
		{"17 nested comp-filters", nest(17), 403, "valid-filter"},
		{"time-range with no bound", evFilter(`<C:time-range/>`), 400, ""},
		{"time-range bad start", evFilter(`<C:time-range start="2026-03-01"/>`), 400, ""},
		{"time-range bad end", evFilter(`<C:time-range end="20260301"/>`), 400, ""},
		{"time-range local time", evFilter(`<C:time-range start="20260301T000000"/>`), 400, ""},
		{"time-range impossible date", evFilter(`<C:time-range start="20261301T000000Z"/>`), 400, ""},
		{"prop time-range bad", evFilter(propFilter("DTSTAMP", "", `<C:time-range start="x"/>`)), 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.report(calURL, query(tt.filter))
			if rec.Code != tt.status {
				t.Fatalf("status %d, want %d; %s", rec.Code, tt.status, rec.Body)
			}
			if tt.pre != "" {
				if got := precondition(t, rec); got != tt.pre {
					t.Errorf("precondition = %q, want %q", got, tt.pre)
				}
			}
		})
	}
	// Limits are inclusive.
	ok := evFilter(strings.Repeat(`<C:prop-filter name="X"/>`, 100))
	if rec := f.report(calURL, query(ok)); rec.Code != http.StatusMultiStatus {
		t.Errorf("100 prop-filters: status %d", rec.Code)
	}
	if rec := f.report(calURL, query(nest(16))); rec.Code != http.StatusMultiStatus {
		t.Errorf("16 nested comp-filters: status %d", rec.Code)
	}
}

func TestTextMatchSizeLimits(t *testing.T) {
	tm := func(n int) string { return `<C:text-match>` + strings.Repeat("a", n) + `</C:text-match>` }
	pf := func(inner string) string { return query(evFilter(propFilter("SUMMARY", "", inner))) }
	f := newFixture(t, "a.ics")
	check := func(name, body string, want int) {
		t.Helper()
		rec := f.report(calURL, body)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
		if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "valid-filter") {
			t.Errorf("%s: body %s", name, rec.Body)
		}
	}
	check("8 KiB value", pf(tm(8192)), http.StatusMultiStatus)
	check("8 KiB + 1 value", pf(tm(8193)), http.StatusForbidden)
	// One text-match per prop-filter; spread them to reach the total.
	many := func(n, size int) string {
		var b strings.Builder
		for range n {
			b.WriteString(propFilter("SUMMARY", "", tm(size)))
		}
		return query(evFilter(b.String()))
	}
	check("64 KiB total", many(8, 8192), http.StatusMultiStatus)
	check("64 KiB + 1 total", many(9, 7282), http.StatusForbidden)
	param := query(evFilter(propFilter("X", "", `<C:param-filter name="P">`+tm(8192)+`</C:param-filter>`)))
	check("param-filter value counts", param, http.StatusMultiStatus)
}

func TestQueryWithoutFilterReadsNoData(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics", "c.ics")
	r := parseReply(t, f.report(calURL, query("")))
	if len(r.hrefs) != 3 {
		t.Fatalf("got %v", r.hrefs)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("GetObject called %d times, want 0", n)
	}
	if n := f.st.dataLists.Load(); n != 0 {
		t.Errorf("listed with data %d times", n)
	}
	parseReply(t, f.report(calURL, query(`<C:filter/>`)))
	parseReply(t, f.report(calURL, query(`<C:filter><C:comp-filter name="VCALENDAR"/></C:filter>`)))
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("empty filters: GetObject called %d times, want 0", n)
	}
}

func TestQueryWithCalendarDataReadsEachCandidateOnce(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics", "c.ics")
	body := `<C:calendar-query ` + nsDecl + `><D:prop><D:getetag/><C:calendar-data/></D:prop>` +
		evFilter(propFilter("UID", "", "")) + `</C:calendar-query>`
	r := parseReply(t, f.report(calURL, body))
	if want := []string{"a.ics", "b.ics", "c.ics"}; !slices.Equal(bases(r), want) {
		t.Fatalf("got %v", bases(r))
	}
	if n := f.st.gets.Load(); n != 3 {
		t.Errorf("GetObject called %d times, want 3", n)
	}
	for i := range r.hrefs {
		if got := r.must(t, i, dav.CalendarData).Text; got != string(icsFor(bases(r)[i])) {
			t.Errorf("%s data = %q", r.hrefs[i], got)
		}
	}

	// Without a filter but with calendar-data, each object is read once and
	// an unreadable one is skipped.
	f.put(t, "dead.ics", string(icsFor("dead")))
	f.st.fail = map[string]bool{"dead.ics": true}
	f.st.gets.Store(0)
	body = `<C:calendar-query ` + nsDecl + `><D:prop><D:getetag/><C:calendar-data/></D:prop></C:calendar-query>`
	r = parseReply(t, f.report(calURL, body))
	if want := []string{"a.ics", "b.ics", "c.ics"}; !slices.Equal(bases(r), want) {
		t.Errorf("got %v, want %v", bases(r), want)
	}
	if n := f.st.gets.Load(); n != 4 {
		t.Errorf("GetObject called %d times, want 4", n)
	}
}

func TestQuerySkipsBadObjects(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics")
	f.put(t, "garbage.ics", "\x00\xff not a calendar at all")
	f.put(t, "dead.ics", string(icsFor("dead")))
	f.st.fail = map[string]bool{"dead.ics": true}
	var logs bytes.Buffer
	f.h.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	for name, flt := range map[string]string{
		"time-range":     evFilter(`<C:time-range start="20260101T000000Z" end="20270101T000000Z"/>`),
		"is-not-defined": evFilter(propFilter("LOCATION", "", `<C:is-not-defined/>`)),
	} {
		t.Run(name, func(t *testing.T) {
			r := parseReply(t, f.report(calURL, query(flt)))
			if want := []string{"a.ics", "b.ics"}; !slices.Equal(bases(r), want) {
				t.Errorf("got %v, want %v", bases(r), want)
			}
		})
	}
	out := logs.String()
	if !strings.Contains(out, "garbage.ics") || !strings.Contains(out, "dead.ics") || !strings.Contains(out, "level=WARN") {
		t.Errorf("warnings not logged by name:\n%s", out)
	}
	if strings.Contains(out, "not a calendar") || strings.Contains(out, "disk on fire") {
		t.Errorf("log leaks data or error text:\n%s", out)
	}
}

func TestQueryObjectCap(t *testing.T) {
	old := maxQueryObjects
	maxQueryObjects = 5
	t.Cleanup(func() { maxQueryObjects = old })

	f := newFixture(t)
	for i := range 8 {
		f.put(t, fmt.Sprintf("p%d.ics", i), string(icsFor(fmt.Sprint("p", i))))
	}
	for name, body := range map[string]string{
		"no filter": query(""),
		"filter":    query(evFilter(propFilter("UID", "", ""))),
	} {
		r := parseReply(t, f.report(calURL, body))
		resps := r.root.Kids(dav.ResponseEl)
		if len(resps) != 6 {
			t.Fatalf("%s: %d responses %v, want 5 objects and the 507", name, len(resps), r.hrefs)
		}
		last := resps[5]
		if last.Child(dav.Href).Text != calURL || !strings.Contains(last.Child(dav.Status).Text, "507") ||
			last.Child(dav.ErrorEl).Child(dav.NumberOfMatchesWithinLimits) == nil {
			t.Errorf("%s: last response is not the 507: %v", name, r.hrefs)
		}
	}
	maxQueryObjects = 8
	if r := parseReply(t, f.report(calURL, query(""))); len(r.hrefs) != 8 {
		t.Errorf("at the cap: %v", r.hrefs)
	}
}

func TestReportDuplicatePropertiesAnsweredOnce(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics", "c.ics")
	props := `<D:getetag/>` + strings.Repeat(`<C:calendar-data/>`, 20) + `<D:getetag/>`
	bodies := map[string]string{
		"query": `<C:calendar-query ` + nsDecl + `><D:prop>` + props + `</D:prop></C:calendar-query>`,
		"multiget": `<C:calendar-multiget ` + nsDecl + `><D:prop>` + props + `</D:prop>` +
			hrefEl("/dav/calendars/alice/work/a.ics") + hrefEl("/dav/calendars/alice/work/b.ics") +
			hrefEl("/dav/calendars/alice/work/c.ics") + `</C:calendar-multiget>`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			f.st.gets.Store(0)
			rec := f.report(calURL, body)
			r := parseReply(t, rec)
			if len(r.hrefs) != 3 {
				t.Fatalf("hrefs = %v", r.hrefs)
			}
			if n := strings.Count(rec.Body.String(), "getetag"); n != 3*2 {
				t.Errorf("%d getetag tags, want one element per response", n)
			}
			if n := f.st.gets.Load(); n > 3 {
				t.Errorf("GetObject called %d times for 3 objects", n)
			}
		})
	}
	inc := `<C:calendar-query ` + nsDecl + `><D:allprop/><D:include>` + strings.Repeat(`<C:calendar-data/>`, 20) + `</D:include></C:calendar-query>`
	r := parseReply(t, f.report(calURL, inc))
	for i := range r.hrefs {
		n := 0
		for _, p := range r.props[i][200] {
			if p.Name == dav.CalendarData {
				n++
			}
		}
		if n != 1 {
			t.Errorf("response %d has %d calendar-data, want 1", i, n)
		}
	}
}

func TestReportTooManyDistinctProperties(t *testing.T) {
	names := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, `<X:p%d xmlns:X="urn:x"/>`, i)
		}
		return b.String()
	}
	bodies := func(props string) map[string]string {
		return map[string]string{
			"query": `<C:calendar-query ` + nsDecl + `><D:prop>` + props + `</D:prop></C:calendar-query>`,
			"multiget": `<C:calendar-multiget ` + nsDecl + `><D:prop>` + props + `</D:prop>` +
				hrefEl("/dav/calendars/alice/work/a.ics") + `</C:calendar-multiget>`,
		}
	}
	f := newFixture(t, "a.ics")
	for name, body := range bodies(names(1001)) {
		if rec := f.report(calURL, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: 1001 names: status %d, want 400", name, rec.Code)
		}
	}
	for name, body := range bodies(names(1000)) {
		if rec := f.report(calURL, body); rec.Code != http.StatusMultiStatus {
			t.Errorf("%s: 1000 names: status %d", name, rec.Code)
		}
	}
	for name, body := range bodies(strings.Repeat(`<X:p xmlns:X="urn:x"/>`, 5000)) {
		if rec := f.report(calURL, body); rec.Code != http.StatusMultiStatus {
			t.Errorf("%s: duplicates: status %d", name, rec.Code)
		}
	}
	inc := `<C:calendar-query ` + nsDecl + `><D:allprop/><D:include>` + names(1001) + `</D:include></C:calendar-query>`
	if rec := f.report(calURL, inc); rec.Code != http.StatusBadRequest {
		t.Errorf("include: status %d, want 400", rec.Code)
	}
}

func TestMultiget(t *testing.T) {
	f := newFixture(t, "ada.ics", "bob.ics")
	other := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "other"}
	ctx := context.Background()
	if _, err := f.mem.CreateCollection(ctx, other, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.PutObject(ctx, other, "x.ics", icsFor("x"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	hrefs := []string{
		"/dav/calendars/alice/work/bob.ics",
		"/dav/calendars/alice/work/missing.ics",
		"/dav/calendars/alice/other/x.ics",
		"/dav/calendars/bob/work/ada.ics",
		"/dav/calendars/alice/work/%2e%2e",
		"/dav/calendars/alice/work/../other/x.ics",
		"/dav/calendars/alice/work/a%00b.ics",
		"/dav/calendars/alice/work/" + strings.Repeat("a", 5000),
		"http://example.com/dav/calendars/alice/work/ada.ics",
		"/dav/calendars/alice/work/bob.ics", // duplicate
		"garbage",
		"",
		"/dav/calendars/alice/work/",
		"/dav/calendars/alice/work/a%2Fb",
	}
	var inner strings.Builder
	for _, h := range hrefs {
		inner.WriteString(hrefEl(strings.ReplaceAll(h, "&", "&amp;")))
	}
	f.st.gets.Store(0)
	r := parseReply(t, f.report(calURL, multiget(inner.String())))
	resps := r.root.Kids(dav.ResponseEl)
	status := func(i int) string {
		if s := resps[i].Child(dav.Status); s != nil {
			return s.Text
		}
		return "propstat"
	}
	want := []string{"propstat", "404", "404", "404", "404", "404", "404", "404", "propstat", "404", "404", "404", "404"}
	if len(resps) != len(want) {
		t.Fatalf("got %d responses %v, want %d", len(resps), r.hrefs, len(want))
	}
	for i, w := range want {
		if !strings.Contains(status(i), w) {
			t.Errorf("response %d (%q): status %q, want %s", i, r.hrefs[i], status(i), w)
		}
	}
	if got := r.must(t, 0, dav.CalendarData).Text; got != string(icsFor("bob.ics")) {
		t.Errorf("bob data = %q", got)
	}
	if r.hrefs[0] != "/dav/calendars/alice/work/bob.ics" {
		t.Errorf("href = %q", r.hrefs[0])
	}
	if n := f.st.gets.Load(); n > 3 {
		t.Errorf("hostile hrefs reached the store: %d reads", n)
	}
}

func TestMultigetAppleBody(t *testing.T) {
	f := newFixture(t, "ada.ics")
	body := `<?xml version="1.0" encoding="UTF-8"?>
<B:calendar-multiget xmlns:B="urn:ietf:params:xml:ns:caldav" xmlns:A="DAV:">
  <A:prop><A:getetag/><B:calendar-data/></A:prop>
  <A:href>/dav/calendars/alice/work/ada.ics</A:href>
</B:calendar-multiget>`
	r := parseReply(t, f.report(calURL, body))
	if len(r.hrefs) != 1 {
		t.Fatal(r.hrefs)
	}
	if got := r.must(t, 0, dav.CalendarData).Text; got != string(icsFor("ada.ics")) {
		t.Errorf("data = %q", got)
	}
}

func TestMultigetEncodedName(t *testing.T) {
	f := newFixture(t)
	f.put(t, "a b&c.ics", string(icsFor("s")))
	r := parseReply(t, f.report(calURL, multiget(hrefEl("/dav/calendars/alice/work/a%20b&amp;c.ics"))))
	if len(r.hrefs) != 1 {
		t.Fatal(r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	r = parseReply(t, f.report(calURL, multiget(hrefEl("/dav/calendars/alice/work/%252e%252e"))))
	if !strings.Contains(r.root.Kids(dav.ResponseEl)[0].Child(dav.Status).Text, "404") {
		t.Error("want 404")
	}
}

func TestMultigetTooManyHrefs(t *testing.T) {
	f := newFixture(t, "a.ics")
	var inner strings.Builder
	for i := range 1001 {
		inner.WriteString(hrefEl(fmt.Sprintf("/dav/calendars/alice/work/n%d", i)))
	}
	if rec := f.report(calURL, multiget(inner.String())); rec.Code != http.StatusBadRequest {
		t.Errorf("1001 hrefs: status %d, want 400", rec.Code)
	}
	inner.Reset()
	for i := range 1000 {
		inner.WriteString(hrefEl(fmt.Sprintf("/dav/calendars/alice/work/n%d", i)))
	}
	if rec := f.report(calURL, multiget(inner.String())); rec.Code != http.StatusMultiStatus {
		t.Errorf("1000 hrefs: status %d", rec.Code)
	}
}

func TestMultigetStoreErrorIs500PerHref(t *testing.T) {
	f := newFixture(t, "ada.ics", "bob.ics")
	f.st.fail = map[string]bool{"bob.ics": true}
	r := parseReply(t, f.report(calURL, multiget(
		hrefEl("/dav/calendars/alice/work/ada.ics")+hrefEl("/dav/calendars/alice/work/bob.ics"))))
	resps := r.root.Kids(dav.ResponseEl)
	if len(resps) != 2 {
		t.Fatal(r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	if s := resps[1].Child(dav.Status).Text; !strings.Contains(s, "500") || strings.Contains(s, "fire") {
		t.Errorf("status %q, want a plain 500", s)
	}
	if want := []string{"/dav/calendars/alice/work/ada.ics", "/dav/calendars/alice/work/bob.ics"}; !slices.Equal(r.hrefs, want) {
		t.Errorf("hrefs = %v, want %v", r.hrefs, want)
	}
}

func TestMultigetMissingCalendar(t *testing.T) {
	f := newFixture(t, "a.ics")
	rec := f.report("/dav/calendars/alice/nope/", multiget(hrefEl("/dav/calendars/alice/nope/a.ics")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("read %d objects", n)
	}
}

func TestMultigetSpellingsOfOneObjectAnswerOnce(t *testing.T) {
	f := newFixture(t, "bob.ics")
	f.st.gets.Store(0)
	r := parseReply(t, f.report(calURL, multiget(
		hrefEl("/dav/calendars/alice/work/bob.ics")+
			hrefEl("/dav/calendars/%61lice/work/b%6Fb.ics")+
			hrefEl("http://example.com/dav/calendars/alice/work/bob.ics"))))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/calendars/alice/work/bob.ics" {
		t.Errorf("hrefs = %v", r.hrefs)
	}
	if n := f.st.gets.Load(); n != 1 {
		t.Errorf("GetObject called %d times, want 1", n)
	}
}

func TestOwnerBoundary(t *testing.T) {
	mem := memory.New()
	ctx := context.Background()
	for _, o := range []string{"owner-alice", "owner-bob"} {
		ref := store.CollectionRef{Owner: o, Kind: store.KindCalendar, Name: "work"}
		if _, err := mem.CreateCollection(ctx, ref, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if _, err := mem.PutObject(ctx, ref, "c.ics", icsFor(o), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := New(Config{Store: mem, Prefix: "/dav", Resolve: func(_ *http.Request, u string) (string, error) {
		return "owner-" + u, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(target, body string) reply {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("REPORT", target, strings.NewReader(body)))
		return parseReply(t, rec)
	}
	r := run("/dav/calendars/bob/work/", multiget(
		hrefEl("/dav/calendars/alice/work/c.ics")+hrefEl("/dav/calendars/bob/work/c.ics")))
	resps := r.root.Kids(dav.ResponseEl)
	if !strings.Contains(resps[0].Child(dav.Status).Text, "404") {
		t.Error("bob reached alice's object")
	}
	if got := r.must(t, 1, dav.CalendarData).Text; !strings.Contains(got, "owner-bob") {
		t.Errorf("bob's own data = %q", got)
	}
	r = run("/dav/calendars/bob/work/", query(evFilter(propFilter("UID", "", ""))))
	if len(r.hrefs) != 1 || !strings.Contains(r.hrefs[0], "/bob/") {
		t.Errorf("query hrefs = %v", r.hrefs)
	}
}

func TestReportDispatch(t *testing.T) {
	f := newFixture(t, "a.ics")
	rec := f.report(calURL, `<D:bogus `+nsDecl+`/>`)
	if rec.Code != 403 || precondition(t, rec) != "supported-report" {
		t.Errorf("unknown root: %d %s", rec.Code, rec.Body)
	}
	rec = f.report(calURL, `<D:sync-collection `+nsDecl+`><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`)
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("sync-collection: %d %s", rec.Code, rec.Body)
	}
	if rec := f.report(calURL, `<oops`); rec.Code != 400 {
		t.Errorf("malformed: %d", rec.Code)
	}
	if rec := f.report(calURL, ``); rec.Code != 400 {
		t.Errorf("empty: %d", rec.Code)
	}
	if rec := f.report(calURL, query("")+strings.Repeat(" ", 2<<20)); rec.Code != 413 {
		t.Errorf("large: %d", rec.Code)
	}
	if rec := f.report("/dav/calendars/alice/nope/", query("")); rec.Code != 404 {
		t.Errorf("missing calendar: %d", rec.Code)
	}
	if rec := f.report("/dav/calendars/alice/nope/", `<D:bogus `+nsDecl+`/>`); rec.Code != 404 {
		t.Errorf("missing calendar, bad report: %d", rec.Code)
	}
	for _, u := range []string{"/dav/calendars/alice/", "/dav/calendars/alice/work/a.ics"} {
		rec := f.report(u, query(""))
		if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "PROPFIND") || strings.Contains(rec.Header().Get("Allow"), "REPORT") {
			t.Errorf("%s: %d allow %q", u, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if rec := f.report("/dav/calendars/alice/work", query("")); rec.Code != http.StatusMultiStatus {
		t.Errorf("no trailing slash: %d", rec.Code)
	}
}

type listFailStore struct{ store.Store }

func (listFailStore) ListObjects(context.Context, store.CollectionRef, store.ListOptions) ([]store.Object, error) {
	return nil, fmt.Errorf("boom")
}

func TestQueryStoreListFailureIs500(t *testing.T) {
	f := newFixture(t, "a.ics")
	f.h.store = listFailStore{f.st}
	rec := f.report(calURL, query(""))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestQueryLargeCalendar(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	f := newFixture(t)
	want := 0
	for i := range 3000 {
		day := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, i%365)
		if day.Month() == time.March {
			want++
		}
		d := wrapCal(fmt.Sprintf("BEGIN:VEVENT\r\nUID:u%d\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:Event %d\r\nEND:VEVENT\r\n",
			i, day.Format("20060102T150405Z"), day.Add(time.Hour).Format("20060102T150405Z"), i))
		f.put(t, fmt.Sprintf("e%d.ics", i), d)
	}
	start := time.Now()
	r := parseReply(t, f.report(calURL, timeQuery("20260301T000000Z", "20260401T000000Z")))
	el := time.Since(start)
	if len(r.hrefs) != want {
		t.Errorf("got %d results, want %d", len(r.hrefs), want)
	}
	if el > 10*time.Second {
		t.Errorf("query took %v", el)
	}
}

func FuzzReport(f *testing.F) {
	mem := memory.New()
	ctx := context.Background()
	if _, err := mem.CreateCollection(ctx, workRef, store.Props{}); err != nil {
		f.Fatal(err)
	}
	for _, n := range []string{"a.ics", "b.ics"} {
		if _, err := mem.PutObject(ctx, workRef, n, icsFor(n), store.PutOptions{}); err != nil {
			f.Fatal(err)
		}
	}
	if _, err := mem.PutObject(ctx, workRef, "bad.ics", []byte("garbage"), store.PutOptions{}); err != nil {
		f.Fatal(err)
	}
	h, err := New(Config{Store: mem, Prefix: "/dav", Resolve: resolveAs("alice")})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(timeQuery("20260301T000000Z", "20260401T000000Z"))
	f.Add(query(evFilter(propFilter("SUMMARY", "", textMatch(`negate-condition="yes"`, "a")))))
	f.Add(query(evFilter(propFilter("ATTENDEE", "", `<C:param-filter name="PARTSTAT"><C:is-not-defined/></C:param-filter>`))))
	f.Add(multiget(hrefEl("/dav/calendars/alice/work/a.ics")))
	f.Add(`<D:sync-collection xmlns:D="DAV:"/>`)
	f.Fuzz(func(t *testing.T, body string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("REPORT", calURL, strings.NewReader(body)))
		if rec.Code >= 500 {
			t.Fatalf("status %d for %q", rec.Code, body)
		}
	})
}

// TestQueryWideWindows asks for "everything" with windows far wider than
// 100 years.
func TestQueryWideWindows(t *testing.T) {
	f := newFixture(t)
	f.put(t, "single.ics", wrapCal(vev("UID:s", "DTSTART:20260310T090000Z", "DTEND:20260310T100000Z")))
	f.put(t, "weekly.ics", wrapCal(vev("UID:w", "DTSTART:20200106T090000Z", "DTEND:20200106T100000Z", "RRULE:FREQ=WEEKLY")))
	f.put(t, "daily.ics", wrapCal(vev("UID:d", "DTSTART:20250101T090000Z", "DTEND:20250101T100000Z", "RRULE:FREQ=DAILY;COUNT=400")))
	f.put(t, "old.ics", wrapCal(vev("UID:o", "DTSTART:19500101T090000Z", "DTEND:19500101T100000Z", "RRULE:FREQ=DAILY;UNTIL=19500110T090000Z")))
	all := []string{"daily.ics", "old.ics", "single.ics", "weekly.ics"}
	recent := []string{"daily.ics", "single.ics", "weekly.ics"}
	tests := []struct {
		name       string
		start, end string
		want       []string
	}{
		{"1900 to 2100", "19000101T000000Z", "21000101T000000Z", all},
		{"start only 1900", "19000101T000000Z", "", all},
		{"year 1 to year 9999", "00010101T000000Z", "99991231T235959Z", all},
		{"year 0 to year 9999", "00000101T000000Z", "99991231T235959Z", all},
		{"end only 2100", "", "21000101T000000Z", all},
		{"end only year 9999", "", "99991231T235959Z", all},
		{"1960 to 2100", "19600101T000000Z", "21000101T000000Z", recent},
		{"end only year 1 matches nothing", "", "00010101T000000Z", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := parseReply(t, f.report(calURL, timeQuery(tt.start, tt.end)))
			if got := bases(r); len(got) != len(tt.want) || (len(got) > 0 && !slices.Equal(got, tt.want)) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A query that selects every object but asks for calendar-data reads each
// object anyway. One whose data does not parse is skipped, as with a
// filter: clients parse every calendar-data they get, and one bad object
// would fail their whole query.
func TestQueryWithCalendarDataSkipsUnparsableObjects(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics")
	f.put(t, "garbage.ics", "\x00\xff not a calendar at all")
	f.put(t, "card.ics", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:x\r\nEND:VCARD\r\n")
	var logs bytes.Buffer
	f.h.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	data := `<D:prop><D:getetag/><C:calendar-data/></D:prop>`
	for name, flt := range map[string]string{
		"no filter":          "",
		"empty filter":       `<C:filter/>`,
		"bare VCALENDAR":     `<C:filter><C:comp-filter name="VCALENDAR"/></C:filter>`,
		"allprop, no filter": "allprop",
	} {
		t.Run(name, func(t *testing.T) {
			body := `<C:calendar-query ` + nsDecl + `>` + data + flt + `</C:calendar-query>`
			if flt == "allprop" {
				body = `<C:calendar-query ` + nsDecl + `><D:allprop/><D:include><C:calendar-data/></D:include></C:calendar-query>`
			}
			r := parseReply(t, f.report(calURL, body))
			if want := []string{"a.ics", "b.ics"}; !slices.Equal(bases(r), want) {
				t.Errorf("got %v, want %v", bases(r), want)
			}
		})
	}
	if out := logs.String(); !strings.Contains(out, "garbage.ics") || !strings.Contains(out, "card.ics") {
		t.Errorf("warnings not logged by name:\n%s", out)
	}

	// Without calendar-data nothing is read, and every object is listed.
	f.st.gets.Store(0)
	r := parseReply(t, f.report(calURL, query("")))
	if want := []string{"a.ics", "b.ics", "card.ics", "garbage.ics"}; !slices.Equal(bases(r), want) {
		t.Errorf("hrefs only: got %v, want %v", bases(r), want)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("hrefs only: GetObject called %d times, want 0", n)
	}
}

func TestFreeBusyQueryIsUnsupported(t *testing.T) {
	f := newFixture(t, "a.ics")
	rec := f.report(calURL, `<C:free-busy-query `+nsDecl+`><C:time-range start="20260310T000000Z" end="20260311T000000Z"/></C:free-busy-query>`)
	if rec.Code != http.StatusForbidden || precondition(t, rec) != "supported-report" {
		t.Errorf("free-busy-query: %d %s", rec.Code, rec.Body)
	}
}
