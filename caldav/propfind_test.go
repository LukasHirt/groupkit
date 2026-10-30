// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

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

func icsFor(name string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + name +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\nSUMMARY:" + name + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
}

type fixture struct {
	h        *Handler
	st       *countingStore
	mem      store.Store
	calendar store.Collection
	owner    string
}

// newFixture makes owner "alice" with the calendar "work" holding the named objects.
func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	mem := memory.New()
	st := &countingStore{Store: mem}
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	_, err := mem.CreateCollection(context.Background(), ref, store.Props{DisplayName: "Work Calendar", Description: "Team calendar"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := mem.PutObject(context.Background(), ref, n, icsFor(n), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	calendar, _ := mem.GetCollection(context.Background(), ref)
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{h: h, st: st, mem: mem, calendar: calendar, owner: "alice"}
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
	return `<?xml version="1.0"?><D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="http://apple.com/ns/ical/" xmlns:CS="http://calendarserver.org/ns/"><D:prop>` + inner + `</D:prop></D:propfind>`
}

func TestPropfindHome(t *testing.T) {
	f := newFixture(t, "a.ics")
	body := propBody(`<D:resourcetype/><D:displayname/><D:current-user-principal/><D:owner/><D:current-user-privilege-set/><D:supported-report-set/>`)

	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/calendars/alice/" {
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

	// Depth 1 lists the calendars.
	r = parseReply(t, f.propfind(t, "/dav/calendars/alice/", "1", body))
	if strings.Join(r.hrefs, " ") != "/dav/calendars/alice/ /dav/calendars/alice/work/" {
		t.Fatalf("depth 1 hrefs = %v", r.hrefs)
	}
	if got := names([]*dav.Node{r.must(t, 1, dav.ResourceType)}); strings.Join(got, ",") != "collection,calendar" {
		t.Errorf("calendar resourcetype = %v", got)
	}
}

func TestPropfindCalendarDepth0(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics")
	body := propBody(`<D:resourcetype/><D:displayname/><CS:getctag/><D:sync-token/><C:calendar-description/>
		<C:supported-calendar-data/><C:max-resource-size/><D:supported-report-set/><D:current-user-privilege-set/>`)
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/calendars/alice/work/" {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if got := r.must(t, 0, dav.DisplayName).Text; got != "Work Calendar" {
		t.Errorf("displayname = %q", got)
	}
	rev := fmt.Sprint(f.calendar.Revision)
	if got := r.must(t, 0, dav.GetCTag).Text; got != rev || rev == "0" {
		t.Errorf("getctag = %q, want %q", got, rev)
	}
	if got, want := r.must(t, 0, dav.SyncToken).Text, "urn:groupkit:sync:"+f.calendar.ID+":"+rev; got != want {
		t.Errorf("sync-token = %q, want %q", got, want)
	}
	if got := r.must(t, 0, dav.CalendarDescription).Text; got != "Team calendar" {
		t.Errorf("description = %q", got)
	}
	sad := r.must(t, 0, dav.SupportedCalendarData)
	var versions []string
	for _, c := range sad.Kids(dav.CalendarData) {
		versions = append(versions, c.Attr("content-type")+"/"+c.Attr("version"))
	}
	if strings.Join(versions, ",") != "text/calendar/2.0" {
		t.Errorf("supported-calendar-data = %v", versions)
	}
	if got := r.must(t, 0, dav.CalMaxResourceSize).Text; got != "1048576" {
		t.Errorf("max-resource-size = %q", got)
	}
	var reports []string
	for _, sr := range r.must(t, 0, dav.SupportedReportSet).Kids(dav.SupportedReport) {
		reports = append(reports, sr.Child(dav.Report).Children[0].Name.Local)
	}
	if strings.Join(reports, ",") != "calendar-query,calendar-multiget,sync-collection" {
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

func TestPropfindCalendarDisplayNameFallbackAndEmptyDescription(t *testing.T) {
	f := newFixture(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "plain"}
	if _, err := f.mem.CreateCollection(context.Background(), ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/plain/", "0", propBody(`<D:displayname/><C:calendar-description/>`)))
	if got := r.must(t, 0, dav.DisplayName).Text; got != "plain" {
		t.Errorf("displayname = %q", got)
	}
	if got := r.must(t, 0, dav.CalendarDescription).Text; got != "" {
		t.Errorf("description = %q", got)
	}
}

func TestPropfindCalendarDepth1Objects(t *testing.T) {
	f := newFixture(t, "c b.ics", "a.ics", "ü.ics")
	body := propBody(`<D:getetag/><D:getcontenttype/><D:getcontentlength/><D:getlastmodified/><D:displayname/><D:resourcetype/><D:creationdate/>`)
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "1", body))
	want := []string{
		"/dav/calendars/alice/work/",
		"/dav/calendars/alice/work/a.ics",
		"/dav/calendars/alice/work/c%20b.ics",
		"/dav/calendars/alice/work/%C3%BC.ics",
	}
	if strings.Join(r.hrefs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("hrefs = %v\nwant %v", r.hrefs, want)
	}
	for i, name := range []string{"", "a.ics", "c b.ics", "ü.ics"} {
		if i == 0 {
			continue
		}
		data := icsFor(name)
		if got, want := r.must(t, i, dav.GetETag).Text, `"`+store.ETag(data)+`"`; got != want {
			t.Errorf("%s etag = %q, want %q", name, got, want)
		}
		if got := r.must(t, i, dav.GetContentType).Text; got != "text/calendar; charset=utf-8" {
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
	f := newFixture(t, "a.ics")
	body := propBody(`<D:getetag/><C:calendar-data/><D:owner/><D:current-user-privilege-set/><D:supported-report-set/>`)
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/a.ics", "0", body))
	if len(r.hrefs) != 1 || r.hrefs[0] != "/dav/calendars/alice/work/a.ics" {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if got := r.must(t, 0, dav.CalendarData).Text; got != string(icsFor("a.ics")) {
		t.Errorf("calendar-data = %q", got)
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

	rec := f.propfind(t, "/dav/calendars/alice/work/nope.ics", "0", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing object status = %d", rec.Code)
	}
}

func TestPropfindAllpropOmitsCalendarData(t *testing.T) {
	f := newFixture(t, "a.ics")
	for _, body := range []string{"", `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`} {
		r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "1", body))
		if len(r.hrefs) != 2 {
			t.Fatalf("hrefs = %v", r.hrefs)
		}
		if r.find(t, 1, 200, dav.GetETag) == nil {
			t.Errorf("allprop lacks getetag")
		}
		for i := range r.hrefs {
			for _, n := range r.props[i][200] {
				if n.Name == dav.CalendarData {
					t.Errorf("allprop returned calendar-data")
				}
			}
		}
	}
	if f.st.gets.Load() != 0 {
		t.Errorf("GetObject called %d times", f.st.gets.Load())
	}
}

func TestPropfindPropname(t *testing.T) {
	f := newFixture(t, "a.ics")
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "1",
		`<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`))
	has := func(i int, n xml.Name) bool { return r.find(t, i, 200, n) != nil }
	if !has(0, dav.GetCTag) || !has(0, dav.SyncToken) {
		t.Errorf("calendar propname incomplete: %v", r.props[0])
	}
	if !has(1, dav.GetETag) || !has(1, dav.CalendarData) {
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
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "0",
		propBody(`<D:displayname/><X:nope xmlns:X="urn:x"/>`)))
	if r.must(t, 0, dav.DisplayName).Text != "Work Calendar" {
		t.Errorf("displayname missing")
	}
	n := r.find(t, 0, 404, xml.Name{Space: "urn:x", Local: "nope"})
	if n == nil {
		t.Errorf("unknown property not in 404: %v", r.props[0])
	}
}

func TestPropfindCalendarDataExactBytes(t *testing.T) {
	f := newFixture(t, "a.ics", "b.ics", "c.ics")
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "1", propBody(`<D:getetag/><C:calendar-data/>`)))
	for i, name := range []string{"", "a.ics", "b.ics", "c.ics"} {
		if i == 0 {
			if r.find(t, 0, 404, dav.CalendarData) == nil {
				t.Errorf("calendar calendar-data should be 404")
			}
			continue
		}
		if got := r.must(t, i, dav.CalendarData).Text; got != string(icsFor(name)) {
			t.Errorf("%s calendar-data = %q", name, got)
		}
	}
	if got := f.st.gets.Load(); got != 3 {
		t.Errorf("GetObject calls = %d, want 3", got)
	}
}

func TestPropfindManyObjectsReadsNoData(t *testing.T) {
	f := newFixture(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	for i := 0; i < 5000; i++ {
		if _, err := f.mem.PutObject(context.Background(), ref, fmt.Sprintf("c%05d.ics", i), icsFor("x"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.propfind(t, "/dav/calendars/alice/work/", "1", propBody(`<D:getetag/>`))
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
	f := newFixture(t, "a.ics", "b.ics", "c.ics")
	f.st.fail = map[string]bool{"b.ics": true}
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "1", propBody(`<D:getetag/><C:calendar-data/>`)))
	if len(r.hrefs) != 4 {
		t.Fatalf("hrefs = %v", r.hrefs)
	}
	if r.find(t, 2, 500, dav.CalendarData) == nil {
		t.Errorf("b.ics calendar-data should be 500: %v", r.props[2])
	}
	if r.must(t, 2, dav.GetETag).Text == "" {
		t.Errorf("b.ics etag missing")
	}
	for _, i := range []int{1, 3} {
		if r.find(t, i, 200, dav.CalendarData) == nil {
			t.Errorf("response %d lost calendar-data", i)
		}
	}
}

func TestPropfindMissingCalendar(t *testing.T) {
	f := newFixture(t)
	for _, depth := range []string{"0", "1"} {
		if rec := f.propfind(t, "/dav/calendars/alice/nope/", depth, ""); rec.Code != http.StatusNotFound {
			t.Errorf("Depth %s status = %d", depth, rec.Code)
		}
	}
	if rec := f.propfind(t, "/dav/calendars/alice/nope/x.ics", "0", ""); rec.Code != http.StatusNotFound {
		t.Errorf("object in missing calendar status = %d", rec.Code)
	}
}

func TestPropfindDepth(t *testing.T) {
	f := newFixture(t, "a.ics")
	for _, d := range []string{"infinity", "Infinity", "", "1"} {
		r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", d, ""))
		if len(r.hrefs) != 2 {
			t.Errorf("Depth %q gave %d responses", d, len(r.hrefs))
		}
	}
	if rec := f.propfind(t, "/dav/calendars/alice/work/", "2", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("Depth 2 status = %d", rec.Code)
	}
	if rec := f.propfind(t, "/dav/calendars/alice/work/", "0", "<nonsense"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad XML status = %d", rec.Code)
	}
	big := strings.Repeat(" ", dav.MaxXMLBytes+10)
	if rec := f.propfind(t, "/dav/calendars/alice/work/", "0", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big body status = %d", rec.Code)
	}
}

func TestPropfindUnicodeUserPrincipalHref(t *testing.T) {
	mem := memory.New()
	h, err := New(Config{Store: mem, Prefix: "/dav", Resolve: resolveAs("owner-1")})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PROPFIND", "/dav/calendars/J%C3%BCrgen%20M%C3%BCller/", bytes.NewReader([]byte(propBody(`<D:current-user-principal/><D:owner/><D:displayname/>`))))
	req.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := parseReply(t, rec)
	if r.hrefs[0] != "/dav/calendars/J%C3%BCrgen%20M%C3%BCller/" {
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
	f := newFixture(t, "a.ics")
	f.h.store = failListStore{f.st}
	rec := f.propfind(t, "/dav/calendars/alice/work/", "1", "")
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

func TestPropfindCalendarWithoutTrailingSlash(t *testing.T) {
	f := newFixture(t, "a.ics")
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work", "1", propBody(`<D:getetag/>`)))
	if strings.Join(r.hrefs, " ") != "/dav/calendars/alice/work/ /dav/calendars/alice/work/a.ics" {
		t.Errorf("hrefs = %v", r.hrefs)
	}
}

const fullCalendarProps = `<D:resourcetype/><D:displayname/><CS:getctag/><D:sync-token/><C:calendar-description/>
	<A:calendar-color/><A:calendar-order/><C:calendar-timezone/><C:supported-calendar-component-set/>
	<C:supported-calendar-data/><C:max-resource-size/><D:current-user-principal/><D:owner/>
	<D:current-user-privilege-set/><D:supported-report-set/>`

const vtimezone = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\nEND:VTIMEZONE\r\nEND:VCALENDAR\r\n"

func (f *fixture) makeCalendar(t *testing.T, name string, p store.Props) {
	t.Helper()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: name}
	if _, err := f.mem.CreateCollection(context.Background(), ref, p); err != nil {
		t.Fatal(err)
	}
}

func TestPropfindCalendarEveryProperty(t *testing.T) {
	f := newFixture(t)
	f.makeCalendar(t, "full", store.Props{
		DisplayName: "Full", Description: "Everything", Color: "#ff0000ff", Order: 7,
		Timezone: vtimezone, Components: []string{"VEVENT", "VTODO"},
	})
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/full/", "0", propBody(fullCalendarProps)))
	if got := names([]*dav.Node{r.must(t, 0, dav.ResourceType)}); strings.Join(got, ",") != "collection,calendar" {
		t.Errorf("resourcetype = %v", got)
	}
	if got := r.must(t, 0, dav.ResourceType).Children[1].Name; got != dav.Calendar {
		t.Errorf("calendar resourcetype name = %v", got)
	}
	for name, want := range map[xml.Name]string{
		dav.DisplayName:         "Full",
		dav.CalendarDescription: "Everything",
		dav.CalendarColor:       "#ff0000ff",
		dav.CalendarOrder:       "7",
		dav.CalendarTimezone:    vtimezone,
		dav.CalMaxResourceSize:  "1048576",
	} {
		if got := r.must(t, 0, name).Text; got != want {
			t.Errorf("%v = %q, want %q", name, got, want)
		}
	}
	var comps []string
	for _, c := range r.must(t, 0, dav.SupportedCalendarComponentSet).Kids(dav.CalComp) {
		comps = append(comps, c.Attr("name"))
	}
	// The calendar stores VEVENT and VTODO; only VEVENT is served.
	if strings.Join(comps, ",") != "VEVENT" {
		t.Errorf("components = %v", comps)
	}
	for _, n := range []xml.Name{dav.CurrentUserPrincipal, dav.Owner} {
		if got := r.must(t, 0, n).Child(dav.Href).Text; got != "/dav/principals/alice/" {
			t.Errorf("%v = %q", n, got)
		}
	}
	if r.must(t, 0, dav.GetCTag).Text == "" || r.must(t, 0, dav.SyncToken).Text == "" {
		t.Errorf("ctag or sync-token empty")
	}
	if len(r.props[0][404]) != 0 || len(r.props[0][500]) != 0 {
		t.Errorf("unexpected failures: %v", r.props[0])
	}
}

func TestPropfindCalendarDefaultComponentSet(t *testing.T) {
	f := newFixture(t)
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/work/", "0", propBody(`<C:supported-calendar-component-set/>`)))
	var comps []string
	for _, c := range r.must(t, 0, dav.SupportedCalendarComponentSet).Kids(dav.CalComp) {
		comps = append(comps, c.Attr("name"))
	}
	if strings.Join(comps, ",") != "VEVENT" {
		t.Errorf("components = %v, want VEVENT", comps)
	}
}

func TestPropfindUnsetOptionalProperties(t *testing.T) {
	f := newFixture(t)
	f.makeCalendar(t, "plain", store.Props{})
	ask := propBody(`<C:calendar-description/><A:calendar-color/><A:calendar-order/><C:calendar-timezone/>`)
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/plain/", "0", ask))
	if n := r.find(t, 0, 200, dav.CalendarDescription); n == nil || n.Text != "" || len(n.Children) != 0 {
		t.Errorf("unset description should be an empty 200 element: %v", r.props[0])
	}
	for _, n := range []xml.Name{dav.CalendarColor, dav.CalendarTimezone} {
		if r.find(t, 0, 404, n) == nil {
			t.Errorf("unset %v should be 404: %v", n, r.props[0])
		}
	}
	// The order cannot be told apart from 0 in the store, so asking for it
	// always answers; allprop below still leaves it out.
	if n := r.find(t, 0, 200, dav.CalendarOrder); n == nil || n.Text != "0" {
		t.Errorf("order of a plain calendar should be 200 with 0: %v", r.props[0])
	}

	// allprop and propname list only what is set.
	for _, body := range []string{"", `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`} {
		r = parseReply(t, f.propfind(t, "/dav/calendars/alice/plain/", "0", body))
		for _, n := range []xml.Name{dav.CalendarDescription, dav.CalendarColor, dav.CalendarOrder, dav.CalendarTimezone} {
			if r.find(t, 0, 200, n) != nil {
				t.Errorf("allprop returned unset %v", n)
			}
		}
	}
}

// A stored order of 0 reads back as 0 when asked for, as the store cannot
// say whether it was ever set.
func TestPropfindOrderZeroReadsBack(t *testing.T) {
	f := newFixture(t)
	f.makeCalendar(t, "zero", store.Props{Order: 0, Color: "#000000"})
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/zero/", "0", propBody(`<A:calendar-order/>`)))
	if n := r.find(t, 0, 200, dav.CalendarOrder); n == nil || n.Text != "0" {
		t.Errorf("order = %v", r.props[0])
	}
	if len(r.props[0][404]) != 0 {
		t.Errorf("404 = %v", r.props[0][404])
	}
	// Negative values are plain integers too.
	f.makeCalendar(t, "neg", store.Props{Order: -5})
	r = parseReply(t, f.propfind(t, "/dav/calendars/alice/neg/", "0", propBody(`<A:calendar-order/>`)))
	if n := r.find(t, 0, 200, dav.CalendarOrder); n == nil || n.Text != "-5" {
		t.Errorf("order = %v", r.props[0])
	}
}

func TestPropfindSetOptionalPropertiesAppearInAllprop(t *testing.T) {
	f := newFixture(t)
	f.makeCalendar(t, "full", store.Props{Description: "d", Color: "#00ff00ff", Order: 3, Timezone: vtimezone})
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/full/", "0", ""))
	for _, n := range []xml.Name{dav.CalendarDescription, dav.CalendarColor, dav.CalendarOrder, dav.CalendarTimezone} {
		if r.find(t, 0, 200, n) == nil {
			t.Errorf("allprop lacks %v", n)
		}
	}
}

func TestPropfindCalendarTimezoneIsXMLEscaped(t *testing.T) {
	f := newFixture(t)
	f.makeCalendar(t, "tz", store.Props{Timezone: "BEGIN:VTIMEZONE\r\nTZID:A<&>B\r\nEND:VTIMEZONE\r\n"})
	r := parseReply(t, f.propfind(t, "/dav/calendars/alice/tz/", "0", propBody(`<C:calendar-timezone/>`)))
	if got := r.must(t, 0, dav.CalendarTimezone).Text; !strings.Contains(got, "A<&>B") {
		t.Errorf("timezone = %q", got)
	}
}
