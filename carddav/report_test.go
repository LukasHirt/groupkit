// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
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
	bookURL = "/dav/addressbooks/alice/contacts/"
	nsDecl  = `xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav"`
)

var testCards = map[string]string{
	"ada.vcf": "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada Lovelace\r\n" +
		"EMAIL;TYPE=work:ada@example.org\r\nTEL;TYPE=cell:+1 555 0100\r\nEND:VCARD\r\n",
	"bob.vcf": "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:bob\r\nFN:Bob Builder\r\n" +
		"EMAIL;TYPE=home:bob@home.example\r\nEMAIL;TYPE=work:bob@work.example\r\nEND:VCARD\r\n",
	"carl.vcf": "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:carl\r\nFN:Çarl Ünal\r\nN:Ünal;Çarl;;;\r\n" +
		"item1.EMAIL:carl@group.example\r\nitem1.X-ABLabel:Other\r\nEND:VCARD\r\n",
}

func newReportFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	for n, d := range testCards {
		if _, err := f.mem.PutObject(context.Background(), f.bookRef(), n, []byte(d), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) bookRef() store.CollectionRef {
	return store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
}

func (f *fixture) report(target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("REPORT", target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func query(inner string) string {
	return `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:prop><D:getetag/></D:prop>` + inner + `</CR:addressbook-query>`
}

func filter(attr, inner string) string {
	return `<CR:filter ` + attr + `>` + inner + `</CR:filter>`
}

func bases(r reply) []string {
	var out []string
	for _, h := range r.hrefs {
		out = append(out, path.Base(h))
	}
	return out
}

func TestQueryFilters(t *testing.T) {
	tm := func(attrs, v string) string { return `<CR:text-match ` + attrs + `>` + v + `</CR:text-match>` }
	pf := func(name, attrs, inner string) string {
		return `<CR:prop-filter name="` + name + `" ` + attrs + `>` + inner + `</CR:prop-filter>`
	}
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{"contains", filter("", pf("FN", "", tm("", "ada"))), []string{"ada.vcf"}},
		{"case-insensitive upper", filter("", pf("EMAIL", "", tm("", "ADA"))), []string{"ada.vcf"}},
		{"equals", filter("", pf("FN", "", tm(`match-type="equals"`, "Bob Builder"))), []string{"bob.vcf"}},
		{"equals partial", filter("", pf("FN", "", tm(`match-type="equals"`, "bob"))), nil},
		{"starts-with", filter("", pf("FN", "", tm(`match-type="starts-with"`, "ada"))), []string{"ada.vcf"}},
		{"ends-with", filter("", pf("FN", "", tm(`match-type="ends-with"`, "builder"))), []string{"bob.vcf"}},
		{"octet case sensitive miss", filter("", pf("FN", "", tm(`collation="i;octet"`, "ada"))), nil},
		{"octet case sensitive hit", filter("", pf("FN", "", tm(`collation="i;octet"`, "Ada"))), []string{"ada.vcf"}},
		{"ascii-casemap folds ascii", filter("", pf("FN", "", tm(`collation="i;ascii-casemap"`, "BOB"))), []string{"bob.vcf"}},
		{"ascii-casemap does not fold unicode", filter("", pf("FN", "", tm(`collation="i;ascii-casemap"`, "çarl"))), nil},
		{"unicode-casemap folds unicode", filter("", pf("FN", "", tm(`collation="i;unicode-casemap"`, "çARL"))), []string{"carl.vcf"}},
		{"negate", filter("", pf("FN", "", tm(`negate-condition="yes"`, "ada"))), []string{"bob.vcf", "carl.vcf"}},
		{"negate no", filter("", pf("FN", "", tm(`negate-condition="no"`, "ada"))), []string{"ada.vcf"}},
		{"anyof default", filter("", pf("FN", "", tm("", "ada"))+pf("EMAIL", "", tm("", "bob"))), []string{"ada.vcf", "bob.vcf"}},
		{"filter allof", filter(`test="allof"`, pf("FN", "", tm("", "a"))+pf("TEL", "", `<CR:is-not-defined/>`)), []string{"carl.vcf"}},
		{"prop-filter allof", filter("", pf("EMAIL", `test="allof"`, tm("", "bob")+tm("", "work"))), []string{"bob.vcf"}},
		{"prop-filter anyof", filter("", pf("EMAIL", `test="anyof"`, tm("", "ada")+tm("", "home"))), []string{"ada.vcf", "bob.vcf"}},
		{"is-not-defined", filter("", pf("TEL", "", `<CR:is-not-defined/>`)), []string{"bob.vcf", "carl.vcf"}},
		{"second email matches", filter("", pf("EMAIL", "", tm("", "work.example"))), []string{"bob.vcf"}},
		{"group property", filter("", pf("EMAIL", "", tm("", "group.example"))), []string{"carl.vcf"}},
		{"param-filter text-match", filter("", pf("EMAIL", "", `<CR:param-filter name="TYPE">`+tm("", "home")+`</CR:param-filter>`)), []string{"bob.vcf"}},
		{"param-filter is-not-defined", filter("", pf("EMAIL", "", `<CR:param-filter name="TYPE"><CR:is-not-defined/></CR:param-filter>`)), []string{"carl.vcf"}},
		{"no prop-filter matches all", filter("", ""), []string{"ada.vcf", "bob.vcf", "carl.vcf"}},
		{"no filter matches all", "", []string{"ada.vcf", "bob.vcf", "carl.vcf"}},
		{"prop-filter without conditions", filter("", pf("TEL", "", "")), []string{"ada.vcf"}},
	}
	f := newReportFixture(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.report(bookURL, query(tt.filter))
			r := parseReply(t, rec)
			got := bases(r)
			if !slices.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			for i := range r.hrefs {
				if r.find(t, i, 200, dav.GetETag) == nil {
					t.Errorf("response %d has no getetag", i)
				}
			}
		})
	}
}

func TestQueryClientBodies(t *testing.T) {
	f := newReportFixture(t)
	thunderbird := `<?xml version="1.0" encoding="UTF-8"?>
<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><CR:address-data content-type="text/vcard" version="3.0"><CR:prop name="FN"/><CR:prop name="EMAIL"/></CR:address-data></D:prop>
  <CR:filter test="anyof"><CR:prop-filter name="FN"><CR:text-match collation="i;unicode-casemap" match-type="contains">ADA</CR:text-match></CR:prop-filter></CR:filter>
  <CR:limit><CR:nresults>50</CR:nresults></CR:limit>
</CR:addressbook-query>`
	r := parseReply(t, f.report(bookURL, thunderbird))
	if !slices.Equal(bases(r), []string{"ada.vcf"}) {
		t.Fatalf("got %v", bases(r))
	}
	if got := r.must(t, 0, dav.AddressData).Text; got != testCards["ada.vcf"] {
		t.Errorf("address-data = %q, want the full stored card", got)
	}

	apple := `<?xml version="1.0" encoding="UTF-8"?>
<A:addressbook-query xmlns:A="urn:ietf:params:xml:ns:carddav" xmlns:B="DAV:">
  <B:prop><B:getetag/><A:address-data/></B:prop>
  <A:filter><A:prop-filter name="EMAIL"><A:text-match match-type="contains">work</A:text-match></A:prop-filter></A:filter>
</A:addressbook-query>`
	r = parseReply(t, f.report(bookURL, apple))
	if !slices.Equal(bases(r), []string{"bob.vcf"}) {
		t.Fatalf("got %v", bases(r))
	}
	r.must(t, 0, dav.GetETag)
	r.must(t, 0, dav.AddressData)
}

func TestQueryDefaultsToAllprop(t *testing.T) {
	f := newReportFixture(t)
	r := parseReply(t, f.report(bookURL, `<CR:addressbook-query `+nsDecl+`/>`))
	if len(r.hrefs) != 3 {
		t.Fatalf("got %v", r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	if r.find(t, 0, 200, dav.AddressData) != nil {
		t.Error("allprop returned address-data")
	}
	r = parseReply(t, f.report(bookURL, `<CR:addressbook-query `+nsDecl+`><D:propname/></CR:addressbook-query>`))
	r.must(t, 0, dav.GetETag)
}

func TestQueryWithoutFilterReadsNoData(t *testing.T) {
	f := newReportFixture(t)
	r := parseReply(t, f.report(bookURL, query("")))
	if len(r.hrefs) != 3 {
		t.Fatalf("got %v", r.hrefs)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("GetObject called %d times, want 0", n)
	}
	if n := f.st.dataLists.Load(); n != 0 {
		t.Errorf("listed with data %d times", n)
	}
	f.st.gets.Store(0)
	parseReply(t, f.report(bookURL, query(filter("", ""))))
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("empty filter: GetObject called %d times, want 0", n)
	}
}

func TestQueryLimit(t *testing.T) {
	f := newReportFixture(t)
	lim := func(n string) string { return `<CR:limit><CR:nresults>` + n + `</CR:nresults></CR:limit>` }

	r := parseReply(t, f.report(bookURL, query(lim("2"))))
	if want := []string{"ada.vcf", "bob.vcf", "contacts"}; !slices.Equal(bases(r), want) {
		t.Fatalf("got %v, want %v", bases(r), want)
	}
	last := r.root.Kids(dav.ResponseEl)[2]
	if last.Child(dav.Href).Text != bookURL {
		t.Errorf("truncation href = %q", last.Child(dav.Href).Text)
	}
	if !strings.Contains(last.Child(dav.Status).Text, "507") {
		t.Errorf("status = %q", last.Child(dav.Status).Text)
	}
	if last.Child(dav.ErrorEl).Child(dav.NumberOfMatchesWithinLimits) == nil {
		t.Error("507 response lacks number-of-matches-within-limits")
	}

	for _, n := range []string{"3", "10"} {
		r = parseReply(t, f.report(bookURL, query(lim(n))))
		if len(r.hrefs) != 3 {
			t.Errorf("nresults %s: got %v, want 3 plain results", n, r.hrefs)
		}
	}
	r = parseReply(t, f.report(bookURL, query(lim("0"))))
	if len(r.hrefs) != 1 || !strings.Contains(r.root.Kids(dav.ResponseEl)[0].Child(dav.Status).Text, "507") {
		t.Errorf("nresults 0: got %v", r.hrefs)
	}
	for _, n := range []string{"-1", "abc", "", "1.5"} {
		if rec := f.report(bookURL, query(lim(n))); rec.Code != http.StatusBadRequest {
			t.Errorf("nresults %q: status %d, want 400", n, rec.Code)
		}
	}
}

func multiget(inner string) string {
	return `<?xml version="1.0"?><CR:addressbook-multiget ` + nsDecl + `><D:prop><D:getetag/><CR:address-data/></D:prop>` + inner + `</CR:addressbook-multiget>`
}

func hrefEl(h string) string { return `<D:href>` + h + `</D:href>` }

func TestMultiget(t *testing.T) {
	f := newReportFixture(t)
	if _, err := f.mem.CreateCollection(context.Background(),
		store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "other"}, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.PutObject(context.Background(),
		store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "other"}, "x.vcf", vcardFor("x"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	hrefs := []string{
		"/dav/addressbooks/alice/contacts/bob.vcf",
		"/dav/addressbooks/alice/contacts/missing.vcf",
		"/dav/addressbooks/alice/other/x.vcf",
		"/dav/addressbooks/bob/contacts/ada.vcf",
		"/dav/addressbooks/alice/contacts/%2e%2e",
		"/dav/addressbooks/alice/contacts/../other/x.vcf",
		"/dav/addressbooks/alice/contacts/a%00b.vcf",
		"/dav/addressbooks/alice/contacts/" + strings.Repeat("a", 5000),
		"http://example.com/dav/addressbooks/alice/contacts/ada.vcf",
		"/dav/addressbooks/alice/contacts/bob.vcf", // duplicate
		"garbage",
		"",
		"/dav/addressbooks/alice/contacts/",
		"/dav/addressbooks/alice/contacts/a%2Fb",
	}
	var inner strings.Builder
	for _, h := range hrefs {
		inner.WriteString(hrefEl(strings.ReplaceAll(h, "&", "&amp;")))
	}
	f.st.gets.Store(0)
	rec := f.report(bookURL, multiget(inner.String()))
	r := parseReply(t, rec)
	resps := r.root.Kids(dav.ResponseEl)
	status := func(i int) string {
		if s := resps[i].Child(dav.Status); s != nil {
			return s.Text
		}
		return "propstat"
	}
	want := []string{
		"propstat", "404", "404", "404", "404", "404", "404", "404", "propstat", "", "404", "404", "404", "404",
	}
	// The duplicate is dropped.
	want = slices.Delete(want, 9, 10)
	if len(resps) != len(want) {
		t.Fatalf("got %d responses %v, want %d", len(resps), r.hrefs, len(want))
	}
	for i, w := range want {
		if !strings.Contains(status(i), w) {
			t.Errorf("response %d (%q): status %q, want %s", i, r.hrefs[i], status(i), w)
		}
	}
	if got := r.must(t, 0, dav.AddressData).Text; got != testCards["bob.vcf"] {
		t.Errorf("bob data = %q", got)
	}
	if r.hrefs[0] != "/dav/addressbooks/alice/contacts/bob.vcf" {
		t.Errorf("href = %q", r.hrefs[0])
	}
	if n := f.st.gets.Load(); n > 6 {
		t.Errorf("hostile hrefs reached the store: %d reads", n)
	}
}

func TestMultigetEncodedName(t *testing.T) {
	f := newFixture(t)
	if _, err := f.mem.PutObject(context.Background(), f.bookRef(), "a b&c.vcf", vcardFor("s"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	r := parseReply(t, f.report(bookURL, multiget(hrefEl("/dav/addressbooks/alice/contacts/a%20b&amp;c.vcf"))))
	if len(r.hrefs) != 1 {
		t.Fatal(r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	// %252e is the literal text "%2e", decoded once, not a traversal.
	r = parseReply(t, f.report(bookURL, multiget(hrefEl("/dav/addressbooks/alice/contacts/%252e%252e"))))
	if !strings.Contains(r.root.Kids(dav.ResponseEl)[0].Child(dav.Status).Text, "404") {
		t.Error("want 404")
	}
}

func TestMultigetTooManyHrefs(t *testing.T) {
	f := newReportFixture(t)
	var inner strings.Builder
	for i := 0; i < 1001; i++ {
		inner.WriteString(hrefEl(fmt.Sprintf("/dav/addressbooks/alice/contacts/n%d", i)))
	}
	if rec := f.report(bookURL, multiget(inner.String())); rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
}

func TestMultigetStoreErrorIs500PerHref(t *testing.T) {
	f := newReportFixture(t)
	f.st.fail = map[string]bool{"bob.vcf": true}
	r := parseReply(t, f.report(bookURL, multiget(
		hrefEl("/dav/addressbooks/alice/contacts/ada.vcf")+hrefEl("/dav/addressbooks/alice/contacts/bob.vcf"))))
	resps := r.root.Kids(dav.ResponseEl)
	if len(resps) != 2 {
		t.Fatal(r.hrefs)
	}
	r.must(t, 0, dav.GetETag)
	if s := resps[1].Child(dav.Status).Text; !strings.Contains(s, "500") {
		t.Errorf("status %q, want 500", s)
	}
	if strings.Contains(resps[1].Child(dav.Status).Text, "fire") {
		t.Error("error detail leaked")
	}
}

func TestOwnerBoundary(t *testing.T) {
	mem := memory.New()
	ctx := context.Background()
	for _, o := range []string{"owner-alice", "owner-bob"} {
		ref := store.CollectionRef{Owner: o, Kind: store.KindAddressBook, Name: "contacts"}
		if _, err := mem.CreateCollection(ctx, ref, store.Props{}); err != nil {
			t.Fatal(err)
		}
		if _, err := mem.PutObject(ctx, ref, "c.vcf", vcardFor(o), store.PutOptions{}); err != nil {
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
		req := httptest.NewRequest("REPORT", target, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return parseReply(t, rec)
	}
	r := run("/dav/addressbooks/bob/contacts/", multiget(
		hrefEl("/dav/addressbooks/alice/contacts/c.vcf")+hrefEl("/dav/addressbooks/bob/contacts/c.vcf")))
	resps := r.root.Kids(dav.ResponseEl)
	if !strings.Contains(resps[0].Child(dav.Status).Text, "404") {
		t.Error("bob reached alice's object")
	}
	if got := r.must(t, 1, dav.AddressData).Text; !strings.Contains(got, "owner-bob") {
		t.Errorf("bob's own data = %q", got)
	}
	r = run("/dav/addressbooks/bob/contacts/", query(""))
	if len(r.hrefs) != 1 || !strings.Contains(r.hrefs[0], "/bob/") {
		t.Errorf("query hrefs = %v", r.hrefs)
	}
}

func TestQuerySkipsUnreadableAndUnparsable(t *testing.T) {
	f := newReportFixture(t)
	ctx := context.Background()
	if _, err := f.mem.PutObject(ctx, f.bookRef(), "broken.vcf", []byte("not a vcard"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.PutObject(ctx, f.bookRef(), "dead.vcf", vcardFor("dead"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	f.st.fail = map[string]bool{"dead.vcf": true}
	r := parseReply(t, f.report(bookURL, query(filter("", `<CR:prop-filter name="FN"><CR:is-not-defined/></CR:prop-filter>`))))
	if len(r.hrefs) != 0 {
		t.Errorf("got %v", r.hrefs)
	}
	r = parseReply(t, f.report(bookURL, query(filter("", `<CR:prop-filter name="UID"/>`))))
	if want := []string{"ada.vcf", "bob.vcf", "carl.vcf"}; !slices.Equal(bases(r), want) {
		t.Errorf("got %v, want %v", bases(r), want)
	}
}

func TestQueryFilterErrors(t *testing.T) {
	f := newReportFixture(t)
	pf := func(inner string) string { return filter("", `<CR:prop-filter name="FN">`+inner+`</CR:prop-filter>`) }
	many := strings.Repeat(`<CR:prop-filter name="FN"/>`, 101)
	manyTM := strings.Repeat(`<CR:text-match>a</CR:text-match>`, 101)
	tests := []struct {
		name, filter, pre string
	}{
		{"unknown collation", pf(`<CR:text-match collation="i;bogus">a</CR:text-match>`), "supported-collation"},
		{"unknown match-type", pf(`<CR:text-match match-type="regex">a</CR:text-match>`), "valid-filter"},
		{"unknown filter test", filter(`test="xor"`, ""), "valid-filter"},
		{"unknown prop-filter test", filter("", `<CR:prop-filter name="FN" test="xor"/>`), "valid-filter"},
		{"missing name", filter("", `<CR:prop-filter/>`), "valid-filter"},
		{"bad negate", pf(`<CR:text-match negate-condition="maybe">a</CR:text-match>`), "valid-filter"},
		{"unknown child", filter("", `<CR:bogus/>`), "valid-filter"},
		{"unknown prop-filter child", pf(`<CR:bogus/>`), "valid-filter"},
		{"is-not-defined with text-match", pf(`<CR:is-not-defined/><CR:text-match>a</CR:text-match>`), "valid-filter"},
		{"param without name", pf(`<CR:param-filter/>`), "valid-filter"},
		{"too many prop-filters", filter("", many), "valid-filter"},
		{"too many text-matches", pf(manyTM), "valid-filter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := f.report(bookURL, query(tt.filter))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403; %s", rec.Code, rec.Body)
			}
			root, err := dav.ParseXML(rec.Body.Bytes())
			if err != nil {
				t.Fatalf("body %q: %v", rec.Body, err)
			}
			if len(root.Children) != 1 || root.Children[0].Name.Local != tt.pre {
				t.Errorf("precondition = %v, want %s", root.Children, tt.pre)
			}
		})
	}
}

func TestReportDispatch(t *testing.T) {
	f := newReportFixture(t)
	pre := func(rec *httptest.ResponseRecorder) string {
		root, err := dav.ParseXML(rec.Body.Bytes())
		if err != nil || len(root.Children) != 1 {
			return rec.Body.String()
		}
		return root.Children[0].Name.Local
	}
	rec := f.report(bookURL, `<D:bogus `+nsDecl+`/>`)
	if rec.Code != 403 || pre(rec) != "supported-report" {
		t.Errorf("unknown root: %d %s", rec.Code, rec.Body)
	}
	rec = f.report(bookURL, `<D:sync-collection `+nsDecl+`><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`)
	if rec.Code != http.StatusMultiStatus {
		t.Errorf("sync-collection: %d %s", rec.Code, rec.Body)
	}
	if rec := f.report(bookURL, `<oops`); rec.Code != 400 {
		t.Errorf("malformed: %d", rec.Code)
	}
	if rec := f.report(bookURL, ``); rec.Code != 400 {
		t.Errorf("empty: %d", rec.Code)
	}
	if rec := f.report(bookURL, query("")+strings.Repeat(" ", 2<<20)); rec.Code != 413 {
		t.Errorf("large: %d", rec.Code)
	}
	if rec := f.report("/dav/addressbooks/alice/nope/", query("")); rec.Code != 404 {
		t.Errorf("missing book: %d", rec.Code)
	}
	for _, u := range []string{"/dav/addressbooks/alice/", "/dav/addressbooks/alice/contacts/ada.vcf"} {
		rec := f.report(u, query(""))
		if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "PROPFIND") {
			t.Errorf("%s: %d allow %q", u, rec.Code, rec.Header().Get("Allow"))
		}
	}
}

func TestQueryStoreListFailureIs500(t *testing.T) {
	f := newReportFixture(t)
	f.h.store = listFailStore{f.st}
	rec := f.report(bookURL, query(""))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("status %d body %q", rec.Code, rec.Body)
	}
}

type listFailStore struct{ store.Store }

func (listFailStore) ListObjects(context.Context, store.CollectionRef, store.ListOptions) ([]store.Object, error) {
	return nil, fmt.Errorf("boom")
}

func TestQueryLargeBook(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 3000; i++ {
		d := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:u%d\r\nFN:Person %d\r\nEMAIL:p%d@example.org\r\nEND:VCARD\r\n", i, i, i)
		if _, err := f.mem.PutObject(ctx, f.bookRef(), fmt.Sprintf("p%d.vcf", i), []byte(d), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	rec := f.report(bookURL, query(filter("", `<CR:prop-filter name="EMAIL"><CR:text-match>p2999@</CR:text-match></CR:prop-filter>`)))
	el := time.Since(start)
	r := parseReply(t, rec)
	if len(r.hrefs) != 1 {
		t.Errorf("got %d results", len(r.hrefs))
	}
	if el > 10*time.Second {
		t.Errorf("query took %v", el)
	}
}

func FuzzReport(f *testing.F) {
	fx := &fixture{}
	mem := memory.New()
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := mem.CreateCollection(ctx, ref, store.Props{}); err != nil {
		f.Fatal(err)
	}
	for n, d := range testCards {
		if _, err := mem.PutObject(ctx, ref, n, []byte(d), store.PutOptions{}); err != nil {
			f.Fatal(err)
		}
	}
	h, err := New(Config{Store: mem, Prefix: "/dav", Resolve: resolveAs("alice")})
	if err != nil {
		f.Fatal(err)
	}
	fx.h = h
	f.Add(query(filter("", `<CR:prop-filter name="FN"><CR:text-match>ada</CR:text-match></CR:prop-filter>`)))
	f.Add(multiget(hrefEl("/dav/addressbooks/alice/contacts/ada.vcf")))
	f.Add(`<D:sync-collection xmlns:D="DAV:"/>`)
	f.Add(query(`<CR:limit><CR:nresults>1</CR:nresults></CR:limit>`))
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest("REPORT", bookURL, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("status %d for %q", rec.Code, body)
		}
	})
}

// A query that selects every card but asks for address-data reads each card
// anyway. One whose data does not parse is skipped, as with a filter:
// clients parse every address-data they get, and one bad object would fail
// their whole query.
func TestQueryWithAddressDataSkipsUnparsableObjects(t *testing.T) {
	f := newReportFixture(t)
	for n, d := range map[string]string{
		"garbage.vcf": "\x00\xff not a card at all",
		"cal.vcf":     "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n",
	} {
		if _, err := f.mem.PutObject(context.Background(), f.bookRef(), n, []byte(d), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var logs strings.Builder
	f.h.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

	data := `<D:prop><D:getetag/><CR:address-data/></D:prop>`
	for name, flt := range map[string]string{
		"no filter":    "",
		"empty filter": filter("", ""),
	} {
		t.Run(name, func(t *testing.T) {
			r := parseReply(t, f.report(bookURL, `<CR:addressbook-query `+nsDecl+`>`+data+flt+`</CR:addressbook-query>`))
			if want := []string{"ada.vcf", "bob.vcf", "carl.vcf"}; !slices.Equal(bases(r), want) {
				t.Errorf("got %v, want %v", bases(r), want)
			}
		})
	}
	if out := logs.String(); !strings.Contains(out, "garbage.vcf") || !strings.Contains(out, "cal.vcf") {
		t.Errorf("warnings not logged by name:\n%s", out)
	}

	// Without address-data nothing is read, and every object is listed.
	f.st.gets.Store(0)
	r := parseReply(t, f.report(bookURL, query("")))
	if want := []string{"ada.vcf", "bob.vcf", "cal.vcf", "carl.vcf", "garbage.vcf"}; !slices.Equal(bases(r), want) {
		t.Errorf("hrefs only: got %v, want %v", bases(r), want)
	}
	if n := f.st.gets.Load(); n != 0 {
		t.Errorf("hrefs only: GetObject called %d times, want 0", n)
	}
}
