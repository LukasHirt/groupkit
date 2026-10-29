// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

// countingStore counts GetObject calls and can fail chosen objects.
type countingStore struct {
	store.Store
	gets atomic.Int64
	// dataLists counts ListObjects calls made with IncludeData.
	dataLists atomic.Int64
	fail      map[string]bool
}

func (c *countingStore) ListObjects(ctx context.Context, ref store.CollectionRef, opts store.ListOptions) ([]store.Object, error) {
	if opts.IncludeData {
		c.dataLists.Add(1)
	}
	return c.Store.ListObjects(ctx, ref, opts)
}

func (c *countingStore) GetObject(ctx context.Context, ref store.CollectionRef, name string) (store.Object, error) {
	c.gets.Add(1)
	if c.fail[name] {
		return store.Object{}, errors.New("disk on fire")
	}
	return c.Store.GetObject(ctx, ref, name)
}

func vcardFor(name string) []byte {
	return []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + name + "\r\nFN:" + name + "\r\nEND:VCARD\r\n")
}

type fixture struct {
	h     *Handler
	st    *countingStore
	mem   store.Store
	book  store.Collection
	owner string
}

// newFixture makes owner "alice" with the book "contacts" holding the named objects.
func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	mem := memory.New()
	st := &countingStore{Store: mem}
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	_, err := mem.CreateCollection(context.Background(), ref, store.Props{DisplayName: "My Contacts", Description: "People"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := mem.PutObject(context.Background(), ref, n, vcardFor(n), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	book, _ := mem.GetCollection(context.Background(), ref)
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: h, st: st, mem: mem, book: book, owner: "alice"}
}

func (f *fixture) propfind(t *testing.T, target, depth, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PROPFIND", target, strings.NewReader(body))
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if n := f.st.dataLists.Load(); n != 0 {
		t.Errorf("PROPFIND listed objects with data %d times", n)
	}
	return rec
}

// reply is a parsed 207 response.
type reply struct {
	hrefs []string
	// props[i] maps status to the property nodes of response i.
	props []map[int][]*dav.Node
	root  *dav.Node
}

func parseReply(t *testing.T, rec *httptest.ResponseRecorder) reply {
	t.Helper()
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207; body %s", rec.Code, rec.Body)
	}
	root, err := dav.ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, rec.Body)
	}
	r := reply{root: root}
	for _, resp := range root.Kids(dav.ResponseEl) {
		r.hrefs = append(r.hrefs, resp.Child(dav.Href).Text)
		m := map[int][]*dav.Node{}
		for _, ps := range resp.Kids(dav.PropstatEl) {
			var code int
			if _, err := fmt.Sscanf(ps.Child(dav.Status).Text, "HTTP/1.1 %d", &code); err != nil {
				t.Fatalf("status: %v", err)
			}
			m[code] = append(m[code], ps.Child(dav.PropEl).Children...)
		}
		r.props = append(r.props, m)
	}
	return r
}

func (r reply) find(t *testing.T, i, status int, name xml.Name) *dav.Node {
	t.Helper()
	for _, n := range r.props[i][status] {
		if n.Name == name {
			return n
		}
	}
	return nil
}

func (r reply) must(t *testing.T, i int, name xml.Name) *dav.Node {
	t.Helper()
	n := r.find(t, i, 200, name)
	if n == nil {
		t.Fatalf("response %d (%s) has no %v in 200", i, r.hrefs[i], name)
	}
	return n
}

func names(nodes []*dav.Node) []string {
	var out []string
	for _, n := range nodes {
		for _, c := range n.Children {
			out = append(out, c.Name.Local)
		}
	}
	return out
}

func propBody(inner string) string {
	return `<?xml version="1.0"?><D:propfind xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/"><D:prop>` + inner + `</D:prop></D:propfind>`
}

func TestPropfindHome(t *testing.T) {
	f := newFixture(t, "a.vcf")
	body := propBody(`<D:resourcetype/><D:displayname/><D:current-user-principal/><D:owner/><D:current-user-privilege-set/><D:supported-report-set/>`)

	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/addressbooks/alice/" {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if got := names([]*dav.Node{r.must(t, 0, dav.ResourceType)}); len(got) != 1 || got[0] != "collection" {
		t.Errorf("resourcetype = %v", got)
	}
	if got := r.must(t, 0, dav.DisplayName).Text; got != "alice" {
		t.Errorf("displayname = %q", got)
	}
	for _, n := range []xml.Name{dav.CurrentUserPrincipal, dav.Owner} {
		if got := r.must(t, 0, n).Child(dav.Href).Text; got != "/dav/principals/alice/" {
			t.Errorf("%v = %q", n, got)
		}
	}
	var privs []string
	for _, p := range r.must(t, 0, dav.CurrentUserPrivilegeSet).Kids(dav.Privilege) {
		privs = append(privs, p.Children[0].Name.Local)
	}
	if strings.Join(privs, ",") != "read,bind" {
		t.Errorf("home privileges = %v", privs)
	}
	if n := r.must(t, 0, dav.SupportedReportSet); len(n.Children) != 0 {
		t.Errorf("home supported-report-set not empty")
	}

	// Depth 1 lists the books.
	r = parseReply(t, f.propfind(t, "/dav/addressbooks/alice/", "1", body))
	if strings.Join(r.hrefs, " ") != "/dav/addressbooks/alice/ /dav/addressbooks/alice/contacts/" {
		t.Fatalf("depth 1 hrefs = %v", r.hrefs)
	}
	if got := names([]*dav.Node{r.must(t, 1, dav.ResourceType)}); strings.Join(got, ",") != "collection,addressbook" {
		t.Errorf("book resourcetype = %v", got)
	}
}

func TestPropfindBookDepth0(t *testing.T) {
	f := newFixture(t, "a.vcf", "b.vcf")
	body := propBody(`<D:resourcetype/><D:displayname/><CS:getctag/><D:sync-token/><CR:addressbook-description/>
		<CR:supported-address-data/><CR:max-resource-size/><D:supported-report-set/><D:current-user-privilege-set/>`)
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/addressbooks/alice/contacts/" {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if got := r.must(t, 0, dav.DisplayName).Text; got != "My Contacts" {
		t.Errorf("displayname = %q", got)
	}
	rev := fmt.Sprint(f.book.Revision)
	if got := r.must(t, 0, dav.GetCTag).Text; got != rev || rev == "0" {
		t.Errorf("getctag = %q, want %q", got, rev)
	}
	if got, want := r.must(t, 0, dav.SyncToken).Text, "urn:groupkit:sync:"+f.book.ID+":"+rev; got != want {
		t.Errorf("sync-token = %q, want %q", got, want)
	}
	if got := r.must(t, 0, dav.AddressbookDescription).Text; got != "People" {
		t.Errorf("description = %q", got)
	}
	sad := r.must(t, 0, dav.SupportedAddressData)
	var versions []string
	for _, c := range sad.Kids(dav.CRAddressDataType) {
		versions = append(versions, c.Attr("content-type")+"/"+c.Attr("version"))
	}
	if strings.Join(versions, ",") != "text/vcard/3.0,text/vcard/4.0" {
		t.Errorf("supported-address-data = %v", versions)
	}
	if got := r.must(t, 0, dav.CRMaxResourceSize).Text; got != "1048576" {
		t.Errorf("max-resource-size = %q", got)
	}
	var reports []string
	for _, sr := range r.must(t, 0, dav.SupportedReportSet).Kids(dav.SupportedReport) {
		reports = append(reports, sr.Child(dav.Report).Children[0].Name.Local)
	}
	if strings.Join(reports, ",") != "addressbook-query,addressbook-multiget,sync-collection" {
		t.Errorf("reports = %v", reports)
	}
	var privs []string
	for _, p := range r.must(t, 0, dav.CurrentUserPrivilegeSet).Kids(dav.Privilege) {
		privs = append(privs, p.Children[0].Name.Local)
	}
	if strings.Join(privs, ",") != "read,write,write-properties,write-content,bind,unbind" {
		t.Errorf("privileges = %v", privs)
	}
}

func TestPropfindBookDisplayNameFallbackAndEmptyDescription(t *testing.T) {
	f := newFixture(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "plain"}
	if _, err := f.mem.CreateCollection(context.Background(), ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/plain/", "0", propBody(`<D:displayname/><CR:addressbook-description/>`)))
	if got := r.must(t, 0, dav.DisplayName).Text; got != "plain" {
		t.Errorf("displayname = %q", got)
	}
	if got := r.must(t, 0, dav.AddressbookDescription).Text; got != "" {
		t.Errorf("description = %q", got)
	}
}

func TestPropfindBookDepth1Objects(t *testing.T) {
	f := newFixture(t, "c b.vcf", "a.vcf", "ü.vcf")
	body := propBody(`<D:getetag/><D:getcontenttype/><D:getcontentlength/><D:getlastmodified/><D:displayname/><D:resourcetype/><D:creationdate/>`)
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", body))
	want := []string{
		"/dav/addressbooks/alice/contacts/",
		"/dav/addressbooks/alice/contacts/a.vcf",
		"/dav/addressbooks/alice/contacts/c%20b.vcf",
		"/dav/addressbooks/alice/contacts/%C3%BC.vcf",
	}
	if strings.Join(r.hrefs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("hrefs = %v\nwant %v", r.hrefs, want)
	}
	for i, name := range []string{"", "a.vcf", "c b.vcf", "ü.vcf"} {
		if i == 0 {
			continue
		}
		data := vcardFor(name)
		if got, want := r.must(t, i, dav.GetETag).Text, `"`+store.ETag(data)+`"`; got != want {
			t.Errorf("%s etag = %q, want %q", name, got, want)
		}
		if got := r.must(t, i, dav.GetContentType).Text; got != "text/vcard; charset=utf-8" {
			t.Errorf("content type = %q", got)
		}
		if got := r.must(t, i, dav.GetContentLength).Text; got != fmt.Sprint(len(data)) {
			t.Errorf("%s length = %q, want %d", name, got, len(data))
		}
		if got := r.must(t, i, dav.GetLastModified).Text; !strings.HasSuffix(got, "GMT") {
			t.Errorf("last modified = %q", got)
		}
		if got := r.must(t, i, dav.DisplayName).Text; got != name {
			t.Errorf("displayname = %q", got)
		}
		if n := r.must(t, i, dav.ResourceType); len(n.Children) != 0 {
			t.Errorf("object resourcetype not empty")
		}
		if r.find(t, i, 404, dav.CreationDate) == nil {
			t.Errorf("creationdate should be 404")
		}
	}
	if f.st.gets.Load() != 0 {
		t.Errorf("GetObject called %d times", f.st.gets.Load())
	}
}

func TestPropfindObjectDepth0(t *testing.T) {
	f := newFixture(t, "a.vcf")
	body := propBody(`<D:getetag/><CR:address-data/><D:owner/><D:current-user-privilege-set/><D:supported-report-set/>`)
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/a.vcf", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/addressbooks/alice/contacts/a.vcf" {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if got := r.must(t, 0, dav.AddressData).Text; got != string(vcardFor("a.vcf")) {
		t.Errorf("address-data = %q", got)
	}
	var privs []string
	for _, p := range r.must(t, 0, dav.CurrentUserPrivilegeSet).Kids(dav.Privilege) {
		privs = append(privs, p.Children[0].Name.Local)
	}
	if strings.Join(privs, ",") != "read,write,write-content" {
		t.Errorf("privileges = %v", privs)
	}
	if f.st.gets.Load() != 1 {
		t.Errorf("gets = %d", f.st.gets.Load())
	}

	rec := f.propfind(t, "/dav/addressbooks/alice/contacts/nope.vcf", "0", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing object status = %d", rec.Code)
	}
}

func TestPropfindAllpropOmitsAddressData(t *testing.T) {
	f := newFixture(t, "a.vcf")
	for _, body := range []string{"", `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`} {
		r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", body))
		if len(r.hrefs) != 2 {
			t.Fatalf("hrefs = %v", r.hrefs)
		}
		if r.find(t, 1, 200, dav.GetETag) == nil {
			t.Errorf("allprop lacks getetag")
		}
		for i := range r.hrefs {
			for _, n := range r.props[i][200] {
				if n.Name == dav.AddressData {
					t.Errorf("allprop returned address-data")
				}
			}
		}
	}
	if f.st.gets.Load() != 0 {
		t.Errorf("GetObject called %d times", f.st.gets.Load())
	}
}

func TestPropfindPropname(t *testing.T) {
	f := newFixture(t, "a.vcf")
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "1",
		`<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`))
	has := func(i int, n xml.Name) bool { return r.find(t, i, 200, n) != nil }
	if !has(0, dav.GetCTag) || !has(0, dav.SyncToken) {
		t.Errorf("book propname incomplete: %v", r.props[0])
	}
	if !has(1, dav.GetETag) || !has(1, dav.AddressData) {
		t.Errorf("object propname incomplete: %v", r.props[1])
	}
	for _, n := range r.props[1][200] {
		if len(n.Children) != 0 || n.Text != "" {
			t.Errorf("propname value for %v not empty", n.Name)
		}
	}
	if f.st.gets.Load() != 0 {
		t.Errorf("GetObject called %d times", f.st.gets.Load())
	}
}

func TestPropfindUnknownProperty(t *testing.T) {
	f := newFixture(t)
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "0",
		propBody(`<D:displayname/><X:nope xmlns:X="urn:x"/>`)))
	if r.must(t, 0, dav.DisplayName).Text != "My Contacts" {
		t.Errorf("displayname missing")
	}
	n := r.find(t, 0, 404, xml.Name{Space: "urn:x", Local: "nope"})
	if n == nil {
		t.Errorf("unknown property not in 404: %v", r.props[0])
	}
}

func TestPropfindAddressDataExactBytes(t *testing.T) {
	f := newFixture(t, "a.vcf", "b.vcf", "c.vcf")
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", propBody(`<D:getetag/><CR:address-data/>`)))
	for i, name := range []string{"", "a.vcf", "b.vcf", "c.vcf"} {
		if i == 0 {
			if r.find(t, 0, 404, dav.AddressData) == nil {
				t.Errorf("book address-data should be 404")
			}
			continue
		}
		if got := r.must(t, i, dav.AddressData).Text; got != string(vcardFor(name)) {
			t.Errorf("%s address-data = %q", name, got)
		}
	}
	if got := f.st.gets.Load(); got != 3 {
		t.Errorf("GetObject calls = %d, want 3", got)
	}
}

func TestPropfindManyObjectsReadsNoData(t *testing.T) {
	f := newFixture(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	for i := 0; i < 5000; i++ {
		if _, err := f.mem.PutObject(context.Background(), ref, fmt.Sprintf("c%05d.vcf", i), vcardFor("x"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", propBody(`<D:getetag/>`))
	// The body is past the parser's size cap, so count the responses.
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d", rec.Code)
	}
	if n := strings.Count(rec.Body.String(), "<D:response>"); n != 5001 {
		t.Fatalf("responses = %d", n)
	}
	if got := f.st.gets.Load(); got != 0 {
		t.Errorf("GetObject calls = %d, want 0", got)
	}
}

func TestPropfindFailingObject(t *testing.T) {
	f := newFixture(t, "a.vcf", "b.vcf", "c.vcf")
	f.st.fail = map[string]bool{"b.vcf": true}
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", propBody(`<D:getetag/><CR:address-data/>`)))
	if len(r.hrefs) != 4 {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if r.find(t, 2, 500, dav.AddressData) == nil {
		t.Errorf("b.vcf address-data should be 500: %v", r.props[2])
	}
	if r.must(t, 2, dav.GetETag).Text == "" {
		t.Errorf("b.vcf etag missing")
	}
	for _, i := range []int{1, 3} {
		if r.find(t, i, 200, dav.AddressData) == nil {
			t.Errorf("response %d lost address-data", i)
		}
	}
}

func TestPropfindMissingBook(t *testing.T) {
	f := newFixture(t)
	for _, depth := range []string{"0", "1"} {
		if rec := f.propfind(t, "/dav/addressbooks/alice/nope/", depth, ""); rec.Code != http.StatusNotFound {
			t.Errorf("Depth %s status = %d", depth, rec.Code)
		}
	}
	if rec := f.propfind(t, "/dav/addressbooks/alice/nope/x.vcf", "0", ""); rec.Code != http.StatusNotFound {
		t.Errorf("object in missing book status = %d", rec.Code)
	}
}

func TestPropfindDepth(t *testing.T) {
	f := newFixture(t, "a.vcf")
	for _, d := range []string{"infinity", "Infinity", "", "1"} {
		r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts/", d, ""))
		if len(r.hrefs) != 2 {
			t.Errorf("Depth %q gave %d responses", d, len(r.hrefs))
		}
	}
	if rec := f.propfind(t, "/dav/addressbooks/alice/contacts/", "2", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("Depth 2 status = %d", rec.Code)
	}
	if rec := f.propfind(t, "/dav/addressbooks/alice/contacts/", "0", "<nonsense"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad XML status = %d", rec.Code)
	}
	big := strings.Repeat(" ", dav.MaxXMLBytes+10)
	if rec := f.propfind(t, "/dav/addressbooks/alice/contacts/", "0", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big body status = %d", rec.Code)
	}
}

func TestPropfindUnicodeUserPrincipalHref(t *testing.T) {
	mem := memory.New()
	h, err := New(Config{Store: mem, Prefix: "/dav", Resolve: resolveAs("owner-1")})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PROPFIND", "/dav/addressbooks/J%C3%BCrgen%20M%C3%BCller/", bytes.NewReader([]byte(propBody(`<D:current-user-principal/><D:owner/><D:displayname/>`))))
	req.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := parseReply(t, rec)
	if r.hrefs[0] != "/dav/addressbooks/J%C3%BCrgen%20M%C3%BCller/" {
		t.Errorf("href = %q", r.hrefs[0])
	}
	if got := r.must(t, 0, dav.CurrentUserPrincipal).Child(dav.Href).Text; got != "/dav/principals/J%C3%BCrgen%20M%C3%BCller/" {
		t.Errorf("principal = %q", got)
	}
	if got := r.must(t, 0, dav.DisplayName).Text; got != "Jürgen Müller" {
		t.Errorf("displayname = %q", got)
	}
}

func TestPropfindStoreFailure(t *testing.T) {
	f := newFixture(t, "a.vcf")
	f.h.store = failListStore{f.st}
	rec := f.propfind(t, "/dav/addressbooks/alice/contacts/", "1", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "disk") {
		t.Errorf("leaked error detail: %s", rec.Body)
	}
}

type failListStore struct{ store.Store }

func (failListStore) ListObjects(context.Context, store.CollectionRef, store.ListOptions) ([]store.Object, error) {
	return nil, errors.New("disk on fire")
}

func TestPropfindBookWithoutTrailingSlash(t *testing.T) {
	f := newFixture(t, "a.vcf")
	r := parseReply(t, f.propfind(t, "/dav/addressbooks/alice/contacts", "1", propBody(`<D:getetag/>`)))
	if strings.Join(r.hrefs, " ") != "/dav/addressbooks/alice/contacts/ /dav/addressbooks/alice/contacts/a.vcf" {
		t.Errorf("hrefs = %v", r.hrefs)
	}
}
