// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-webdav/caldav"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

var alicePersonal = store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "personal"}

const personalHref = "/dav/calendars/alice/personal/"

// queryWindow runs a time-range calendar-query on cal and returns the
// matching paths.
func queryWindow(t *testing.T, ctx context.Context, c *caldav.Client, cal string, start, end time.Time) []string {
	t.Helper()
	found, err := c.QueryCalendar(ctx, cal, timeQuery(start, end))
	if err != nil {
		t.Fatalf("QueryCalendar %v..%v: %v", start, end, err)
	}
	return calObjPaths(found)
}

func TestCalendarSurvivesRestart(t *testing.T) {
	ctx := ctxTimeout(t)
	dir := t.TempDir()

	st := openFileStore(t, dir)
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startCalendarServer(t, st)
	c := calClient(t, ts.URL+"/dav/", "alice")
	for name, data := range map[string]string{
		"weekly.ics": weeklyEvent,
		"zoned.ics":  zonedEvent,
		"gone.ics":   utcEvent("gone-1", "20260330T070000Z", "Gone"),
	} {
		if _, err := c.PutCalendarObject(ctx, personalHref+name, decodeCal(t, data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RemoveAll(ctx, personalHref+"gone.ics"); err != nil {
		t.Fatal(err)
	}
	march := func() []string {
		return queryWindow(t, ctx, c, personalHref, at(2026, 3, 30, 7, 0), at(2026, 3, 30, 7, 30))
	}
	before := march()
	if !slices.Equal(before, []string{personalHref + "weekly.ics"}) {
		t.Fatalf("query before restart = %v", before)
	}
	_, sync1 := calSync(t, ts, personalHref, "")
	old, err := c.GetCalendarObject(ctx, personalHref+"zoned.ics")
	if err != nil {
		t.Fatal(err)
	}
	ts.Close()
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st = openFileStore(t, dir)
	t.Cleanup(func() { _ = st.Close() })
	ts = startCalendarServer(t, st)
	c = calClient(t, ts.URL+"/dav/", "alice")

	if after := march(); !slices.Equal(after, before) {
		t.Errorf("query after restart = %v, before %v", after, before)
	}
	got, err := c.GetCalendarObject(ctx, personalHref+"zoned.ics")
	if err != nil {
		t.Fatal(err)
	}
	if got.ETag != old.ETag || eventSummary(t, got.Data) != "Summer meeting" {
		t.Errorf("zoned after restart: etag %q (was %q)", got.ETag, old.ETag)
	}

	// The old token still works and reports nothing new.
	code, same := calSync(t, ts, personalHref, sync1.token)
	if code != http.StatusMultiStatus || len(same.updated)+len(same.deleted) != 0 || same.token != sync1.token {
		t.Errorf("sync after restart = %d %+v, token before %q", code, same, sync1.token)
	}
	// Changes after the restart are all it reports.
	if _, err := c.PutCalendarObject(ctx, personalHref+"new.ics", decodeCal(t, utcEvent("new-1", "20260330T070000Z", "New"))); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, personalHref+"zoned.ics"); err != nil {
		t.Fatal(err)
	}
	_, next := calSync(t, ts, personalHref, sync1.token)
	if !slices.Equal(next.updated, []string{personalHref + "new.ics"}) || !slices.Equal(next.deleted, []string{personalHref + "zoned.ics"}) {
		t.Errorf("incremental sync after restart = %+v", next)
	}
	if got := march(); !slices.Equal(got, []string{personalHref + "new.ics", personalHref + "weekly.ics"}) {
		t.Errorf("query after the new event = %v", got)
	}
}

// badEvents are written straight to the store, bypassing validation. The
// last two parse but have a start or a rule that does not.
var badEvents = map[string][]byte{
	"garbage.ics":   {0x00, 0xff, 0xfe, 'x', 0x01, '\n', 0x80},
	"empty.ics":     {},
	"truncated.ics": []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:half\r\nDTSTART:20260601"),
	"card.ics":      []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Not an event\r\nEND:VCARD\r\n"),
	"baddate.ics": []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:bad-date\r\n" +
		"DTSTAMP:20260101T000000Z\r\nDTSTART:2026-06-01\r\nSUMMARY:Broken\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"),
	"badrule.ics": []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:bad-rule\r\n" +
		"DTSTAMP:20260101T000000Z\r\nDTSTART:20260601T100000Z\r\nRRULE:FREQ=SOMETIMES;COUNT=x\r\nSUMMARY:Broken\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"),
}

func TestOneBadCalendarObjectDoesNotBreakAnything(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startCalendarServer(t, st)
	c := calClient(t, ts.URL+"/dav/", "alice")

	good := []string{personalHref + "one.ics", personalHref + "two.ics"}
	for i, p := range good {
		if _, err := c.PutCalendarObject(ctx, p, decodeCal(t, utcEvent("good-"+string(rune('a'+i)), "20260601T100000Z", "Good-looking"))); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range badEvents {
		if _, err := st.PutObject(ctx, alicePersonal, name, data, store.PutOptions{}); err != nil {
			t.Fatalf("PutObject %s: %v", name, err)
		}
	}

	// Listing: every object, good or bad, with a 207.
	code, body := rawDo(t, ts, "PROPFIND", personalHref, "alice", http.Header{"Depth": {"1"}}, strings.NewReader(propfindBody))
	if code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d %s", code, body)
	}
	for _, p := range good {
		if !strings.Contains(body, p) {
			t.Errorf("PROPFIND lacks %s", p)
		}
	}

	// A time range and a text match: only the good ones.
	if got := queryWindow(t, ctx, c, personalHref, at(2026, 6, 1, 0, 0), at(2026, 6, 2, 0, 0)); !slices.Equal(got, good) {
		t.Errorf("time-range query = %v, want %v", got, good)
	}
	found, err := c.QueryCalendar(ctx, personalHref, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR"},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Props: []caldav.PropFilter{{Name: "SUMMARY", TextMatch: &caldav.TextMatch{Text: "looking"}}},
		}}},
	})
	if err != nil {
		t.Fatalf("text query: %v", err)
	}
	if got := calObjPaths(found); !slices.Equal(got, good) {
		t.Errorf("text query = %v", got)
	}
	// A query that matches every VCALENDAR: still only the parseable ones.
	found, err = c.QueryCalendar(ctx, personalHref, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR"},
		CompFilter:  caldav.CompFilter{Name: "VCALENDAR"},
	})
	if err != nil {
		t.Fatalf("match-all query: %v", err)
	}
	for _, p := range good {
		if !slices.Contains(calObjPaths(found), p) {
			t.Errorf("match-all query lacks %s: %v", p, calObjPaths(found))
		}
	}

	// Multiget of a good one next to a bad one does not fail.
	code, body = rawDo(t, ts, "REPORT", personalHref, "alice", http.Header{"Depth": {"1"}}, strings.NewReader(
		`<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">`+
			`<D:prop><D:getetag/><C:calendar-data/></D:prop>`+
			`<D:href>`+good[0]+`</D:href><D:href>`+personalHref+`garbage.ics</D:href></C:calendar-multiget>`))
	if code != http.StatusMultiStatus || !strings.Contains(body, "Good-looking") {
		t.Errorf("multiget = %d %s", code, body)
	}

	// Sync: the good ones are there.
	code, res := calSync(t, ts, personalHref, "")
	if code != http.StatusMultiStatus {
		t.Fatalf("sync = %d", code)
	}
	for _, p := range good {
		if !slices.Contains(res.updated, p) {
			t.Errorf("sync lacks %s: %v", p, res.updated)
		}
	}

	// GET of a bad object returns the stored bytes; it is not a server error.
	code, body = rawDo(t, ts, "GET", personalHref+"garbage.ics", "alice", nil, nil)
	if code != http.StatusOK || body != string(badEvents["garbage.ics"]) {
		t.Errorf("GET garbage = %d %q", code, body)
	}
}

// Recurrence fixtures. Every time below is listed with its UTC
// occurrences; the windows in TestRecurrenceOverHTTP are chosen so that a
// wrong offset, a lost EXDATE or override, or an UNTIL off by one changes
// the answer.
const (
	recurHead = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n"
	recurTail = "END:VCALENDAR\r\n"

	// Tuesdays 10:00 Prague from 24 March, three of them; the one of 31
	// March moved to Wednesday 1 April 14:00. UTC: 24 Mar 09:00, 1 Apr
	// 12:00, 7 Apr 08:00.
	movedEvent = recurHead + pragueZone +
		"BEGIN:VEVENT\r\nUID:moved-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Europe/Prague:20260324T100000\r\nDTEND;TZID=Europe/Prague:20260324T110000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=3\r\nSUMMARY:Review\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:moved-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"RECURRENCE-ID;TZID=Europe/Prague:20260331T100000\r\n" +
		"DTSTART;TZID=Europe/Prague:20260401T140000\r\nDTEND;TZID=Europe/Prague:20260401T150000\r\n" +
		"SUMMARY:Review (moved)\r\nEND:VEVENT\r\n" + recurTail

	// Daily 18:00 Prague from 27 March until 30 March 16:00 UTC, which is
	// the last occurrence. UTC: 27 and 28 Mar 17:00, 29 and 30 Mar 16:00.
	untilEvent = recurHead + pragueZone +
		"BEGIN:VEVENT\r\nUID:until-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Europe/Prague:20260327T180000\r\nDURATION:PT1H\r\n" +
		"RRULE:FREQ=DAILY;UNTIL=20260330T160000Z\r\nSUMMARY:Evening\r\nEND:VEVENT\r\n" + recurTail

	// A TZID no VTIMEZONE, Windows name or database knows: read as UTC,
	// 26 Mar 10:00-11:00.
	nowhereEvent = recurHead +
		"BEGIN:VEVENT\r\nUID:nowhere-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Mars/Olympus_Mons:20260326T100000\r\nDTEND;TZID=Mars/Olympus_Mons:20260326T110000\r\n" +
		"SUMMARY:Odd zone\r\nEND:VEVENT\r\n" + recurTail

	// All day on 2 April: 2 Apr 00:00 to 3 Apr 00:00 UTC.
	allDayEvent = recurHead +
		"BEGIN:VEVENT\r\nUID:allday-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;VALUE=DATE:20260402\r\nDTEND;VALUE=DATE:20260403\r\nSUMMARY:Holiday\r\nEND:VEVENT\r\n" + recurTail

	// A Windows zone name without a VTIMEZONE: Central Europe is
	// Europe/Budapest, the same offsets as Prague. Wednesdays 12:00 from
	// 25 March, two of them. UTC: 25 Mar 11:00, 1 Apr 10:00.
	windowsEvent = recurHead +
		"BEGIN:VEVENT\r\nUID:windows-1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART;TZID=Central Europe Standard Time:20260325T120000\r\n" +
		"DTEND;TZID=Central Europe Standard Time:20260325T130000\r\n" +
		"RRULE:FREQ=WEEKLY;COUNT=2\r\nSUMMARY:Sync call\r\nEND:VEVENT\r\n" + recurTail
)

func TestRecurrenceOverHTTP(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startCalendarServer(t, st)
	c := calClient(t, ts.URL+"/dav/", "alice")

	_, tzErr := time.LoadLocation("Europe/Budapest")
	haveTZ := tzErr == nil

	cal := personalHref
	objects := map[string]string{
		"weekly.ics":   weeklyEvent, // 23 Mar 08:00, 30 Mar 07:00, 13 Apr 07:00; not 6 Apr
		"moved.ics":    movedEvent,
		"until.ics":    untilEvent,
		"nowhere.ics":  nowhereEvent,
		"ordinary.ics": utcEvent("ordinary-1", "20260326T080000Z", "Ordinary"),
		"allday.ics":   allDayEvent,
		"atstart.ics":  utcEvent("atstart-1", "20260403T000000Z", "Midnight"),
		"windows.ics":  windowsEvent,
	}
	// Raw PUTs, so the bytes stored are the bytes above.
	for name, data := range objects {
		if code, body := rawDo(t, ts, "PUT", cal+name, "alice", http.Header{"Content-Type": {"text/calendar"}}, strings.NewReader(data)); code != http.StatusCreated {
			t.Fatalf("PUT %s = %d %s", name, code, body)
		}
	}

	tests := []struct {
		name       string
		start, end time.Time
		want       []string
		needsTZ    bool // the answer depends on the system zone database
	}{
		{"weekly first, winter time", at(2026, 3, 23, 8, 0), at(2026, 3, 23, 8, 30), []string{"weekly.ics"}, false},
		{"weekly first is not at summer time", at(2026, 3, 23, 7, 0), at(2026, 3, 23, 7, 30), nil, false},
		{"weekly after the change, summer time", at(2026, 3, 30, 7, 0), at(2026, 3, 30, 7, 30), []string{"weekly.ics"}, false},
		{"weekly after the change is not winter time", at(2026, 3, 30, 8, 0), at(2026, 3, 30, 8, 30), nil, false},
		{"weekly excluded day", at(2026, 4, 6, 0, 0), at(2026, 4, 7, 0, 0), nil, false},
		{"weekly last", at(2026, 4, 13, 0, 0), at(2026, 4, 14, 0, 0), []string{"weekly.ics"}, false},
		{"weekly after the last", at(2026, 4, 20, 0, 0), at(2026, 5, 1, 0, 0), nil, false},

		{"moved: first", at(2026, 3, 24, 9, 0), at(2026, 3, 24, 9, 30), []string{"moved.ics"}, false},
		{"moved: original day is empty", at(2026, 3, 31, 0, 0), at(2026, 4, 1, 0, 0), nil, false},
		{"moved: the new time", at(2026, 4, 1, 12, 0), at(2026, 4, 1, 12, 30), []string{"moved.ics"}, false},
		{"moved: not at the master's time on the new day", at(2026, 4, 1, 8, 0), at(2026, 4, 1, 8, 30), nil, false},
		{"moved: last, summer time", at(2026, 4, 7, 8, 0), at(2026, 4, 7, 8, 30), []string{"moved.ics"}, false},

		{"until: before the change", at(2026, 3, 28, 17, 0), at(2026, 3, 28, 17, 30), []string{"until.ics"}, false},
		{"until: on the change day, summer time", at(2026, 3, 29, 16, 0), at(2026, 3, 29, 16, 30), []string{"until.ics"}, false},
		{"until: on the change day, not winter time", at(2026, 3, 29, 17, 0), at(2026, 3, 29, 17, 30), nil, false},
		{"until: the UNTIL instant itself", at(2026, 3, 30, 16, 0), at(2026, 3, 30, 16, 30), []string{"until.ics"}, false},
		{"until: nothing after UNTIL", at(2026, 3, 31, 12, 0), at(2026, 4, 1, 0, 0), nil, false},

		{"odd zone read as UTC", at(2026, 3, 26, 10, 0), at(2026, 3, 26, 10, 30), []string{"nowhere.ics"}, false},
		{"ordinary next to the odd zone", at(2026, 3, 26, 8, 0), at(2026, 3, 26, 8, 30), []string{"ordinary.ics"}, false},
		{"odd zone not as Prague time", at(2026, 3, 26, 9, 0), at(2026, 3, 26, 9, 30), nil, false},
		{"the odd and the ordinary day", at(2026, 3, 26, 0, 0), at(2026, 3, 27, 0, 0), []string{"nowhere.ics", "ordinary.ics"}, false},

		{"all-day: its own day", at(2026, 4, 2, 0, 0), at(2026, 4, 3, 0, 0), []string{"allday.ics"}, false},
		{"all-day: not the day after", at(2026, 4, 3, 0, 0), at(2026, 4, 4, 0, 0), []string{"atstart.ics"}, false},
		{"midnight start: not the day before", at(2026, 4, 2, 0, 0), at(2026, 4, 3, 0, 0), []string{"allday.ics"}, false},
		{"midnight start: its first hour", at(2026, 4, 3, 0, 0), at(2026, 4, 3, 1, 0), []string{"atstart.ics"}, false},
		{"the day before both", at(2026, 4, 1, 0, 0), at(2026, 4, 2, 0, 0), []string{"moved.ics", "windows.ics"}, false},

		{"windows zone, winter time", at(2026, 3, 25, 11, 0), at(2026, 3, 25, 11, 30), []string{"windows.ics"}, true},
		{"windows zone is not UTC", at(2026, 3, 25, 12, 0), at(2026, 3, 25, 12, 30), nil, true},
		{"windows zone, summer time", at(2026, 4, 1, 10, 0), at(2026, 4, 1, 10, 30), []string{"windows.ics"}, true},

		{"everything", at(2026, 3, 1, 0, 0), at(2026, 5, 1, 0, 0), []string{
			"allday.ics", "atstart.ics", "moved.ics", "nowhere.ics", "ordinary.ics", "until.ics", "weekly.ics", "windows.ics",
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needsTZ && !haveTZ {
				t.Skipf("no time zone database: %v", tzErr)
			}
			want := []string{}
			for _, n := range tt.want {
				want = append(want, cal+n)
			}
			got := queryWindow(t, ctx, c, cal, tt.start, tt.end)
			if !haveTZ {
				// Where the Windows zone falls depends on the database.
				drop := func(p string) bool { return p == cal+"windows.ics" }
				want = slices.DeleteFunc(want, drop)
				got = slices.DeleteFunc(got, drop)
			}
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("%v..%v = %v, want %v", tt.start, tt.end, got, want)
			}
		})
	}
}

func TestHostileCalendarRequests(t *testing.T) {
	st := memory.New()
	if err := groupkit.EnsureDefaultCollections(t.Context(), st, "alice"); err != nil {
		t.Fatal(err)
	}
	event := utcEvent("a-1", "20260601T100000Z", "A")
	if _, err := st.PutObject(t.Context(), alicePersonal, "a.ics", []byte(event), store.PutOptions{UID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, basicConfig(st))
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/.well-known/caldav", srv.WellKnown())
	var errLog panicLog
	ts := httptest.NewUnstartedServer(mux)
	ts.Config.ErrorLog = log.New(&errLog, "", 0)
	ts.Start()
	defer ts.Close()

	ok := func() {
		t.Helper()
		if code, body := rawDo(t, ts, "PROPFIND", personalHref, "alice", http.Header{"Depth": {"1"}}, strings.NewReader(propfindBody)); code != http.StatusMultiStatus ||
			!strings.Contains(body, personalHref+"a.ics") {
			t.Fatalf("server no longer serves: %d %s", code, body)
		}
	}
	quick := func(name string, start time.Time) {
		t.Helper()
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}

	const cal = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"`
	deep := strings.Repeat("<p>", 100000) + strings.Repeat("</p>", 100000)
	// 20000 nested comp-filters fit in the 1 MiB XML limit.
	deepFilter := strings.Repeat(`<C:comp-filter name="VCALENDAR">`, 20000) + strings.Repeat(`</C:comp-filter>`, 20000)
	big := strings.Repeat("<D:prop/>", 2<<20/9+1)
	ifMatch := make([]string, 200)
	for i := range ifMatch {
		ifMatch[i] = `"etag-` + strings.Repeat("x", i) + `"`
	}
	longName := strings.Repeat("n", 10<<10) + ".ics"
	query := func(filter string) string {
		return `<C:calendar-query ` + cal + `><D:prop><D:getetag/></D:prop><C:filter>` + filter + `</C:filter></C:calendar-query>`
	}
	// Just under the 1 MiB XML limit: one huge text-match, and many small
	// prop-filters.
	hugeText := query(`<C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:prop-filter name="SUMMARY">` +
		`<C:text-match>` + strings.Repeat("a", 1<<20-400) + `</C:text-match></C:prop-filter></C:comp-filter></C:comp-filter>`)
	propFilter := `<C:prop-filter name="SUMMARY"><C:is-not-defined/></C:prop-filter>`
	manyFilters := query(`<C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` +
		strings.Repeat(propFilter, (1<<20-400)/len(propFilter)) + `</C:comp-filter></C:comp-filter>`)
	overLimit := query(`<C:comp-filter name="VCALENDAR">` + strings.Repeat(propFilter, 1<<20/len(propFilter)+1) + `</C:comp-filter>`)
	twelveMiB := strings.Replace(event, "SUMMARY:A", "SUMMARY:"+strings.Repeat("A", 12<<20), 1)

	cases := []struct {
		name   string
		method string
		path   string
		hdr    http.Header
		body   string
		want   int
	}{
		{"deep XML propfind", "PROPFIND", personalHref, http.Header{"Depth": {"1"}}, `<D:propfind xmlns:D="DAV:">` + deep + `</D:propfind>`, 400},
		{"deep XML query", "REPORT", personalHref, http.Header{"Depth": {"1"}}, `<C:calendar-query ` + cal + `>` + deep + `</C:calendar-query>`, 400},
		{"deep comp-filters", "REPORT", personalHref, http.Header{"Depth": {"1"}}, query(deepFilter), 400},
		{"deep XML mkcalendar", "MKCALENDAR", "/dav/calendars/alice/deep/", nil, `<C:mkcalendar ` + cal + `>` + deep + `</C:mkcalendar>`, 400},
		{"deep XML on the home", "PROPFIND", "/dav/calendars/alice/", nil, `<D:propfind xmlns:D="DAV:">` + deep + `</D:propfind>`, 400},
		{"2 MiB propfind", "PROPFIND", personalHref, http.Header{"Depth": {"1"}}, `<D:propfind xmlns:D="DAV:">` + big + `</D:propfind>`, 413},
		{"2 MiB report", "REPORT", personalHref, nil, `<C:calendar-query ` + cal + `>` + big + `</C:calendar-query>`, 413},
		{"2 MiB proppatch", "PROPPATCH", personalHref, nil, `<D:propertyupdate xmlns:D="DAV:">` + big + `</D:propertyupdate>`, 413},
		{"2 MiB mkcalendar", "MKCALENDAR", "/dav/calendars/alice/x/", nil, `<C:mkcalendar ` + cal + `>` + big + `</C:mkcalendar>`, 413},
		{"1 MiB text-match", "REPORT", personalHref, http.Header{"Depth": {"1"}}, hugeText, 403},
		{"1 MiB of prop-filters", "REPORT", personalHref, http.Header{"Depth": {"1"}}, manyFilters, 403},
		{"filter over the XML limit", "REPORT", personalHref, http.Header{"Depth": {"1"}}, overLimit, 413},
		{"200 If-Match", "PUT", personalHref + "a.ics", http.Header{"If-Match": ifMatch}, event, 412},
		{"200 If-Match delete", "DELETE", personalHref + "a.ics", http.Header{"If-Match": ifMatch}, "", 412},
		{"10 KiB name put", "PUT", personalHref + longName, nil, utcEvent("long-1", "20260601T100000Z", "L"), 400},
		{"10 KiB name get", "GET", personalHref + longName, nil, "", 400},
		{"10 KiB calendar", "MKCALENDAR", "/dav/calendars/alice/" + longName + "/", nil, "", 400},
		{"Depth -1", "PROPFIND", personalHref, http.Header{"Depth": {"-1"}}, propfindBody, 400},
		{"Depth -1 on the home", "PROPFIND", "/dav/calendars/alice/", http.Header{"Depth": {"-1"}}, propfindBody, 400},
		// REPORT ignores Depth (the reports here act on the calendar's
		// members whatever it says), so an invalid one changes nothing.
		{"Depth -1 report", "REPORT", personalHref, http.Header{"Depth": {"-1"}}, query(`<C:comp-filter name="VCALENDAR"/>`), 207},
		{"12 MiB put", "PUT", personalHref + "big.ics", nil, twelveMiB, 413},
		{"garbage iCalendar", "PUT", personalHref + "g.ics", nil, "\x00\xff\xfeBEGIN:VCALENDAR\r\n\x80", 403},
		{"a vCard as an event", "PUT", personalHref + "g.ics", nil, contactA, 403},
		{"unterminated iCalendar", "PUT", personalHref + "g.ics", nil, strings.Repeat("BEGIN:VCALENDAR\r\n", 50000), 403},
	}
	for _, tc := range cases {
		start := time.Now()
		code, body := rawDo(t, ts, tc.method, tc.path, "alice", tc.hdr, strings.NewReader(tc.body))
		if code != tc.want {
			t.Errorf("%s: status %d, want %d; body %.200s", tc.name, code, tc.want, body)
		}
		quick(tc.name, start)
		ok()
	}
	// Nothing hostile was stored.
	if code, _ := rawDo(t, ts, "GET", personalHref+"g.ics", "alice", nil, nil); code != http.StatusNotFound {
		t.Errorf("GET g.ics = %d, want 404", code)
	}
	if code, body := rawDo(t, ts, "GET", personalHref+"a.ics", "alice", nil, nil); code != http.StatusOK || body != event {
		t.Errorf("GET a.ics = %d %q", code, body)
	}

	// Request-line oddities, sent as written over a raw connection.
	addr := ts.Listener.Addr().String()
	auth := "Authorization: Basic YWxpY2U6YWxpY2UtcHc=\r\n" // alice:alice-pw
	raw := []struct {
		name string
		line string
		want int
	}{
		{"64 KiB URL", "GET " + personalHref + strings.Repeat("u", 64<<10) + " HTTP/1.1", 400},
		{"NUL in path", "GET " + personalHref + "a\x00b.ics HTTP/1.1", 400},
		{"escaped NUL in path", "GET " + personalHref + "a%00b.ics HTTP/1.1", 400},
		{"invalid escape", "GET " + personalHref + "%zz.ics HTTP/1.1", 400},
		{"truncated escape", "MKCALENDAR /dav/calendars/alice/%4 HTTP/1.1", 400},
		{"escaped slash in a calendar name", "MKCALENDAR /dav/calendars/alice/a%2Fb/ HTTP/1.1", 400},
		{"escaped dot-dot calendar", "PROPFIND /dav/calendars/alice/%2e%2e/ HTTP/1.1", 400},
		{"escaped dot-dot object", "GET " + personalHref + "%2E%2E HTTP/1.1", 400},
		{"escaped dot-dot past the object", "GET " + personalHref + "%2e%2e/%2e%2e/bob/personal/a.ics HTTP/1.1", 404},
	}
	for _, tc := range raw {
		start := time.Now()
		code := rawLine(t, addr, tc.line+"\r\nHost: test\r\n"+auth+"Connection: close\r\n\r\n")
		if code != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, code, tc.want)
		}
		quick(tc.name, start)
		ok()
	}

	if s := errLog.String(); strings.Contains(s, "panic") {
		t.Errorf("server log has a panic:\n%s", s)
	}
}
