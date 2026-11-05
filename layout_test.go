// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/caldav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
)

// usersPrefix is the prefix the users segment layout is tested under. It has
// a dot in a segment, as the prefixes of older servers often do.
const usersPrefix = "/remote.php/dav"

// startUsersServer serves a Server with the users segment layout under
// usersPrefix, with Basic auth from basicUsers.
func startUsersServer(t *testing.T, st store.Store) *httptest.Server {
	t.Helper()
	cfg := basicConfig(st)
	cfg.Prefix = usersPrefix
	cfg.UsersSegment = true
	srv := newServer(t, cfg)
	mux := http.NewServeMux()
	mux.Handle(usersPrefix+"/", srv.Handler())
	mux.Handle(usersPrefix, srv.Handler())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	mux.Handle("/.well-known/caldav", srv.WellKnown())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestUsersSegmentDefaultOff checks that the default layout has no users
// segment, so hrefs of existing deployments do not change.
func TestUsersSegmentDefaultOff(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/", "alice", map[string]string{"Depth": "0"}, propfindBody))
	if hrefs, _, _, _ := ms.prop("current-user-principal"); !slices.Equal(hrefs, []string{"/dav/principals/alice/"}) {
		t.Errorf("current-user-principal = %v", hrefs)
	}
	if w := do(t, h, "PROPFIND", "/dav/principals/users/alice/", "alice", nil, propfindBody); w.Code != http.StatusNotFound {
		t.Errorf("principal below users = %d, want 404", w.Code)
	}
	if w := do(t, h, "PROPFIND", "/dav/addressbooks/users/alice/", "alice", nil, propfindBody); w.Code != http.StatusForbidden {
		// "users" is read as a user name, which is not alice's.
		t.Errorf("address book home below users = %d, want 403", w.Code)
	}
}

// TestUsersSegmentPrincipalPaths checks which principal paths the users
// segment layout serves and which it refuses.
func TestUsersSegmentPrincipalPaths(t *testing.T) {
	cfg := testConfig("/dav")
	cfg.UsersSegment = true
	h := newServer(t, cfg).Handler()

	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/", "alice", map[string]string{"Depth": "1"}, propfindBody))
	if got := ms.hrefs(); !slices.Equal(got, []string{"/dav/", "/dav/principals/users/alice/"}) {
		t.Errorf("root depth 1 hrefs = %v", got)
	}
	if hrefs, _, _, _ := ms.prop("current-user-principal"); !slices.Equal(hrefs, []string{"/dav/principals/users/alice/"}) {
		t.Errorf("current-user-principal = %v", hrefs)
	}

	ms = parseMS(t, do(t, h, "PROPFIND", "/dav/principals/users/alice/", "alice", map[string]string{"Depth": "0"}, propfindBody))
	for prop, want := range map[string]string{
		"principal-URL":          "/dav/principals/users/alice/",
		"owner":                  "/dav/principals/users/alice/",
		"current-user-principal": "/dav/principals/users/alice/",
		"addressbook-home-set":   "/dav/addressbooks/users/alice/",
		"calendar-home-set":      "/dav/calendars/alice/",
	} {
		if hrefs, _, _, status := ms.prop(prop); !slices.Equal(hrefs, []string{want}) || !strings.Contains(status, "200") {
			t.Errorf("%s = %v %s, want %s", prop, hrefs, status, want)
		}
	}

	for target, want := range map[string]int{
		"/dav/principals/alice/":            http.StatusNotFound,
		"/dav/principals/users/":            http.StatusNotFound,
		"/dav/principals/users":             http.StatusNotFound,
		"/dav/principals/users/alice/extra": http.StatusNotFound,
		"/dav/principals/users/alice/x/y":   http.StatusNotFound,
		"/dav/principals/users/bob/":        http.StatusForbidden,
		"/dav/principals/users/%2e%2e/":     http.StatusBadRequest,
		"/dav/principals/users/a%2Fb/":      http.StatusBadRequest,
		"/dav/principals/usersx/alice/":     http.StatusNotFound,
		"/dav/principals/users/alice":       http.StatusMultiStatus,
	} {
		if w := do(t, h, "PROPFIND", target, "alice", nil, propfindBody); w.Code != want {
			t.Errorf("%s = %d, want %d", target, w.Code, want)
		}
	}
}

// TestUsersSegmentAddressBooks runs discovery, CRUD, multiget and sync with
// the go-webdav CardDAV client against the users segment layout.
func TestUsersSegmentAddressBooks(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startUsersServer(t, st)
	c := cardClient(t, ts.URL+usersPrefix+"/", "alice")

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal: %v", err)
	}
	if principal != usersPrefix+"/principals/users/alice/" {
		t.Fatalf("principal = %q", principal)
	}
	home, err := c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindAddressBookHomeSet: %v", err)
	}
	if home != usersPrefix+"/addressbooks/users/alice/" {
		t.Fatalf("home = %q", home)
	}
	books, err := c.FindAddressBooks(ctx, home)
	if err != nil {
		t.Fatalf("FindAddressBooks: %v", err)
	}
	if got := bookPaths(books); !slices.Equal(got, []string{home + "contacts/"}) {
		t.Fatalf("books = %v", got)
	}

	if err := c.Mkdir(ctx, home+"friends/"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	book := home + "friends/"
	for _, n := range []string{"a", "b"} {
		if _, err := c.PutAddressObject(ctx, book+n+".vcf", testCard("uid-"+n, "Person "+n)); err != nil {
			t.Fatalf("PutAddressObject: %v", err)
		}
	}
	ao, err := c.GetAddressObject(ctx, book+"a.vcf")
	if err != nil {
		t.Fatalf("GetAddressObject: %v", err)
	}
	if ao.Path != book+"a.vcf" || ao.Card.PreferredValue(vcard.FieldUID) != "uid-a" {
		t.Errorf("GetAddressObject = %q %q", ao.Path, ao.Card.PreferredValue(vcard.FieldUID))
	}

	got, err := c.MultiGetAddressBook(ctx, book, &carddav.AddressBookMultiGet{
		Paths:       []string{book + "b.vcf"},
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		t.Fatalf("MultiGetAddressBook: %v", err)
	}
	if paths := objPaths(got); !slices.Equal(paths, []string{book + "b.vcf"}) {
		t.Errorf("multiget = %v", paths)
	}

	first, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
	})
	if err != nil {
		t.Fatalf("SyncCollection: %v", err)
	}
	if paths := objPaths(first.Updated); !slices.Equal(paths, []string{book + "a.vcf", book + "b.vcf"}) {
		t.Errorf("sync updated = %v", paths)
	}
	if err := c.RemoveAll(ctx, book+"a.vcf"); err != nil {
		t.Fatal(err)
	}
	next, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: first.SyncToken})
	if err != nil {
		t.Fatalf("SyncCollection incremental: %v", err)
	}
	if !slices.Equal(next.Deleted, []string{book + "a.vcf"}) {
		t.Errorf("sync deleted = %v", next.Deleted)
	}
	if err := c.RemoveAll(ctx, book); err != nil {
		t.Fatalf("RemoveAll book: %v", err)
	}
	if _, err := c.GetAddressObject(ctx, book+"b.vcf"); err == nil {
		t.Error("object readable after the book was deleted")
	}

	// The flat paths are not served in this layout.
	for _, p := range []string{usersPrefix + "/addressbooks/alice/contacts/", usersPrefix + "/principals/alice/"} {
		if code, _ := rawDo(t, ts, "PROPFIND", p, "alice", http.Header{"Depth": {"0"}}, strings.NewReader(propfindBody)); code == http.StatusMultiStatus {
			t.Errorf("PROPFIND %s = 207, want a refusal", p)
		}
	}
}

// TestUsersSegmentCalendars checks that calendars keep the flat layout while
// the principal they point at has the users segment.
func TestUsersSegmentCalendars(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startUsersServer(t, st)
	c := calClient(t, ts.URL+usersPrefix+"/", "alice")

	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal: %v", err)
	}
	if principal != usersPrefix+"/principals/users/alice/" {
		t.Fatalf("principal = %q", principal)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindCalendarHomeSet: %v", err)
	}
	if home != usersPrefix+"/calendars/alice/" {
		t.Fatalf("calendar home = %q", home)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil {
		t.Fatalf("FindCalendars: %v", err)
	}
	if len(cals) != 1 || cals[0].Path != home+"personal/" {
		t.Fatalf("calendars = %+v", cals)
	}
	cal := cals[0].Path
	if _, err := c.PutCalendarObject(ctx, cal+"e1.ics", decodeCal(t, utcEvent("e1", "20260301T090000Z", "One"))); err != nil {
		t.Fatalf("PutCalendarObject: %v", err)
	}
	got, err := c.MultiGetCalendar(ctx, cal, &caldav.CalendarMultiGet{
		Paths:       []string{cal + "e1.ics"},
		CompRequest: caldav.CalendarCompRequest{AllProps: true, AllComps: true},
	})
	if err != nil {
		t.Fatalf("MultiGetCalendar: %v", err)
	}
	if paths := calObjPaths(got); !slices.Equal(paths, []string{cal + "e1.ics"}) {
		t.Errorf("multiget = %v", paths)
	}
	code, res := calSync(t, ts, cal, "")
	if code != http.StatusMultiStatus || !slices.Equal(res.updated, []string{cal + "e1.ics"}) {
		t.Errorf("sync = %d %v", code, res.updated)
	}

	// The calendar home carries the principal of this layout.
	code, resp := rawDo(t, ts, "PROPFIND", home, "alice", http.Header{"Depth": {"0"}}, strings.NewReader(propfindBody))
	if code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s = %d %s", home, code, resp)
	}
	var ms multistatus
	if err := xml.Unmarshal([]byte(resp), &ms); err != nil {
		t.Fatalf("bad multistatus: %v\n%s", err, resp)
	}
	for _, prop := range []string{"current-user-principal", "owner"} {
		if hrefs, _, _, _ := ms.prop(prop); !slices.Equal(hrefs, []string{usersPrefix + "/principals/users/alice/"}) {
			t.Errorf("%s of the calendar home = %v", prop, hrefs)
		}
	}
}
