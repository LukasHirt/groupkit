// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

func TestMkcolTrimsAndBlankIsUnset(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		body := mkcolBody(`<D:displayname>  Friends
 </D:displayname><C:addressbook-description>   </C:addressbook-description>`)
		wantStatus(t, f.doPath("MKCOL", newURL, body, nil), http.StatusCreated)
		c, err := f.getBook(t, newRef)
		if err != nil {
			t.Fatal(err)
		}
		if c.Props.DisplayName != "Friends" || c.Props.Description != "" {
			t.Errorf("props = %+v", c.Props)
		}
	})
}

func TestProppatchTrimsAndBlankIsUnset(t *testing.T) {
	f := patchFixture(t, memory.New())
	rec := f.doPath("PROPPATCH", objBase, patchBody(
		`<D:set><D:prop><D:displayname> 
 </D:displayname><C:addressbook-description>  New desc </C:addressbook-description></D:prop></D:set>`), nil)
	wantStatus(t, rec, http.StatusMultiStatus)
	c, _ := f.getBook(t, objRef)
	if c.Props.DisplayName != "" || c.Props.Description != "New desc" {
		t.Errorf("props = %+v", c.Props)
	}
	r := parseReply(t, f.doPath("PROPFIND", objBase, []byte(propBody(`<D:displayname/>`)), hdrs{"Depth": "0"}))
	if got := r.must(t, 0, dav.DisplayName).Text; got != "contacts" {
		t.Errorf("displayname = %q, want the fallback", got)
	}
}

func TestPropertyWithChildElementsIsRejected(t *testing.T) {
	const bad = `a<b xmlns="urn:x">x</b>`
	t.Run("mkcol", func(t *testing.T) {
		for _, p := range []string{
			`<D:displayname>` + bad + `</D:displayname>`,
			`<C:addressbook-description>` + bad + `</C:addressbook-description>`,
		} {
			f := emptyFixture(t, memory.New())
			wantStatus(t, f.doPath("MKCOL", newURL, mkcolBody(p), nil), http.StatusBadRequest)
			if _, err := f.getBook(t, newRef); err == nil {
				t.Errorf("%s: book was created", p)
			}
		}
	})
	t.Run("proppatch", func(t *testing.T) {
		f := patchFixture(t, memory.New())
		before, _ := f.getBook(t, objRef)
		rec := f.doPath("PROPPATCH", objBase, patchBody(
			`<D:set><D:prop><D:displayname>`+bad+`</D:displayname><C:addressbook-description>ok</C:addressbook-description></D:prop></D:set>`), nil)
		r := parseReply(t, rec)
		if len(r.props[0][403]) != 1 || r.props[0][403][0].Name != dav.DisplayName {
			t.Errorf("403 = %v", r.props[0][403])
		}
		if len(r.props[0][424]) != 1 || r.props[0][424][0].Name != dav.AddressbookDescription {
			t.Errorf("424 = %v", r.props[0][424])
		}
		after, _ := f.getBook(t, objRef)
		if !reflect.DeepEqual(before.Props, after.Props) {
			t.Errorf("store changed: %+v -> %+v", before.Props, after.Props)
		}
	})
}

func TestProppatchDuplicateNamesLastWinsListedOnce(t *testing.T) {
	f := patchFixture(t, memory.New())
	rec := f.doPath("PROPPATCH", objBase, patchBody(
		`<D:set><D:prop><D:displayname>One</D:displayname><C:addressbook-description>D1</C:addressbook-description></D:prop></D:set>`+
			`<D:set><D:prop><D:displayname>Two</D:displayname></D:prop></D:set>`+
			`<D:remove><D:prop><C:addressbook-description/></D:prop></D:remove>`), nil)
	r := parseReply(t, rec)
	if n := len(r.props[0][200]); n != 2 {
		t.Errorf("200 propstat lists %d properties, want 2: %v", n, r.props[0][200])
	}
	c, _ := f.getBook(t, objRef)
	if c.Props.DisplayName != "Two" || c.Props.Description != "" {
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

func TestMkcolOnExistingBookAllowLacksMkcol(t *testing.T) {
	f := memFixture(t)
	rec := f.doPath("MKCOL", objBase, nil, nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	a := rec.Header().Get("Allow")
	if strings.Contains(a, "MKCOL") || !strings.Contains(a, "PROPFIND") || !strings.Contains(a, "REPORT") {
		t.Errorf("Allow = %q", a)
	}
}

func TestMkcolConcurrent32(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := emptyFixture(t, st)
		const n = 32
		codes := make([]int, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i] = f.doPath("MKCOL", newURL, nil, nil).Code
			}()
		}
		close(start)
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

func TestDeleteLargeBook(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	eachStore(t, func(t *testing.T, st store.Store) {
		// The file store syncs every write, so it gets a smaller book.
		n := 3000
		if _, ok := st.(*memory.Store); !ok {
			n = 300
		}
		f := newObjFixture(t, st, 1<<20)
		ctx := context.Background()
		other := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "keep"}
		if _, err := st.CreateCollection(ctx, other, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.PutObject(ctx, other, "k.vcf", card("k"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
		for i := range n {
			if _, err := st.PutObject(ctx, objRef, fmt.Sprintf("p%d.vcf", i), card(fmt.Sprint("u", i)), store.PutOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNoContent)
		wantStatus(t, f.doPath("PROPFIND", objBase, nil, hdrs{"Depth": "0"}), http.StatusNotFound)
		wantStatus(t, f.do("GET", fmt.Sprintf("p%d.vcf", n-1), nil, nil), http.StatusNotFound)
		if _, err := st.GetObject(ctx, other, "k.vcf"); err != nil {
			t.Errorf("another book lost an object: %v", err)
		}
		wantStatus(t, f.doPath("MKCOL", objBase, nil, nil), http.StatusCreated)
		objs, err := st.ListObjects(ctx, objRef, store.ListOptions{})
		if err != nil || len(objs) != 0 {
			t.Errorf("re-created book holds %d objects, err %v", len(objs), err)
		}
	})
}

func TestProppatchRacesDelete(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		for round := range 10 {
			f := newObjFixture(t, st, 1<<20)
			wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), http.StatusCreated)
			body := patchBody(fmt.Sprintf(`<D:set><D:prop><D:displayname>R%d</D:displayname></D:prop></D:set>`, round))
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

func TestQueryWithAddressDataReadsEachCandidateOnce(t *testing.T) {
	f := newReportFixture(t)
	body := `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:prop><D:getetag/><CR:address-data/></D:prop>` +
		filter("", `<CR:prop-filter name="EMAIL"/>`) + `</CR:addressbook-query>`
	f.st.gets.Store(0)
	r := parseReply(t, f.report(bookURL, body))
	if want := []string{"ada.vcf", "bob.vcf", "carl.vcf"}; !slices.Equal(bases(r), want) {
		t.Fatalf("got %v", bases(r))
	}
	if n := f.st.gets.Load(); n != 3 {
		t.Errorf("GetObject called %d times, want 3 (one per candidate)", n)
	}
	for i, h := range r.hrefs {
		if got := r.must(t, i, dav.AddressData).Text; got != testCards[bases(r)[i]] {
			t.Errorf("%s data = %q", h, got)
		}
	}
}

func TestQueryWithoutFilterSkipsUnreadableWhenDataAsked(t *testing.T) {
	f := newReportFixture(t)
	if _, err := f.mem.PutObject(context.Background(), f.bookRef(), "dead.vcf", vcardFor("dead"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	f.st.fail = map[string]bool{"dead.vcf": true}
	body := `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:prop><D:getetag/><CR:address-data/></D:prop></CR:addressbook-query>`
	r := parseReply(t, f.report(bookURL, body))
	if want := []string{"ada.vcf", "bob.vcf", "carl.vcf"}; !slices.Equal(bases(r), want) {
		t.Errorf("got %v, want %v", bases(r), want)
	}
	if r.must(t, 0, dav.AddressData).Text != testCards["ada.vcf"] {
		t.Error("no address-data")
	}
	// Without address-data the listing alone answers and reads nothing.
	f.st.gets.Store(0)
	r = parseReply(t, f.report(bookURL, query("")))
	if len(r.hrefs) != 4 || f.st.gets.Load() != 0 {
		t.Errorf("hrefs %v, gets %d", r.hrefs, f.st.gets.Load())
	}
}

func TestQueryHugeNresultsIsUnlimited(t *testing.T) {
	f := newReportFixture(t)
	for _, n := range []string{"2147483648", "4294967296", "99999999999999999999999999"} {
		rec := f.report(bookURL, query(`<CR:limit><CR:nresults>`+n+`</CR:nresults></CR:limit>`))
		if rec.Code != http.StatusMultiStatus {
			t.Fatalf("nresults %s: status %d", n, rec.Code)
		}
		r := parseReply(t, rec)
		if len(r.hrefs) != 3 {
			t.Errorf("nresults %s: got %v", n, r.hrefs)
		}
	}
}

func TestQueryObjectCap(t *testing.T) {
	old := maxQueryObjects
	maxQueryObjects = 5
	t.Cleanup(func() { maxQueryObjects = old })

	f := newFixture(t)
	for i := range 8 {
		if _, err := f.mem.PutObject(context.Background(), f.bookRef(), fmt.Sprintf("p%d.vcf", i), vcardFor(fmt.Sprint("p", i)), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"no filter": query(""),
		"filter":    query(filter("", `<CR:prop-filter name="FN"/>`)),
	} {
		r := parseReply(t, f.report(bookURL, body))
		resps := r.root.Kids(dav.ResponseEl)
		if len(resps) != 6 {
			t.Fatalf("%s: %d responses %v, want 5 objects and the 507", name, len(resps), r.hrefs)
		}
		last := resps[5]
		if last.Child(dav.Href).Text != bookURL || !strings.Contains(last.Child(dav.Status).Text, "507") ||
			last.Child(dav.ErrorEl).Child(dav.NumberOfMatchesWithinLimits) == nil {
			t.Errorf("%s: last response is not the 507: %v", name, r.hrefs)
		}
	}
	// Exactly at the cap there is nothing to cut.
	maxQueryObjects = 8
	r := parseReply(t, f.report(bookURL, query("")))
	if len(r.hrefs) != 8 {
		t.Errorf("at the cap: %v", r.hrefs)
	}
}

func TestMultigetErrorResponseCarriesItsHref(t *testing.T) {
	f := newReportFixture(t)
	f.st.fail = map[string]bool{"bob.vcf": true}
	r := parseReply(t, f.report(bookURL, multiget(hrefEl("/dav/addressbooks/alice/contacts/ada.vcf")+hrefEl("/dav/addressbooks/alice/contacts/bob.vcf"))))
	if want := []string{"/dav/addressbooks/alice/contacts/ada.vcf", "/dav/addressbooks/alice/contacts/bob.vcf"}; !slices.Equal(r.hrefs, want) {
		t.Errorf("hrefs = %v, want %v", r.hrefs, want)
	}
}

func TestMultigetMissingBook(t *testing.T) {
	f := newReportFixture(t)
	rec := f.report("/dav/addressbooks/alice/nope/", multiget(hrefEl("/dav/addressbooks/alice/nope/a.vcf")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("read %d objects", n)
	}
}

func TestMultigetSpellingsOfOneObjectAnswerOnce(t *testing.T) {
	f := newReportFixture(t)
	f.st.gets.Store(0)
	r := parseReply(t, f.report(bookURL, multiget(
		hrefEl("/dav/addressbooks/alice/contacts/bob.vcf")+
			hrefEl("/dav/addressbooks/%61lice/contacts/b%6Fb.vcf")+
			hrefEl("http://example.com/dav/addressbooks/alice/contacts/bob.vcf"))))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/addressbooks/alice/contacts/bob.vcf" {
		t.Errorf("hrefs = %v", r.hrefs)
	}
	if n := f.st.gets.Load(); n != 1 {
		t.Errorf("GetObject called %d times, want 1", n)
	}
}

func TestQueryTooManyParamFilters(t *testing.T) {
	f := newReportFixture(t)
	many := strings.Repeat(`<CR:param-filter name="TYPE"/>`, 101)
	rec := f.report(bookURL, query(filter("", `<CR:prop-filter name="EMAIL">`+many+`</CR:prop-filter>`)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "valid-filter") {
		t.Errorf("status %d body %s", rec.Code, rec.Body)
	}
	ok := strings.Repeat(`<CR:param-filter name="TYPE"/>`, 100)
	rec = f.report(bookURL, query(filter("", `<CR:prop-filter name="EMAIL">`+ok+`</CR:prop-filter>`)))
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("100 param-filters: status %d", rec.Code)
	}
}
