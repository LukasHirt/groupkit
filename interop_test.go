// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/filestore"
)

// basicUsers are the accounts of the Basic auth test servers.
var basicUsers = map[string]string{"alice": "alice-pw", "bob": "bob-pw"}

// basicConfig returns a Config over st that authenticates with HTTP Basic
// against basicUsers. A user may only reach their own data.
func basicConfig(st store.Store) groupkit.Config {
	current := func(r *http.Request) (string, error) {
		u, p, ok := r.BasicAuth()
		if !ok || basicUsers[u] == "" || basicUsers[u] != p {
			return "", groupkit.ErrUnauthenticated
		}
		return u, nil
	}
	return groupkit.Config{
		Store:       st,
		Prefix:      "/dav",
		CurrentUser: current,
		Authorize: func(r *http.Request, urlUser string) (string, error) {
			u, err := current(r)
			if err != nil {
				return "", err
			}
			if u != urlUser {
				return "", groupkit.ErrForbidden
			}
			return u, nil
		},
		Challenge: testChallenge,
	}
}

// startBasicServer serves a Server over st on a real listener, with the
// handler at /dav/ and the well-known redirect at /.well-known/carddav.
func startBasicServer(t *testing.T, st store.Store) *httptest.Server {
	t.Helper()
	srv := newServer(t, basicConfig(st))
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/dav", srv.Handler())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func openFileStore(t *testing.T, dir string) *filestore.Store {
	t.Helper()
	st, err := filestore.Open(dir)
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	return st
}

// cardClient returns a go-webdav CardDAV client for endpoint as user.
func cardClient(t *testing.T, endpoint, user string) *carddav.Client {
	t.Helper()
	hc := webdav.HTTPClientWithBasicAuth(&http.Client{Timeout: 10 * time.Second}, user, basicUsers[user])
	c, err := carddav.NewClient(hc, endpoint)
	if err != nil {
		t.Fatalf("carddav.NewClient: %v", err)
	}
	return c
}

func testCard(uid, fn string) vcard.Card {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "4.0")
	c.SetValue(vcard.FieldUID, uid)
	c.SetValue(vcard.FieldFormattedName, fn)
	c.SetName(&vcard.Name{FamilyName: fn})
	return c
}

func ctxTimeout(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// discover runs the client's discovery from endpoint: current user
// principal, address book home set, address books.
func discover(t *testing.T, ctx context.Context, c *carddav.Client) (home string, books []carddav.AddressBook) {
	t.Helper()
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		t.Fatalf("FindCurrentUserPrincipal: %v", err)
	}
	if principal != "/dav/principals/alice/" {
		t.Fatalf("principal = %q", principal)
	}
	home, err = c.FindAddressBookHomeSet(ctx, principal)
	if err != nil {
		t.Fatalf("FindAddressBookHomeSet: %v", err)
	}
	if home != "/dav/addressbooks/alice/" {
		t.Fatalf("home = %q", home)
	}
	books, err = c.FindAddressBooks(ctx, home)
	if err != nil {
		t.Fatalf("FindAddressBooks: %v", err)
	}
	return home, books
}

func bookPaths(books []carddav.AddressBook) []string {
	var out []string
	for _, b := range books {
		out = append(out, b.Path)
	}
	slices.Sort(out)
	return out
}

func objPaths(objs []carddav.AddressObject) []string {
	var out []string
	for _, o := range objs {
		out = append(out, o.Path)
	}
	slices.Sort(out)
	return out
}

func TestInteropDiscovery(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, st)

	c := cardClient(t, ts.URL+"/dav/", "alice")
	if err := c.HasSupport(ctx); err != nil {
		t.Fatalf("HasSupport: %v", err)
	}
	_, books := discover(t, ctx, c)
	if len(books) != 1 {
		t.Fatalf("books = %+v", books)
	}
	b := books[0]
	if b.Path != "/dav/addressbooks/alice/contacts/" || b.Name != "Contacts" {
		t.Errorf("book = %+v", b)
	}
	if b.MaxResourceSize <= 0 {
		t.Errorf("MaxResourceSize = %d", b.MaxResourceSize)
	}
	if !slices.ContainsFunc(b.SupportedAddressData, func(d carddav.AddressDataType) bool {
		return d.ContentType == "text/vcard" && d.Version == "4.0"
	}) {
		t.Errorf("SupportedAddressData = %+v", b.SupportedAddressData)
	}

	// Wrong password: the client sees an error, not data.
	hc := webdav.HTTPClientWithBasicAuth(http.DefaultClient, "alice", "wrong")
	wc, err := carddav.NewClient(hc, ts.URL+"/dav/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wc.FindCurrentUserPrincipal(ctx); err == nil {
		t.Error("FindCurrentUserPrincipal with a wrong password succeeded")
	}
}

// TestInteropWellKnown starts discovery at /.well-known/carddav as RFC 6764
// section 6 describes: the client asks the well-known URI, follows the
// redirect to the context path and goes on from there. The go-webdav client
// has no well-known support of its own (DiscoverContextURL only does DNS),
// and net/http turns a redirected PROPFIND into a GET, so the hop is made
// here with the redirect's Location.
func TestInteropWellKnown(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, st)

	noFollow := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, method := range []string{"GET", "PROPFIND"} {
		req, err := http.NewRequestWithContext(ctx, method, ts.URL+"/.well-known/carddav", nil)
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
		base, _ := url.Parse(ts.URL + "/.well-known/carddav")
		loc, err := base.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		c := cardClient(t, loc.String(), "alice")
		if _, books := discover(t, ctx, c); len(books) != 1 {
			t.Fatalf("books via well-known = %+v", books)
		}
	}
}

func TestInteropReadWrite(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	ts := startBasicServer(t, st)
	c := cardClient(t, ts.URL+"/dav/", "alice")

	// A new address book with a plain MKCOL inside the home set.
	if err := c.Mkdir(ctx, "/dav/addressbooks/alice/friends/"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	home, books := discover(t, ctx, c)
	if got := bookPaths(books); !slices.Equal(got, []string{"/dav/addressbooks/alice/friends/"}) {
		t.Fatalf("books = %v", got)
	}
	book := home + "friends/"

	// Create three contacts.
	cards := map[string]vcard.Card{
		book + "ada.vcf":     testCard("uid-ada", "Ada Lovelace"),
		book + "grace.vcf":   testCard("uid-grace", "Grace Hopper"),
		book + "charles.vcf": testCard("uid-charles", "Charles Babbage"),
	}
	etags := map[string]string{}
	for p, card := range cards {
		ao, err := c.PutAddressObject(ctx, p, card)
		if err != nil {
			t.Fatalf("PutAddressObject %s: %v", p, err)
		}
		if ao.ETag == "" {
			t.Errorf("PutAddressObject %s: no ETag", p)
		}
		etags[p] = ao.ETag
	}

	// Get one back.
	ao, err := c.GetAddressObject(ctx, book+"ada.vcf")
	if err != nil {
		t.Fatalf("GetAddressObject: %v", err)
	}
	if ao.Card.PreferredValue(vcard.FieldFormattedName) != "Ada Lovelace" || ao.ETag != etags[book+"ada.vcf"] {
		t.Errorf("GetAddressObject = %q etag %q, want etag %q", ao.Card.PreferredValue(vcard.FieldFormattedName), ao.ETag, etags[book+"ada.vcf"])
	}
	if ao.ModTime.IsZero() || ao.ContentLength == 0 {
		t.Errorf("GetAddressObject metadata = %v %d", ao.ModTime, ao.ContentLength)
	}

	// A text filter.
	found, err := c.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
		PropFilters: []carddav.PropFilter{{
			Name:        vcard.FieldFormattedName,
			TextMatches: []carddav.TextMatch{{Text: "hopper"}},
		}},
	})
	if err != nil {
		t.Fatalf("QueryAddressBook: %v", err)
	}
	if got := objPaths(found); !slices.Equal(got, []string{book + "grace.vcf"}) {
		t.Errorf("QueryAddressBook = %v", got)
	} else if found[0].Card.PreferredValue(vcard.FieldUID) != "uid-grace" || found[0].ETag != etags[book+"grace.vcf"] {
		t.Errorf("QueryAddressBook object = %+v", found[0])
	}
	// Query with named properties and a starts-with match.
	found, err = c.QueryAddressBook(ctx, book, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldFormattedName, vcard.FieldUID}},
		PropFilters: []carddav.PropFilter{{
			Name:        vcard.FieldFormattedName,
			TextMatches: []carddav.TextMatch{{Text: "C", MatchType: carddav.MatchStartsWith}},
		}},
	})
	if err != nil {
		t.Fatalf("QueryAddressBook starts-with: %v", err)
	}
	if got := objPaths(found); !slices.Equal(got, []string{book + "charles.vcf"}) {
		t.Errorf("QueryAddressBook starts-with = %v", got)
	}

	// Multiget two of them.
	got, err := c.MultiGetAddressBook(ctx, book, &carddav.AddressBookMultiGet{
		Paths:       []string{book + "ada.vcf", book + "charles.vcf"},
		DataRequest: carddav.AddressDataRequest{AllProp: true},
	})
	if err != nil {
		t.Fatalf("MultiGetAddressBook: %v", err)
	}
	if paths := objPaths(got); !slices.Equal(paths, []string{book + "ada.vcf", book + "charles.vcf"}) {
		t.Errorf("MultiGetAddressBook = %v", paths)
	}

	// Replace one: a new ETag.
	ada := testCard("uid-ada", "Ada King")
	ao, err = c.PutAddressObject(ctx, book+"ada.vcf", ada)
	if err != nil {
		t.Fatalf("PutAddressObject replace: %v", err)
	}
	if ao.ETag == "" || ao.ETag == etags[book+"ada.vcf"] {
		t.Errorf("replace ETag = %q, old %q", ao.ETag, etags[book+"ada.vcf"])
	}

	// Remove one; it is gone.
	if err := c.RemoveAll(ctx, book+"charles.vcf"); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := c.GetAddressObject(ctx, book+"charles.vcf"); err == nil {
		t.Error("GetAddressObject after RemoveAll succeeded")
	}

	// Another user's address book is out of reach.
	bob := cardClient(t, ts.URL+"/dav/", "bob")
	if _, err := bob.GetAddressObject(ctx, book+"ada.vcf"); err == nil {
		t.Error("bob read alice's contact")
	}
	if _, err := bob.FindAddressBooks(ctx, home); err == nil {
		t.Error("bob listed alice's address books")
	}

	// Removing the whole address book.
	if err := c.RemoveAll(ctx, book); err != nil {
		t.Fatalf("RemoveAll book: %v", err)
	}
	if _, books := discover(t, ctx, c); len(books) != 0 {
		t.Errorf("books after delete = %v", bookPaths(books))
	}
}

func TestInteropSync(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, st)
	c := cardClient(t, ts.URL+"/dav/", "alice")
	book := "/dav/addressbooks/alice/contacts/"

	for _, n := range []string{"a", "b", "c"} {
		if _, err := c.PutAddressObject(ctx, book+n+".vcf", testCard("uid-"+n, "Person "+n)); err != nil {
			t.Fatalf("PutAddressObject: %v", err)
		}
	}
	first, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldUID}},
	})
	if err != nil {
		t.Fatalf("SyncCollection initial: %v", err)
	}
	if got := objPaths(first.Updated); !slices.Equal(got, []string{book + "a.vcf", book + "b.vcf", book + "c.vcf"}) {
		t.Errorf("initial updated = %v", got)
	}
	if len(first.Deleted) != 0 || !strings.HasPrefix(first.SyncToken, "urn:groupkit:sync:") {
		t.Errorf("initial deleted %v token %q", first.Deleted, first.SyncToken)
	}
	for _, o := range first.Updated {
		if o.ETag == "" {
			t.Errorf("initial %s has no ETag", o.Path)
		}
	}

	// Nothing changed: nothing reported, same token.
	same, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: first.SyncToken})
	if err != nil {
		t.Fatalf("SyncCollection unchanged: %v", err)
	}
	if len(same.Updated)+len(same.Deleted) != 0 || same.SyncToken != first.SyncToken {
		t.Errorf("unchanged sync = %+v", same)
	}

	// One added, one changed, one removed.
	if _, err := c.PutAddressObject(ctx, book+"d.vcf", testCard("uid-d", "Person d")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutAddressObject(ctx, book+"a.vcf", testCard("uid-a", "Person A")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, book+"b.vcf"); err != nil {
		t.Fatal(err)
	}
	next, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: first.SyncToken})
	if err != nil {
		t.Fatalf("SyncCollection incremental: %v", err)
	}
	if got := objPaths(next.Updated); !slices.Equal(got, []string{book + "a.vcf", book + "d.vcf"}) {
		t.Errorf("incremental updated = %v", got)
	}
	if !slices.Equal(next.Deleted, []string{book + "b.vcf"}) {
		t.Errorf("incremental deleted = %v", next.Deleted)
	}
	if next.SyncToken == first.SyncToken {
		t.Error("token did not change")
	}

	// A token the server never issued: the client gets an error.
	if _, err := c.SyncCollection(ctx, book, &carddav.SyncQuery{SyncToken: "urn:groupkit:sync:nope:1"}); err == nil {
		t.Error("SyncCollection with a foreign token succeeded")
	}
}
