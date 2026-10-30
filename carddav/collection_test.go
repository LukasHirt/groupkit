// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

const (
	homeURL = "/dav/addressbooks/alice/"
	newURL  = "/dav/addressbooks/alice/new/"
)

var newRef = store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "new"}

// emptyFixture is a handler over an empty store; alice resolves to "alice".
func emptyFixture(t *testing.T, st store.Store) *objFixture {
	t.Helper()
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return &objFixture{h: h, st: st}
}

func (f *objFixture) getBook(t *testing.T, ref store.CollectionRef) (store.Collection, error) {
	t.Helper()
	return f.st.GetCollection(context.Background(), ref)
}

const mkcolNS = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav" xmlns:L="urn:ietf:params:xml:ns:caldav"`

func mkcolBody(props string) []byte {
	return []byte(`<?xml version="1.0"?><D:mkcol ` + mkcolNS + `><D:set><D:prop>` + props + `</D:prop></D:set></D:mkcol>`)
}

func patchBody(blocks string) []byte {
	return []byte(`<?xml version="1.0"?><D:propertyupdate ` + mkcolNS + ` xmlns:X="urn:x">` + blocks + `</D:propertyupdate>`)
}

func eachStore(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	for name, mk := range storeFactories {
		t.Run(name, func(t *testing.T) { fn(t, mk(t)) })
	}
}

func TestMkcolPlain(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		rec := f.doPath("MKCOL", newURL, nil, nil)
		wantStatus(t, rec, http.StatusCreated)
		if loc := rec.Header().Get("Location"); loc != newURL {
			t.Errorf("Location = %q", loc)
		}
		if _, err := f.getBook(t, newRef); err != nil {
			t.Fatal(err)
		}
		rec = f.doPath("PROPFIND", homeURL, nil, hdrs{"Depth": "1"})
		r := parseReply(t, rec)
		if len(r.hrefs) != 2 || r.hrefs[1] != newURL {
			t.Errorf("hrefs = %v", r.hrefs)
		}
		rec = f.doPath("PROPFIND", newURL, nil, hdrs{"Depth": "0"})
		wantStatus(t, rec, http.StatusMultiStatus)
	})
}

func TestMkcolNoTrailingSlashAndEscapedLocation(t *testing.T) {
	f := emptyFixture(t, memory.New())
	rec := f.doPath("MKCOL", "/dav/addressbooks/alice/my%20book", nil, nil)
	wantStatus(t, rec, http.StatusCreated)
	if loc := rec.Header().Get("Location"); loc != "/dav/addressbooks/alice/my%20book/" {
		t.Errorf("Location = %q", loc)
	}
}

func TestMkcolExtended(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		body := mkcolBody(`<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>` +
			`<D:displayname>Work</D:displayname><C:addressbook-description>Colleagues</C:addressbook-description>`)
		wantStatus(t, f.doPath("MKCOL", newURL, body, nil), http.StatusCreated)
		c, err := f.getBook(t, newRef)
		if err != nil {
			t.Fatal(err)
		}
		if c.Props.DisplayName != "Work" || c.Props.Description != "Colleagues" {
			t.Errorf("props = %+v", c.Props)
		}
	})
}

func TestMkcolResourceTypeOnlyAddressbook(t *testing.T) {
	f := emptyFixture(t, memory.New())
	body := mkcolBody(`<D:resourcetype><C:addressbook/></D:resourcetype>`)
	wantStatus(t, f.doPath("MKCOL", newURL, body, nil), http.StatusCreated)
}

func TestMkcolRejected(t *testing.T) {
	tests := []struct {
		name  string
		props string
		pre   string
	}{
		{"calendar type", `<D:resourcetype><D:collection/><L:calendar/></D:resourcetype>`, "valid-resourcetype"},
		{"collection only", `<D:resourcetype><D:collection/></D:resourcetype>`, "valid-resourcetype"},
		{"empty type", `<D:resourcetype/>`, "valid-resourcetype"},
		{"addressbook and calendar", `<D:resourcetype><C:addressbook/><L:calendar/></D:resourcetype>`, "valid-resourcetype"},
		{"unknown DAV property", `<D:thing>1</D:thing>`, "cannot-modify-protected-property"},
		{"protected property", `<D:getetag>"x"</D:getetag>`, "cannot-modify-protected-property"},
		{"getctag", `<CS:getctag xmlns:CS="http://calendarserver.org/ns/">1</CS:getctag>`, "cannot-modify-protected-property"},
		{"max-resource-size", `<C:max-resource-size>1</C:max-resource-size>`, "cannot-modify-protected-property"},
		{"valid beside protected", `<D:displayname>A</D:displayname><D:getetag>"x"</D:getetag>`, "cannot-modify-protected-property"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := emptyFixture(t, st)
				rec := f.doPath("MKCOL", newURL, mkcolBody(tt.props), nil)
				wantPrecondition(t, rec, http.StatusForbidden, tt.pre)
				if _, err := f.getBook(t, newRef); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("book exists after rejection: %v", err)
				}
			})
		})
	}
}

// Properties the engine does not handle, outside DAV:, are skipped when an
// address book is created.
func TestMkcolSkipsForeignProperties(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		body := mkcolBody(`<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>` +
			`<X:thing xmlns:X="urn:x">1</X:thing><L:schedule-calendar-transp xmlns:L="urn:ietf:params:xml:ns:caldav"/>` +
			`<D:displayname>Kept</D:displayname><C:addressbook-description>Desc</C:addressbook-description>`)
		wantStatus(t, f.doPath("MKCOL", newURL, body, nil), http.StatusCreated)
		c, err := f.getBook(t, newRef)
		if err != nil {
			t.Fatal(err)
		}
		if c.Props.DisplayName != "Kept" || c.Props.Description != "Desc" {
			t.Errorf("props = %+v", c.Props)
		}
	})
}

func TestMkcolBadRequests(t *testing.T) {
	long := strings.Repeat("x", 256)
	tests := []struct {
		name string
		body []byte
		path string
	}{
		{"malformed", []byte("<D:mkcol"), newURL},
		{"wrong root", []byte(`<a xmlns="DAV:"/>`), newURL},
		{"remove", []byte(`<D:mkcol ` + mkcolNS + `><D:remove><D:prop><D:displayname/></D:prop></D:remove></D:mkcol>`), newURL},
		{"long name", mkcolBody(`<D:displayname>` + long + `</D:displayname>`), newURL},
		{"long description", mkcolBody(`<C:addressbook-description>` + strings.Repeat("x", 4097) + `</C:addressbook-description>`), newURL},
		{"dotdot", nil, "/dav/addressbooks/alice/.."},
		{"encoded dotdot", nil, "/dav/addressbooks/alice/%2e%2e/"},
		{"encoded slash", nil, "/dav/addressbooks/alice/a%2Fb/"},
		{"300 bytes", nil, "/dav/addressbooks/alice/" + strings.Repeat("a", 300) + "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := emptyFixture(t, memory.New())
			wantStatus(t, f.doPath("MKCOL", tt.path, tt.body, nil), http.StatusBadRequest)
			list, _ := f.st.ListCollections(context.Background(), "alice", store.KindAddressBook)
			if len(list) != 0 {
				t.Errorf("created %v", list)
			}
		})
	}
}

func TestMkcolBoundaryLengths(t *testing.T) {
	f := emptyFixture(t, memory.New())
	body := mkcolBody(`<D:displayname>` + strings.Repeat("é", 127) + `x</D:displayname>` +
		`<C:addressbook-description>` + strings.Repeat("d", 4096) + `</C:addressbook-description>`)
	wantStatus(t, f.doPath("MKCOL", newURL, body, nil), http.StatusCreated)
}

func TestMkcolExistsHomeObjectAndTooLarge(t *testing.T) {
	f := memFixture(t)
	rec := f.doPath("MKCOL", objBase, nil, nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	if rec.Header().Get("Allow") == "" {
		t.Error("no Allow header")
	}
	for _, p := range []string{homeURL, objBase + "x.vcf"} {
		rec := f.doPath("MKCOL", p, nil, nil)
		wantStatus(t, rec, http.StatusMethodNotAllowed)
		if a := rec.Header().Get("Allow"); strings.Contains(a, "MKCOL") {
			t.Errorf("%s Allow = %q", p, a)
		}
	}
	big := bytes.Repeat([]byte(" "), dav.MaxXMLBytes+1)
	wantStatus(t, f.doPath("MKCOL", newURL, big, nil), http.StatusRequestEntityTooLarge)
}

func TestMkcolConcurrent(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		const n = 12
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = f.doPath("MKCOL", newURL, nil, nil).Code
			}()
		}
		wg.Wait()
		created := 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				created++
			case http.StatusMethodNotAllowed:
			default:
				t.Errorf("unexpected status %d", c)
			}
		}
		if created != 1 {
			t.Errorf("created = %d, want 1 (%v)", created, codes)
		}
	})
}

func TestMkcolUsesResolvedOwner(t *testing.T) {
	st := memory.New()
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: func(_ *http.Request, u string) (string, error) {
		return "owner-" + u, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("MKCOL", "/dav/addressbooks/bob/b/", nil))
	wantStatus(t, rec, http.StatusCreated)
	if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "owner-bob", Kind: store.KindAddressBook, Name: "b"}); err != nil {
		t.Errorf("not under the resolved owner: %v", err)
	}
	if _, err := st.GetCollection(context.Background(), store.CollectionRef{Owner: "bob", Kind: store.KindAddressBook, Name: "b"}); err == nil {
		t.Error("created under the URL user")
	}
	// PROPPATCH and DELETE use the resolved owner too.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("PROPPATCH", "/dav/addressbooks/bob/b/", bytes.NewReader(patchBody(`<D:set><D:prop><D:displayname>B</D:displayname></D:prop></D:set>`)))
	h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusMultiStatus)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("DELETE", "/dav/addressbooks/bob/b/", nil))
	wantStatus(t, rec, http.StatusNoContent)
}

// patchFixture is a fixture whose book "contacts" has a name and a description.
func patchFixture(t *testing.T, st store.Store) *objFixture {
	t.Helper()
	f := newObjFixture(t, st, 1<<20)
	d, e := "Old", "Old desc"
	if _, err := st.UpdateCollection(context.Background(), objRef, store.PropsUpdate{DisplayName: &d, Description: &e}); err != nil {
		t.Fatal(err)
	}
	return f
}

func ctag(t *testing.T, f *objFixture) string {
	t.Helper()
	rec := f.doPath("PROPFIND", objBase, []byte(propBody(`<CS:getctag/>`)), hdrs{"Depth": "0"})
	r := parseReply(t, rec)
	return r.must(t, 0, dav.GetCTag).Text
}

func TestProppatchSetAndRemove(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := patchFixture(t, st)
		before := ctag(t, f)
		rec := f.doPath("PROPPATCH", objBase, patchBody(
			`<D:set><D:prop><D:displayname>New</D:displayname></D:prop></D:set>`+
				`<D:remove><D:prop><C:addressbook-description/></D:prop></D:remove>`), nil)
		r := parseReply(t, rec)
		if len(r.hrefs) != 1 || r.hrefs[0] != objBase {
			t.Fatalf("hrefs = %v", r.hrefs)
		}
		if len(r.props[0]) != 1 || len(r.props[0][200]) != 2 {
			t.Fatalf("props = %v", r.props)
		}
		if r.find(t, 0, 200, dav.DisplayName) == nil || r.find(t, 0, 200, dav.AddressbookDescription) == nil {
			t.Errorf("missing 200 props: %v", r.props[0])
		}
		c, _ := f.getBook(t, objRef)
		if c.Props.DisplayName != "New" || c.Props.Description != "" {
			t.Errorf("props = %+v", c.Props)
		}
		if after := ctag(t, f); after == before {
			t.Errorf("ctag unchanged: %s", after)
		}
		// PROPFIND shows the new values.
		r = parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/>`)), hdrs{"Depth": "0"}))
		if got := r.must(t, 0, dav.DisplayName).Text; got != "New" {
			t.Errorf("displayname = %q", got)
		}
	})
}

func TestProppatchRemoveDisplayNameFallsBack(t *testing.T) {
	f := patchFixture(t, memory.New())
	wantStatus(t, f.doPath("PROPPATCH", objBase, patchBody(`<D:remove><D:prop><D:displayname/></D:prop></D:remove>`), nil), 207)
	r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/>`)), hdrs{"Depth": "0"}))
	if got := r.must(t, 0, dav.DisplayName).Text; got != "contacts" {
		t.Errorf("displayname = %q", got)
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
			`<D:set><D:prop><D:displayname>X</D:displayname><CS:getctag xmlns:CS="http://calendarserver.org/ns/">9</CS:getctag></D:prop></D:set>`,
			[]string{"getctag"}, []string{"displayname"}},
		{"resourcetype",
			`<D:set><D:prop><D:resourcetype><C:addressbook/></D:resourcetype><C:addressbook-description>X</C:addressbook-description></D:prop></D:set>`,
			[]string{"resourcetype"}, []string{"addressbook-description"}},
		{"foreign property",
			`<D:set><D:prop><X:thing>1</X:thing><D:displayname>X</D:displayname></D:prop></D:set><D:remove><D:prop><C:addressbook-description/></D:prop></D:remove>`,
			[]string{"thing"}, []string{"displayname", "addressbook-description"}},
		{"sync-token",
			`<D:remove><D:prop><D:sync-token/></D:prop></D:remove>`,
			[]string{"sync-token"}, nil},
		{"too long",
			`<D:set><D:prop><D:displayname>` + strings.Repeat("x", 256) + `</D:displayname><C:addressbook-description>ok</C:addressbook-description></D:prop></D:set>`,
			[]string{"displayname"}, []string{"addressbook-description"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := patchFixture(t, st)
				before, _ := f.getBook(t, objRef)
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
				// 403 comes before 424 in the document.
				body := rec.Body.String()
				if len(tt.failed) > 0 && strings.Index(body, "403") > strings.Index(body, "424") {
					t.Errorf("propstat order: %s", body)
				}
				if strings.Contains(body, "<D:error") || strings.Contains(body, ":error") {
					t.Errorf("unexpected error element: %s", body)
				}
				after, _ := f.getBook(t, objRef)
				if !reflect.DeepEqual(before, after) {
					t.Errorf("store changed:\nbefore %+v\nafter  %+v", before, after)
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
			before, _ := f.getBook(t, objRef)
			wantStatus(t, f.doPath("PROPPATCH", objBase, body, nil), http.StatusBadRequest)
			after, _ := f.getBook(t, objRef)
			if !reflect.DeepEqual(before, after) {
				t.Error("store changed")
			}
		})
	}
}

func TestProppatchMissingHomeObject(t *testing.T) {
	f := patchFixture(t, memory.New())
	body := patchBody(`<D:set><D:prop><D:displayname>X</D:displayname></D:prop></D:set>`)
	wantStatus(t, f.doPath("PROPPATCH", "/dav/addressbooks/alice/nope/", body, nil), http.StatusNotFound)
	// Even a request that would fail is 404 for a missing book.
	bad := patchBody(`<D:set><D:prop><X:y>1</X:y></D:prop></D:set>`)
	wantStatus(t, f.doPath("PROPPATCH", "/dav/addressbooks/alice/nope/", bad, nil), http.StatusNotFound)
	wantStatus(t, f.doPath("PROPPATCH", homeURL, body, nil), http.StatusMethodNotAllowed)
	wantStatus(t, f.doPath("PROPPATCH", objBase+"x.vcf", body, nil), http.StatusMethodNotAllowed)
}

func TestDeleteCollection(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), http.StatusCreated)
		wantStatus(t, f.do("PUT", "b.vcf", card("b"), vcType), http.StatusCreated)
		rec := f.doPath("DELETE", objBase, nil, hdrs{"If-Match": `"nothing"`, "Depth": "0"})
		wantStatus(t, rec, http.StatusNoContent)
		wantStatus(t, f.doPath("PROPFIND", objBase, nil, hdrs{"Depth": "0"}), http.StatusNotFound)
		wantStatus(t, f.do("GET", "a.vcf", nil, nil), http.StatusNotFound)
		if _, err := st.GetObject(context.Background(), objRef, "a.vcf"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetObject err = %v", err)
		}
		wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNotFound)
		// The name can be created again.
		wantStatus(t, f.doPath("MKCOL", objBase, nil, nil), http.StatusCreated)
	})
}

func TestDeleteCollectionHomeIs405(t *testing.T) {
	f := memFixture(t)
	rec := f.doPath("DELETE", homeURL, nil, nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	if strings.Contains(rec.Header().Get("Allow"), "DELETE") {
		t.Errorf("Allow = %q", rec.Header().Get("Allow"))
	}
}

func TestCtagChangesMonotonic(t *testing.T) {
	f := patchFixture(t, memory.New())
	a := ctag(t, f)
	wantStatus(t, f.doPath("PROPPATCH", objBase, patchBody(`<D:set><D:prop><D:displayname>Z</D:displayname></D:prop></D:set>`), nil), 207)
	b := ctag(t, f)
	x, _ := strconv.ParseUint(a, 10, 64)
	y, _ := strconv.ParseUint(b, 10, 64)
	if y <= x {
		t.Errorf("ctag %s -> %s", a, b)
	}
}

type createErrStore struct {
	store.Store
	err error
}

func (c createErrStore) CreateCollection(context.Context, store.CollectionRef, store.Props) (store.Collection, error) {
	return store.Collection{}, c.err
}

// Allow is advertised for an existing name only, not for other failures.
func TestMkcolAllowOnlyForExistingName(t *testing.T) {
	for name, tt := range map[string]struct {
		err  error
		code int
	}{
		"fault":        {errSecret, http.StatusInternalServerError},
		"invalid name": {store.ErrInvalidName, http.StatusBadRequest},
		"exists":       {store.ErrExists, http.StatusMethodNotAllowed},
	} {
		t.Run(name, func(t *testing.T) {
			f := emptyFixture(t, createErrStore{Store: memory.New(), err: tt.err})
			rec := f.doPath("MKCOL", newURL, nil, nil)
			wantStatus(t, rec, tt.code)
			a := rec.Header().Get("Allow")
			if tt.err == store.ErrExists && (a == "" || strings.Contains(a, "MKCOL")) {
				t.Errorf("Allow = %q, want the methods of an address book", a)
			}
			if tt.err != store.ErrExists && a != "" {
				t.Errorf("Allow = %q on a failure that is not a clash", a)
			}
		})
	}
}
