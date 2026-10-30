// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

func propfind0(t *testing.T, h http.Handler, target, user string) multistatus {
	t.Helper()
	return parseMS(t, do(t, h, "PROPFIND", target, user, map[string]string{"Depth": "0"}, propfindBody))
}

func TestPrincipalHomeSets(t *testing.T) {
	tests := []struct {
		name         string
		books, cals  bool // disabled
		wantBook     bool
		wantCalendar bool
	}{
		{"both", false, false, true, true},
		{"no calendars", false, true, true, false},
		{"no address books", true, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("/dav")
			cfg.DisableAddressBooks = tt.books
			cfg.DisableCalendars = tt.cals
			h := newServer(t, cfg).Handler()
			ms := propfind0(t, h, "/dav/principals/alice/", "alice")
			book, _, _, _ := ms.prop("addressbook-home-set")
			cal, _, _, st := ms.prop("calendar-home-set")
			if tt.wantBook != (len(book) == 1 && book[0] == "/dav/addressbooks/alice/") {
				t.Errorf("addressbook-home-set = %v, want present %v", book, tt.wantBook)
			}
			if tt.wantCalendar != (len(cal) == 1 && cal[0] == "/dav/calendars/alice/" && strings.Contains(st, "200")) {
				t.Errorf("calendar-home-set = %v, want present %v", cal, tt.wantCalendar)
			}
			if _, _, inner, _ := ms.prop("supported-report-set"); strings.TrimSpace(inner) != "" {
				t.Errorf("supported-report-set = %q", inner)
			}
		})
	}
}

func TestMountsFollowDisableFlags(t *testing.T) {
	tests := []struct {
		name        string
		books, cals bool // disabled
		wantBook    int
		wantCal     int
	}{
		{"both", false, false, 207, 207},
		{"no calendars", false, true, 207, 404},
		{"no address books", true, false, 404, 207},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("/dav")
			cfg.DisableAddressBooks = tt.books
			cfg.DisableCalendars = tt.cals
			h := newServer(t, cfg).Handler()
			for target, want := range map[string]int{
				"/dav/addressbooks/alice/": tt.wantBook,
				"/dav/calendars/alice/":    tt.wantCal,
				"/dav/calendars/alice/c/":  tt.wantCal,
			} {
				got := do(t, h, "PROPFIND", target, "alice", nil, propfindBody).Code
				if want == 404 && got != 404 || want == 207 && target != "/dav/calendars/alice/c/" && got != 207 {
					t.Errorf("PROPFIND %s = %d, want %d", target, got, want)
				}
			}
			// OPTIONS on a home works where the protocol is on.
			if tt.wantCal == 207 {
				w := do(t, h, "OPTIONS", "/dav/calendars/alice/", "alice", nil, "")
				if w.Code != 200 || !strings.Contains(w.Header().Get("DAV"), "calendar-access") {
					t.Errorf("OPTIONS calendars = %d %q", w.Code, w.Header().Get("DAV"))
				}
			}
		})
	}
}

func TestBothDisabledIsAnError(t *testing.T) {
	cfg := testConfig("/dav")
	cfg.DisableAddressBooks = true
	cfg.DisableCalendars = true
	s, err := groupkit.NewServer(cfg)
	if err == nil || s != nil || !strings.Contains(err.Error(), "disable everything") {
		t.Fatalf("NewServer = %v, %v", s, err)
	}
}

func TestDAVHeaderCombinations(t *testing.T) {
	tests := []struct {
		name        string
		books, cals bool // disabled
		want        string
	}{
		{"both", false, false, "1, 3, addressbook, extended-mkcol, calendar-access"},
		{"address books only", false, true, "1, 3, addressbook, extended-mkcol"},
		{"calendars only", true, false, "1, 3, calendar-access, extended-mkcol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("/dav")
			cfg.DisableAddressBooks = tt.books
			cfg.DisableCalendars = tt.cals
			h := newServer(t, cfg).Handler()
			for _, target := range []string{"/dav/", "/dav/principals/alice/", "*"} {
				w := do(t, h, "OPTIONS", target, "alice", nil, "")
				if got := w.Header().Get("DAV"); got != tt.want {
					t.Errorf("OPTIONS %s DAV = %q, want %q", target, got, tt.want)
				}
			}
		})
	}
}

func TestCalendarAccessControl(t *testing.T) {
	var calls int
	cfg := testConfig("/dav")
	inner := cfg.Authorize
	cfg.Authorize = func(r *http.Request, u string) (string, error) {
		calls++
		return inner(r, u)
	}
	h := newServer(t, cfg).Handler()

	w := do(t, h, "PROPFIND", "/dav/calendars/alice/", "", nil, propfindBody)
	if w.Code != 401 || w.Header().Get("WWW-Authenticate") != testChallenge {
		t.Errorf("anonymous = %d %v", w.Code, w.Header())
	}
	calls = 0
	if w := do(t, h, "PROPFIND", "/dav/calendars/alice/", "alice", nil, propfindBody); w.Code != 207 || calls != 1 {
		t.Errorf("own home = %d, Authorize calls %d", w.Code, calls)
	}
	for _, target := range []string{"/dav/calendars/alice/", "/dav/calendars/alice/work/", "/dav/calendars/alice/work/a.ics"} {
		if w := do(t, h, "PROPFIND", target, "bob", nil, propfindBody); w.Code != 403 {
			t.Errorf("bob %s = %d", target, w.Code)
		}
	}
	if w := do(t, h, "PROPFIND", "/dav/principals/alice/", "bob", nil, propfindBody); w.Code != 403 {
		t.Errorf("bob principal = %d", w.Code)
	}
	calls = 0
	wk := newServer(t, cfg).WellKnown()
	if w := do(t, wk, "GET", "/.well-known/caldav", "", nil, ""); w.Code != http.StatusMovedPermanently || calls != 0 {
		t.Errorf("well-known = %d, calls %d", w.Code, calls)
	}
}

func listKinds(t *testing.T, st store.Store, owner string, kind store.Kind) []store.Collection {
	t.Helper()
	cs, err := st.ListCollections(t.Context(), owner, kind)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestEnsureDefaultCollectionsBothKinds(t *testing.T) {
	ctx := t.Context()
	t.Run("one of each, idempotent", func(t *testing.T) {
		st := memory.New()
		for range 2 {
			if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
				t.Fatal(err)
			}
		}
		cals := listKinds(t, st, "alice", store.KindCalendar)
		if len(cals) != 1 || cals[0].Ref.Name != "personal" || cals[0].Props.DisplayName != "Personal" ||
			!slices.Equal(cals[0].Props.Components, []string{"VEVENT"}) {
			t.Fatalf("calendars = %+v", cals)
		}
		if books := listKinds(t, st, "alice", store.KindAddressBook); len(books) != 1 {
			t.Fatalf("books = %+v", books)
		}
		if got := listKinds(t, st, "bob", store.KindCalendar); len(got) != 0 {
			t.Errorf("other owner got %v", got)
		}
	})
	t.Run("existing differently named calendar", func(t *testing.T) {
		st := memory.New()
		ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
		if _, err := st.CreateCollection(ctx, ref, store.Props{DisplayName: "Work"}); err != nil {
			t.Fatal(err)
		}
		if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
			t.Fatal(err)
		}
		if cs := listKinds(t, st, "alice", store.KindCalendar); len(cs) != 1 || cs[0].Ref.Name != "work" {
			t.Fatalf("calendars = %+v", cs)
		}
		if books := listKinds(t, st, "alice", store.KindAddressBook); len(books) != 1 {
			t.Fatalf("books = %+v", books)
		}
	})
	t.Run("a book but no calendar adds only the calendar", func(t *testing.T) {
		st := memory.New()
		ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "work"}
		if _, err := st.CreateCollection(ctx, ref, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
			t.Fatal(err)
		}
		books := listKinds(t, st, "alice", store.KindAddressBook)
		if len(books) != 1 || books[0].Ref.Name != "work" {
			t.Fatalf("books = %+v", books)
		}
		if cals := listKinds(t, st, "alice", store.KindCalendar); len(cals) != 1 {
			t.Fatalf("calendars = %+v", cals)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		st := memory.New()
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for range cap(errs) {
			wg.Go(func() { errs <- groupkit.EnsureDefaultCollections(ctx, st, "alice") })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Error(err)
			}
		}
		if len(listKinds(t, st, "alice", store.KindCalendar)) != 1 || len(listKinds(t, st, "alice", store.KindAddressBook)) != 1 {
			t.Fatal("want exactly one of each")
		}
	})
}

// lostRace lists nothing and refuses every create with ErrExists, counting
// the creates per kind.
type lostRace struct {
	store.Store
	mu      sync.Mutex
	creates map[store.Kind]int
}

func (*lostRace) ListCollections(context.Context, string, store.Kind) ([]store.Collection, error) {
	return nil, nil
}

func (l *lostRace) CreateCollection(_ context.Context, ref store.CollectionRef, _ store.Props) (store.Collection, error) {
	l.mu.Lock()
	l.creates[ref.Kind]++
	l.mu.Unlock()
	return store.Collection{}, store.ErrExists
}

func TestEnsureDefaultCollectionsLosingTheRaceForBothKinds(t *testing.T) {
	st := &lostRace{creates: map[store.Kind]int{}}
	if err := groupkit.EnsureDefaultCollections(t.Context(), st, "alice"); err != nil {
		t.Fatalf("ErrExists must be ignored: %v", err)
	}
	if st.creates[store.KindAddressBook] != 1 || st.creates[store.KindCalendar] != 1 {
		t.Errorf("creates = %v", st.creates)
	}
}

func TestServerEnsureDefaultCollectionsRespectsFlags(t *testing.T) {
	tests := []struct {
		name        string
		books, cals bool // disabled
		wantBooks   int
		wantCals    int
	}{
		{"both", false, false, 1, 1},
		{"no calendars", false, true, 1, 0},
		{"no address books", true, false, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig("/dav")
			cfg.DisableAddressBooks = tt.books
			cfg.DisableCalendars = tt.cals
			srv := newServer(t, cfg)
			for range 2 {
				if err := srv.EnsureDefaultCollections(t.Context(), "alice"); err != nil {
					t.Fatal(err)
				}
			}
			if got := len(listKinds(t, cfg.Store, "alice", store.KindAddressBook)); got != tt.wantBooks {
				t.Errorf("books = %d, want %d", got, tt.wantBooks)
			}
			if got := len(listKinds(t, cfg.Store, "alice", store.KindCalendar)); got != tt.wantCals {
				t.Errorf("calendars = %d, want %d", got, tt.wantCals)
			}
		})
	}
	if err := newServer(t, testConfig("/dav")).EnsureDefaultCollections(t.Context(), ""); err == nil {
		t.Error("empty owner: want an error")
	}
}

const pragueZone = "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

// weekly: Mondays 09:00 Prague from 23 March 2026, four of them, the third
// (6 April) excluded. 29 March 2026 is the change to summer time.
const weeklyEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + pragueZone +
	"BEGIN:VEVENT\r\nUID:weekly-1\r\nDTSTAMP:20260101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Prague:20260323T090000\r\nDTEND;TZID=Europe/Prague:20260323T100000\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=4\r\nEXDATE;TZID=Europe/Prague:20260406T090000\r\nSUMMARY:Standup\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func windowQuery(start, end string) string {
	return `<?xml version="1.0"?><C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">` +
		`<D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">` +
		`<C:time-range start="` + start + `" end="` + end + `"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`
}

func TestCalendarFullWalk(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	user := "alice"

	ms := propfind0(t, h, "/dav/", user)
	principal, _, _, _ := ms.prop("current-user-principal")
	if len(principal) != 1 {
		t.Fatalf("no principal in %+v", ms)
	}
	ms = propfind0(t, h, principal[0], user)
	home, _, _, _ := ms.prop("calendar-home-set")
	if len(home) != 1 {
		t.Fatalf("no calendar home in %+v", ms)
	}
	cal := home[0] + "work/"

	if w := do(t, h, "MKCALENDAR", cal, user, nil, ""); w.Code != http.StatusCreated {
		t.Fatalf("MKCALENDAR = %d %s", w.Code, w.Body)
	}
	ms = parseMS(t, do(t, h, "PROPFIND", home[0], user, map[string]string{"Depth": "1"}, propfindBody))
	if got := ms.hrefs(); len(got) != 2 || got[1] != cal {
		t.Fatalf("home hrefs = %v", got)
	}

	obj := cal + "weekly.ics"
	w := do(t, h, "PUT", obj, user, map[string]string{"Content-Type": "text/calendar"}, weeklyEvent)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") == "" {
		t.Fatalf("PUT = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := do(t, h, "GET", obj, user, nil, ""); w.Code != 200 || w.Body.String() != weeklyEvent {
		t.Fatalf("GET = %d %q", w.Code, w.Body)
	}

	// Occurrences: 23 March (winter), 30 March and 13 April (summer); the
	// 6 April one is excluded.
	for _, tt := range []struct {
		name       string
		start, end string
		want       bool
	}{
		{"first, winter time", "20260323T080000Z", "20260323T083000Z", true},
		{"first is not at summer time", "20260323T070000Z", "20260323T073000Z", false},
		{"after the change, summer time", "20260330T070000Z", "20260330T073000Z", true},
		{"after the change is not winter time", "20260330T080000Z", "20260330T083000Z", false},
		{"excluded day", "20260406T000000Z", "20260407T000000Z", false},
		{"last", "20260413T070000Z", "20260413T073000Z", true},
		{"after the last", "20260420T000000Z", "20260501T000000Z", false},
	} {
		ms := parseMS(t, do(t, h, "REPORT", cal, user, map[string]string{"Depth": "1"}, windowQuery(tt.start, tt.end)))
		if got := slices.Equal(ms.hrefs(), []string{obj}); got != tt.want {
			t.Errorf("%s: hrefs %v, want match %v", tt.name, ms.hrefs(), tt.want)
		}
	}

	syncBody := func(token string) string {
		return `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token>` + token +
			`</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	}
	w = do(t, h, "REPORT", cal, user, nil, syncBody(""))
	ms = parseMS(t, w)
	if got := ms.hrefs(); len(got) != 1 || got[0] != obj {
		t.Fatalf("initial sync hrefs = %v", got)
	}
	var tok struct {
		Token string `xml:"sync-token"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("no sync token: %v\n%s", err, w.Body)
	}
	other := strings.Replace(weeklyEvent, "weekly-1", "weekly-2", 1)
	if w := do(t, h, "PUT", cal+"second.ics", user, nil, other); w.Code != http.StatusCreated {
		t.Fatalf("PUT 2 = %d %s", w.Code, w.Body)
	}
	w = do(t, h, "REPORT", cal, user, nil, syncBody(tok.Token))
	ms = parseMS(t, w)
	if got := ms.hrefs(); len(got) != 1 || got[0] != cal+"second.ics" {
		t.Fatalf("incremental sync hrefs = %v", got)
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("no sync token after the increment: %v\n%s", err, w.Body)
	}
	afterPut := tok.Token

	for _, target := range []string{obj, cal, home[0]} {
		if w := do(t, h, "GET", target, "bob", nil, ""); w.Code != http.StatusForbidden {
			t.Errorf("bob GET %s = %d", target, w.Code)
		}
	}

	if w := do(t, h, "DELETE", obj, user, nil, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE object = %d", w.Code)
	}
	if w := do(t, h, "GET", obj, user, nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d", w.Code)
	}
	// The delete shows up in the next incremental sync as a 404 response.
	w = do(t, h, "REPORT", cal, user, nil, syncBody(afterPut))
	ms = parseMS(t, w)
	if got := ms.hrefs(); len(got) != 1 || got[0] != obj {
		t.Fatalf("sync after delete hrefs = %v", got)
	}
	var del struct {
		Responses []struct {
			Status string `xml:"status"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &del); err != nil || len(del.Responses) != 1 ||
		!strings.Contains(del.Responses[0].Status, "404") {
		t.Fatalf("sync after delete: %v\n%s", err, w.Body)
	}
	if w := do(t, h, "DELETE", cal, user, nil, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE calendar = %d", w.Code)
	}
	ms = parseMS(t, do(t, h, "PROPFIND", home[0], user, map[string]string{"Depth": "1"}, propfindBody))
	if len(ms.hrefs()) != 1 {
		t.Fatalf("home hrefs after delete = %v", ms.hrefs())
	}
}

func TestDisabledProtocolNeverAsksTheCallbacks(t *testing.T) {
	for _, tt := range []struct {
		name         string
		books, cals  bool // disabled
		disabledPath string
	}{
		{"no calendars", false, true, "/dav/calendars/alice/"},
		{"no address books", true, false, "/dav/addressbooks/alice/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			cfg := testConfig("/dav")
			cfg.DisableAddressBooks, cfg.DisableCalendars = tt.books, tt.cals
			currentUser, authorize := cfg.CurrentUser, cfg.Authorize
			cfg.CurrentUser = func(r *http.Request) (string, error) {
				calls.Add(1)
				return currentUser(r)
			}
			cfg.Authorize = func(r *http.Request, u string) (string, error) {
				calls.Add(1)
				return authorize(r, u)
			}
			h := newServer(t, cfg).Handler()
			for _, user := range []string{"", "alice", "bob"} {
				for _, method := range []string{"PROPFIND", "GET", "PUT", "DELETE", "REPORT", "OPTIONS"} {
					for _, p := range []string{tt.disabledPath, tt.disabledPath + "x/", tt.disabledPath + "x/o"} {
						if w := do(t, h, method, p, user, nil, ""); w.Code != http.StatusNotFound {
							t.Errorf("%s %s as %q = %d, want 404", method, p, user, w.Code)
						}
					}
				}
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("callbacks ran %d times for a disabled protocol", n)
			}
		})
	}
}

func TestCalendarsWithEmptyPrefix(t *testing.T) {
	h := newServer(t, testConfig("")).Handler()
	if w := do(t, h, "MKCALENDAR", "/calendars/alice/work/", "alice", nil, ""); w.Code != http.StatusCreated {
		t.Fatalf("MKCALENDAR = %d %s", w.Code, w.Body)
	}
	obj := "/calendars/alice/work/weekly.ics"
	if w := do(t, h, "PUT", obj, "alice", nil, weeklyEvent); w.Code != http.StatusCreated {
		t.Fatalf("PUT = %d %s", w.Code, w.Body)
	}
	ms := parseMS(t, do(t, h, "PROPFIND", "/calendars/alice/work/", "alice", map[string]string{"Depth": "1"}, propfindBody))
	if got := ms.hrefs(); !slices.Equal(got, []string{"/calendars/alice/work/", obj}) {
		t.Errorf("hrefs = %v", got)
	}
	ms = propfind0(t, h, "/principals/alice/", "alice")
	if home, _, _, _ := ms.prop("calendar-home-set"); !slices.Equal(home, []string{"/calendars/alice/"}) {
		t.Errorf("calendar-home-set = %v", home)
	}
	if w := do(t, h, "GET", "/dav/calendars/alice/work/", "alice", nil, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET under a prefix that is not mounted = %d", w.Code)
	}
}
