// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/LukasHirt/groupkit/store"
)

// startCalendarServer is startBasicServer with both well-known URLs.
func startCalendarServer(t *testing.T, st store.Store) *httptest.Server {
	t.Helper()
	srv := newServer(t, basicConfig(st))
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/dav", srv.Handler())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	mux.Handle("/.well-known/caldav", srv.WellKnown())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// calClient returns a go-webdav CalDAV client for endpoint as user.
func calClient(t *testing.T, endpoint, user string) *caldav.Client {
	t.Helper()
	hc := webdav.HTTPClientWithBasicAuth(&http.Client{Timeout: 10 * time.Second}, user, basicUsers[user])
	c, err := caldav.NewClient(hc, endpoint)
	if err != nil {
		t.Fatalf("caldav.NewClient: %v", err)
	}
	return c
}

// decodeCal parses s with the client's iCalendar decoder.
func decodeCal(t *testing.T, s string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(s)).Decode()
	if err != nil {
		t.Fatalf("decoding test calendar: %v\n%s", err, s)
	}
	return cal
}

// mkcalendar creates a calendar with a raw MKCALENDAR: the go-webdav client
// has no call for it.
func mkcalendar(t *testing.T, ts *httptest.Server, href, user, name string) {
	t.Helper()
	body := `<?xml version="1.0"?><C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:set><D:prop><D:displayname>` + name + `</D:displayname>` +
		`<C:supported-calendar-component-set><C:comp name="VEVENT"/></C:supported-calendar-component-set>` +
		`</D:prop></D:set></C:mkcalendar>`
	if code, resp := rawDo(t, ts, "MKCALENDAR", href, user, nil, strings.NewReader(body)); code != http.StatusCreated {
		t.Fatalf("MKCALENDAR %s = %d %s", href, code, resp)
	}
}

// calObjPaths returns the sorted paths of objs.
func calObjPaths(objs []caldav.CalendarObject) []string {
	out := []string{}
	for _, o := range objs {
		out = append(out, o.Path)
	}
	slices.Sort(out)
	return out
}

// eventSummary returns the SUMMARY of the first VEVENT of cal.
func eventSummary(t *testing.T, cal *ical.Calendar) string {
	t.Helper()
	evs := cal.Events()
	if len(evs) == 0 {
		t.Fatal("calendar has no VEVENT")
	}
	s, err := evs[0].Props.Text(ical.PropSummary)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// discoverCalendars runs the client's discovery from its endpoint: current
// user principal, calendar home set, calendars.
func discoverCalendars(t *testing.T, ctx context.Context, c *caldav.Client) (home string, cals []caldav.Calendar) {
	t.Helper()
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal: %v", err)
	}
	if principal != "/dav/principals/alice/" {
		t.Fatalf("principal = %q", principal)
	}
	home, err = c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindCalendarHomeSet: %v", err)
	}
	if home != "/dav/calendars/alice/" {
		t.Fatalf("calendar home = %q", home)
	}
	cals, err = c.FindCalendars(ctx, home)
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}
	return home, cals
}

// timeQuery is a calendar-query for VEVENTs overlapping [start, end) that
// asks for the whole object.
func timeQuery(start, end time.Time) *caldav.CalendarQuery {
	return &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name: "VEVENT", Start: start.UTC(), End: end.UTC(),
		}}},
	}
}

// zonedEvent is a single event in Prague summer time: 1 July 2026 10:00
// local is 08:00 UTC.
const zonedEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + pragueZone +
	"BEGIN:VEVENT\r\nUID:zoned-1\r\nDTSTAMP:20260101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Prague:20260701T100000\r\nDTEND;TZID=Europe/Prague:20260701T110000\r\n" +
	"SUMMARY:Summer meeting\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// utcEvent returns a one-hour event in UTC.
func utcEvent(uid, start, summary string) string {
	s, err := time.Parse("20060102T150405Z", start)
	if err != nil {
		panic(err)
	}
	end := s.Add(time.Hour).Format("20060102T150405Z")
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:" + start + "\r\nDTEND:" + end +
		"\r\nSUMMARY:" + summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// TestInteropCalendarWellKnown starts at /.well-known/caldav (RFC 6764
// section 6) and follows the redirect by hand: the go-webdav client has no
// well-known support and net/http turns a redirected PROPFIND into a GET.
func TestInteropCalendarWellKnown(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	ts := startCalendarServer(t, st)
	mkcalendar(t, ts, "/dav/calendars/alice/work/", "alice", "Work")

	noFollow := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, method := range []string{"GET", "PROPFIND"} {
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+"/.well-known/caldav", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently {
			t.Fatalf("%s well-known = %d", method, resp.StatusCode)
		}
		base, _ := url.Parse(ts.URL + "/.well-known/caldav")
		loc, err := base.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		c := calClient(t, loc.String(), "alice")
		_, cals := discoverCalendars(t, ctx, c)
		if len(cals) != 1 || cals[0].Path != "/dav/calendars/alice/work/" || cals[0].Name != "Work" {
			t.Fatalf("calendars via well-known = %+v", cals)
		}
	}
}

func TestInteropCalendarReadWrite(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	ts := startCalendarServer(t, st)
	c := calClient(t, ts.URL+"/dav/", "alice")

	mkcalendar(t, ts, "/dav/calendars/alice/work/", "alice", "Work")
	home, cals := discoverCalendars(t, ctx, c)
	if len(cals) != 1 {
		t.Fatalf("calendars = %+v", cals)
	}
	cal := cals[0]
	if cal.Path != home+"work/" || cal.Name != "Work" || cal.MaxResourceSize <= 0 ||
		!slices.Equal(cal.SupportedComponentSet, []string{"VEVENT"}) {
		t.Errorf("calendar = %+v", cal)
	}
	work := cal.Path

	events := map[string]string{
		work + "zoned.ics":  zonedEvent,
		work + "weekly.ics": weeklyEvent,
		work + "lunch.ics":  utcEvent("lunch-1", "20260501T120000Z", "Lunch"),
	}
	etags := map[string]string{}
	for p, data := range events {
		co, err := c.PutCalendarObject(ctx, p, decodeCal(t, data))
		if err != nil {
			t.Fatalf("PutCalendarObject %s: %v", p, err)
		}
		if co.ETag == "" {
			t.Errorf("PutCalendarObject %s: no ETag", p)
		}
		etags[p] = co.ETag
	}

	// Get one back; the VTIMEZONE came through.
	co, err := c.GetCalendarObject(ctx, work+"zoned.ics")
	if err != nil {
		t.Fatalf("GetCalendarObject: %v", err)
	}
	if eventSummary(t, co.Data) != "Summer meeting" || co.ETag != etags[work+"zoned.ics"] {
		t.Errorf("GetCalendarObject: summary %q etag %q, want etag %q", eventSummary(t, co.Data), co.ETag, etags[work+"zoned.ics"])
	}
	if tz := co.Data.Children; !slices.ContainsFunc(tz, func(c *ical.Component) bool { return c.Name == ical.CompTimezone }) {
		t.Error("GetCalendarObject: VTIMEZONE lost")
	}
	if co.ModTime.IsZero() || co.ContentLength == 0 {
		t.Errorf("GetCalendarObject metadata = %v %d", co.ModTime, co.ContentLength)
	}

	// Time ranges: the zoned event at 08:00 UTC, the weekly one after the
	// change to summer time, nothing in between.
	for _, tt := range []struct {
		name       string
		start, end time.Time
		want       []string
	}{
		{"zoned at 08:00 UTC", at(2026, 7, 1, 8, 0), at(2026, 7, 1, 8, 30), []string{work + "zoned.ics"}},
		{"zoned is not at 10:00 UTC", at(2026, 7, 1, 10, 0), at(2026, 7, 1, 10, 30), nil},
		{"weekly in summer time", at(2026, 3, 30, 7, 0), at(2026, 3, 30, 7, 30), []string{work + "weekly.ics"}},
		{"weekly excluded day", at(2026, 4, 6, 0, 0), at(2026, 4, 7, 0, 0), nil},
		{"May", at(2026, 5, 1, 0, 0), at(2026, 6, 1, 0, 0), []string{work + "lunch.ics"}},
		{"the year", at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0), []string{work + "lunch.ics", work + "weekly.ics", work + "zoned.ics"}},
	} {
		found, err := c.QueryCalendar(ctx, work, timeQuery(tt.start, tt.end))
		if err != nil {
			t.Fatalf("QueryCalendar %s: %v", tt.name, err)
		}
		if got := calObjPaths(found); !slices.Equal(got, append([]string{}, tt.want...)) {
			t.Errorf("QueryCalendar %s = %v, want %v", tt.name, got, tt.want)
		}
	}

	// A UID prop-filter.
	found, err := c.QueryCalendar(ctx, work, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Props: []caldav.PropFilter{{Name: ical.PropUID, TextMatch: &caldav.TextMatch{Text: "weekly-1"}}},
		}}},
	})
	if err != nil {
		t.Fatalf("QueryCalendar UID: %v", err)
	}
	if got := calObjPaths(found); !slices.Equal(got, []string{work + "weekly.ics"}) {
		t.Errorf("QueryCalendar UID = %v", got)
	} else if eventSummary(t, found[0].Data) != "Standup" || found[0].ETag != etags[work+"weekly.ics"] {
		t.Errorf("QueryCalendar UID object = %+v", found[0])
	}
	// A negated one.
	found, err = c.QueryCalendar(ctx, work, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR"},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Props: []caldav.PropFilter{{Name: ical.PropUID, TextMatch: &caldav.TextMatch{Text: "weekly", NegateCondition: true}}},
		}}},
	})
	if err != nil {
		t.Fatalf("QueryCalendar negated UID: %v", err)
	}
	if got := calObjPaths(found); !slices.Equal(got, []string{work + "lunch.ics", work + "zoned.ics"}) {
		t.Errorf("QueryCalendar negated UID = %v", got)
	}

	// Multiget two of them.
	got, err := c.MultiGetCalendar(ctx, work, &caldav.CalendarMultiGet{
		Paths:       []string{work + "zoned.ics", work + "lunch.ics"},
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
	})
	if err != nil {
		t.Fatalf("MultiGetCalendar: %v", err)
	}
	if paths := calObjPaths(got); !slices.Equal(paths, []string{work + "lunch.ics", work + "zoned.ics"}) {
		t.Errorf("MultiGetCalendar = %v", paths)
	}
	for _, o := range got {
		if o.ETag != etags[o.Path] {
			t.Errorf("MultiGetCalendar %s etag %q, want %q", o.Path, o.ETag, etags[o.Path])
		}
	}

	// Replace one: a new ETag and the new data.
	co, err = c.PutCalendarObject(ctx, work+"lunch.ics", decodeCal(t, utcEvent("lunch-1", "20260501T130000Z", "Late lunch")))
	if err != nil {
		t.Fatalf("PutCalendarObject replace: %v", err)
	}
	if co.ETag == "" || co.ETag == etags[work+"lunch.ics"] {
		t.Errorf("replace ETag = %q, old %q", co.ETag, etags[work+"lunch.ics"])
	}
	if co, err := c.GetCalendarObject(ctx, work+"lunch.ics"); err != nil || eventSummary(t, co.Data) != "Late lunch" {
		t.Errorf("GetCalendarObject after replace = %v", err)
	}

	// The same UID under another name is refused.
	if _, err := c.PutCalendarObject(ctx, work+"copy.ics", decodeCal(t, weeklyEvent)); err == nil {
		t.Error("a second object with the same UID was accepted")
	}

	// Remove one; it is gone and no query finds it.
	if err := c.RemoveAll(ctx, work+"weekly.ics"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := c.GetCalendarObject(ctx, work+"weekly.ics"); err == nil {
		t.Error("GetCalendarObject after RemoveAll succeeded")
	}
	found, err = c.QueryCalendar(ctx, work, timeQuery(at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if got := calObjPaths(found); !slices.Equal(got, []string{work + "lunch.ics", work + "zoned.ics"}) {
		t.Errorf("QueryCalendar after remove = %v", got)
	}

	// Another user's calendar is out of reach.
	bob := calClient(t, ts.URL+"/dav/", "bob")
	if _, err := bob.GetCalendarObject(ctx, work+"zoned.ics"); err == nil {
		t.Error("bob read alice's event")
	}
	if _, err := bob.FindCalendars(ctx, home); err == nil {
		t.Error("bob listed alice's calendars")
	}
	if _, err := bob.QueryCalendar(ctx, work, timeQuery(at(2026, 1, 1, 0, 0), at(2027, 1, 1, 0, 0))); err == nil {
		t.Error("bob queried alice's calendar")
	}

	// Removing the whole calendar.
	if err := c.RemoveAll(ctx, work); err != nil {
		t.Fatalf("RemoveAll calendar: %v", err)
	}
	if _, cals := discoverCalendars(t, ctx, c); len(cals) != 0 {
		t.Errorf("calendars after delete = %+v", cals)
	}
}

// at returns the UTC time of the given day, hour and minute.
func at(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// syncResult is the part of a sync-collection answer the tests look at.
type syncResult struct {
	updated, deleted []string
	data             map[string]string // calendar-data by href
	token            string
}

// calSync runs a sync-collection REPORT on href with token ("" for an
// initial sync), asking for ETags and calendar data, and returns the
// changed and removed hrefs and the new token. A status other than 207
// returns the status with an empty result.
func calSync(t *testing.T, ts *httptest.Server, href, token string) (int, syncResult) {
	t.Helper()
	body := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:sync-token>` + token + `</D:sync-token><D:sync-level>1</D:sync-level>` +
		`<D:prop><D:getetag/><C:calendar-data/></D:prop></D:sync-collection>`
	code, resp := rawDo(t, ts, "REPORT", href, "alice", nil, strings.NewReader(body))
	if code != http.StatusMultiStatus {
		return code, syncResult{}
	}
	var ms struct {
		Responses []struct {
			Href      string `xml:"href"`
			Status    string `xml:"status"`
			Propstats []struct {
				Status string `xml:"status"`
				ETag   string `xml:"prop>getetag"`
				Data   string `xml:"prop>calendar-data"`
			} `xml:"propstat"`
		} `xml:"response"`
		Token string `xml:"sync-token"`
	}
	if err := xml.Unmarshal([]byte(resp), &ms); err != nil {
		t.Fatalf("sync answer: %v\n%s", err, resp)
	}
	res := syncResult{updated: []string{}, deleted: []string{}, data: map[string]string{}, token: ms.Token}
	for _, r := range ms.Responses {
		if strings.Contains(r.Status, "404") {
			res.deleted = append(res.deleted, r.Href)
			continue
		}
		ok := false
		for _, ps := range r.Propstats {
			if strings.Contains(ps.Status, "200") && ps.ETag != "" {
				ok = true
				res.data[r.Href] = ps.Data
			}
		}
		if !ok {
			t.Errorf("sync response for %s lacks an ETag: %+v", r.Href, r.Propstats)
		}
		res.updated = append(res.updated, r.Href)
	}
	slices.Sort(res.updated)
	slices.Sort(res.deleted)
	return code, res
}

func TestInteropCalendarSync(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	ts := startCalendarServer(t, st)
	c := calClient(t, ts.URL+"/dav/", "alice")
	work := "/dav/calendars/alice/work/"
	mkcalendar(t, ts, work, "alice", "Work")

	for _, n := range []string{"a", "b", "c"} {
		if _, err := c.PutCalendarObject(ctx, work+n+".ics", decodeCal(t, utcEvent("uid-"+n, "20260601T100000Z", n))); err != nil {
			t.Fatal(err)
		}
	}
	code, first := calSync(t, ts, work, "")
	if code != http.StatusMultiStatus {
		t.Fatalf("initial sync = %d", code)
	}
	if !slices.Equal(first.updated, []string{work + "a.ics", work + "b.ics", work + "c.ics"}) || len(first.deleted) != 0 ||
		!strings.HasPrefix(first.token, "urn:groupkit:sync:") {
		t.Fatalf("initial sync = %+v", first)
	}
	if got := first.data[work+"b.ics"]; !strings.Contains(got, "UID:uid-b") {
		t.Errorf("initial sync data for b.ics = %q", got)
	}
	if _, same := calSync(t, ts, work, first.token); len(same.updated)+len(same.deleted) != 0 || same.token != first.token {
		t.Errorf("unchanged sync = %+v", same)
	}

	if _, err := c.PutCalendarObject(ctx, work+"d.ics", decodeCal(t, utcEvent("uid-d", "20260601T100000Z", "d"))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutCalendarObject(ctx, work+"a.ics", decodeCal(t, utcEvent("uid-a", "20260601T110000Z", "A"))); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, work+"b.ics"); err != nil {
		t.Fatal(err)
	}
	_, next := calSync(t, ts, work, first.token)
	if !slices.Equal(next.updated, []string{work + "a.ics", work + "d.ics"}) || !slices.Equal(next.deleted, []string{work + "b.ics"}) ||
		next.token == first.token {
		t.Errorf("incremental sync = %+v", next)
	}
	if code, _ := calSync(t, ts, work, "urn:groupkit:sync:nope:1"); code != http.StatusForbidden {
		t.Errorf("foreign token = %d, want 403", code)
	}
}
