// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav/carddav"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

var aliceContacts = store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}

const contactsHref = "/dav/addressbooks/alice/contacts/"

// rawDo sends one request to ts as user with a timeout and returns the
// status and body.
func rawDo(t *testing.T, ts *httptest.Server, method, path, user string, hdr http.Header, body io.Reader) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, ts.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range hdr {
		req.Header[k] = vs
	}
	if user != "" {
		req.SetBasicAuth(user, basicUsers[user])
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %.80s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %.80s: reading body: %v", method, path, err)
	}
	return resp.StatusCode, string(b)
}

// queryNames runs an addressbook-query for FN containing text and returns
// the matching paths.
func queryNames(t *testing.T, ctx context.Context, c *carddav.Client, text string) []string {
	t.Helper()
	found, err := c.QueryAddressBook(ctx, contactsHref, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{Props: []string{vcard.FieldFormattedName}},
		PropFilters: []carddav.PropFilter{{
			Name:        vcard.FieldFormattedName,
			TextMatches: []carddav.TextMatch{{Text: text}},
		}},
	})
	if err != nil {
		t.Fatalf("QueryAddressBook: %v", err)
	}
	return objPaths(found)
}

func TestSurvivesRestart(t *testing.T) {
	ctx := ctxTimeout(t)
	dir := t.TempDir()

	st := openFileStore(t, dir)
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, st)
	c := cardClient(t, ts.URL+"/dav/", "alice")
	for _, n := range []string{"anna", "anton", "bert"} {
		if _, err := c.PutAddressObject(ctx, contactsHref+n+".vcf", testCard("uid-"+n, "Name "+n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.RemoveAll(ctx, contactsHref+"bert.vcf"); err != nil {
		t.Fatal(err)
	}
	before := queryNames(t, ctx, c, "an")
	sync1, err := c.SyncCollection(ctx, contactsHref, &carddav.SyncQuery{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := c.GetAddressObject(ctx, contactsHref+"anna.vcf")
	if err != nil {
		t.Fatal(err)
	}
	ts.Close()
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	st = openFileStore(t, dir)
	t.Cleanup(func() { _ = st.Close() })
	ts = startBasicServer(t, st)
	c = cardClient(t, ts.URL+"/dav/", "alice")

	if after := queryNames(t, ctx, c, "an"); !slices.Equal(after, before) || len(after) != 2 {
		t.Errorf("query after restart = %v, before %v", after, before)
	}
	got, err := c.GetAddressObject(ctx, contactsHref+"anna.vcf")
	if err != nil {
		t.Fatal(err)
	}
	if got.ETag != old.ETag || got.Card.PreferredValue(vcard.FieldFormattedName) != "Name anna" {
		t.Errorf("anna after restart: etag %q (was %q) fn %q", got.ETag, old.ETag, got.Card.PreferredValue(vcard.FieldFormattedName))
	}

	// The old token still works and reports nothing new.
	same, err := c.SyncCollection(ctx, contactsHref, &carddav.SyncQuery{SyncToken: sync1.SyncToken})
	if err != nil {
		t.Fatalf("sync with the old token: %v", err)
	}
	if len(same.Updated)+len(same.Deleted) != 0 || same.SyncToken != sync1.SyncToken {
		t.Errorf("sync after restart = %+v, token before %q", same, sync1.SyncToken)
	}
	// Changes after the restart are all it reports.
	if _, err := c.PutAddressObject(ctx, contactsHref+"carl.vcf", testCard("uid-carl", "Name carl")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, contactsHref+"anton.vcf"); err != nil {
		t.Fatal(err)
	}
	next, err := c.SyncCollection(ctx, contactsHref, &carddav.SyncQuery{SyncToken: sync1.SyncToken})
	if err != nil {
		t.Fatalf("incremental sync after restart: %v", err)
	}
	if got := objPaths(next.Updated); !slices.Equal(got, []string{contactsHref + "carl.vcf"}) {
		t.Errorf("updated after restart = %v", got)
	}
	if !slices.Equal(next.Deleted, []string{contactsHref + "anton.vcf"}) {
		t.Errorf("deleted after restart = %v", next.Deleted)
	}
}

// badObjects are written straight to the store, bypassing validation.
var badObjects = map[string][]byte{
	"garbage.vcf":   {0x00, 0xff, 0xfe, 'x', 0x01, '\n', 0x80},
	"empty.vcf":     {},
	"truncated.vcf": []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Half"),
	"old.vcf":       []byte("BEGIN:VCARD\r\nVERSION:2.1\r\nFN:Old Timer\r\nEND:VCARD\r\n"),
	"two.vcf": []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One Two\r\nN:;;;;\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:One Two\r\nN:;;;;\r\nEND:VCARD\r\n"),
	"calendar.vcf": []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"),
}

func TestOneBadObjectDoesNotBreakAnything(t *testing.T) {
	ctx := ctxTimeout(t)
	st := openFileStore(t, t.TempDir())
	t.Cleanup(func() { _ = st.Close() })
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		t.Fatal(err)
	}
	ts := startBasicServer(t, st)
	c := cardClient(t, ts.URL+"/dav/", "alice")

	good := []string{contactsHref + "one.vcf", contactsHref + "two-good.vcf"}
	for i, p := range good {
		if _, err := c.PutAddressObject(ctx, p, testCard("uid-good-"+string(rune('a'+i)), "One Good")); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range badObjects {
		if _, err := st.PutObject(ctx, aliceContacts, name, data, store.PutOptions{}); err != nil {
			t.Fatalf("PutObject %s: %v", name, err)
		}
	}

	// Listing: every object, good or bad, with a 207.
	code, body := rawDo(t, ts, "PROPFIND", contactsHref, "alice", http.Header{"Depth": {"1"}}, strings.NewReader(propfindBody))
	if code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND = %d %s", code, body)
	}
	for _, p := range good {
		if !strings.Contains(body, p) {
			t.Errorf("PROPFIND lacks %s", p)
		}
	}

	// Query: the good ones.
	if got := queryNames(t, ctx, c, "one"); !slices.Equal(got, good) {
		t.Errorf("query = %v, want %v", got, good)
	}
	// A query that matches everything by negation: still only parseable ones.
	all, err := c.QueryAddressBook(ctx, contactsHref, &carddav.AddressBookQuery{
		DataRequest: carddav.AddressDataRequest{AllProp: true},
		PropFilters: []carddav.PropFilter{{
			Name:        vcard.FieldFormattedName,
			TextMatches: []carddav.TextMatch{{Text: "zzz", NegateCondition: true}},
		}},
	})
	if err != nil {
		t.Fatalf("negated query: %v", err)
	}
	if got := objPaths(all); !slices.Equal(got, good) {
		t.Errorf("negated query = %v", got)
	}

	// Multiget of the good ones next to a bad one does not fail.
	code, body = rawDo(t, ts, "REPORT", contactsHref, "alice", http.Header{"Depth": {"1"}}, strings.NewReader(
		`<C:addressbook-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`+
			`<D:prop><D:getetag/><C:address-data/></D:prop>`+
			`<D:href>`+good[0]+`</D:href><D:href>`+contactsHref+`garbage.vcf</D:href></C:addressbook-multiget>`))
	if code != http.StatusMultiStatus || !strings.Contains(body, "Good") {
		t.Errorf("multiget = %d %s", code, body)
	}

	// Sync, with and without data: the good ones are there.
	for _, req := range []carddav.AddressDataRequest{{}, {AllProp: true}} {
		res, err := c.SyncCollection(ctx, contactsHref, &carddav.SyncQuery{DataRequest: req})
		if err != nil {
			t.Fatalf("sync %+v: %v", req, err)
		}
		got := objPaths(res.Updated)
		for _, p := range good {
			if !slices.Contains(got, p) {
				t.Errorf("sync %+v lacks %s: %v", req, p, got)
			}
		}
	}

	// GET of a bad object returns the stored bytes; it is not a server error.
	code, body = rawDo(t, ts, "GET", contactsHref+"garbage.vcf", "alice", nil, nil)
	if code != http.StatusOK || body != string(badObjects["garbage.vcf"]) {
		t.Errorf("GET garbage = %d %q", code, body)
	}
}

// panicLog collects the http.Server error log to catch recovered panics.
type panicLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (p *panicLog) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Write(b)
}

func (p *panicLog) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

func TestHostileRequests(t *testing.T) {
	st := memory.New()
	if err := groupkit.EnsureDefaultCollections(t.Context(), st, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutObject(t.Context(), aliceContacts, "a.vcf", []byte(contactA), store.PutOptions{UID: "a-1"}); err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, basicConfig(st))
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	var errLog panicLog
	ts := httptest.NewUnstartedServer(mux)
	ts.Config.ErrorLog = log.New(&errLog, "", 0)
	ts.Start()
	defer ts.Close()

	ok := func() {
		t.Helper()
		if code, body := rawDo(t, ts, "PROPFIND", "/dav/", "alice", http.Header{"Depth": {"0"}}, strings.NewReader(propfindBody)); code != http.StatusMultiStatus {
			t.Fatalf("server no longer serves: %d %s", code, body)
		}
	}
	quick := func(name string, start time.Time) {
		t.Helper()
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s took %v", name, d)
		}
	}

	// 100000 levels in 700 KB: under the body limit, so the nesting limit
	// has to catch it.
	deep := strings.Repeat("<p>", 100000) + strings.Repeat("</p>", 100000)
	deepBody := `<D:propfind xmlns:D="DAV:">` + deep + `</D:propfind>`
	big := bytes.Repeat([]byte("<D:prop/>"), 2<<20/9+1)
	bigBody := append(append([]byte(`<D:propfind xmlns:D="DAV:">`), big...), []byte(`</D:propfind>`)...)
	ifMatch := make([]string, 200)
	for i := range ifMatch {
		ifMatch[i] = `"etag-` + strings.Repeat("x", i) + `"`
	}
	longName := strings.Repeat("n", 10<<10) + ".vcf"

	cases := []struct {
		name   string
		method string
		path   string
		hdr    http.Header
		body   []byte
		want   int
	}{
		{"deep XML propfind", "PROPFIND", contactsHref, http.Header{"Depth": {"1"}}, []byte(deepBody), 400},
		{"deep XML report", "REPORT", contactsHref, http.Header{"Depth": {"1"}}, []byte(
			`<C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">` + deep + `</C:addressbook-query>`), 400},
		{"deep XML on the root", "PROPFIND", "/dav/", nil, []byte(deepBody), 400},
		{"2 MiB propfind", "PROPFIND", contactsHref, http.Header{"Depth": {"1"}}, bigBody, 413},
		{"2 MiB report", "REPORT", contactsHref, nil, bigBody, 413},
		{"2 MiB proppatch", "PROPPATCH", contactsHref, nil, bigBody, 413},
		{"2 MiB mkcol", "MKCOL", "/dav/addressbooks/alice/x/", nil, bigBody, 413},
		{"200 If-Match", "PUT", contactsHref + "a.vcf", http.Header{"If-Match": ifMatch}, []byte(contactA), 412},
		{"200 If-Match delete", "DELETE", contactsHref + "a.vcf", http.Header{"If-Match": ifMatch}, nil, 412},
		{"10 KiB name put", "PUT", contactsHref + longName, nil, []byte(contactA), 400},
		{"10 KiB name get", "GET", contactsHref + longName, nil, nil, 400},
		{"10 KiB collection", "MKCOL", "/dav/addressbooks/alice/" + longName + "/", nil, nil, 400},
		{"Depth -1", "PROPFIND", contactsHref, http.Header{"Depth": {"-1"}}, []byte(propfindBody), 400},
		{"Depth -1 on the root", "PROPFIND", "/dav/", http.Header{"Depth": {"-1"}}, []byte(propfindBody), 400},
	}
	for _, tc := range cases {
		start := time.Now()
		code, body := rawDo(t, ts, tc.method, tc.path, "alice", tc.hdr, bytes.NewReader(tc.body))
		if code != tc.want {
			t.Errorf("%s: status %d, want %d; body %.200s", tc.name, code, tc.want, body)
		}
		quick(tc.name, start)
		ok()
	}

	// Request-line oddities, sent as written over a raw connection.
	addr := ts.Listener.Addr().String()
	auth := "Authorization: Basic YWxpY2U6YWxpY2UtcHc=\r\n" // alice:alice-pw
	raw := []struct {
		name string
		line string
		want int
	}{
		{"64 KiB URL", "GET " + contactsHref + strings.Repeat("u", 64<<10) + " HTTP/1.1", 400},
		{"NUL in path", "GET " + contactsHref + "a\x00b.vcf HTTP/1.1", 400},
		{"escaped NUL in path", "GET " + contactsHref + "a%00b.vcf HTTP/1.1", 400},
		{"invalid escape", "GET " + contactsHref + "%zz.vcf HTTP/1.1", 400},
		{"truncated escape", "PROPFIND /dav/addressbooks/alice/%4 HTTP/1.1", 400},
		{"OPTIONS *", "OPTIONS * HTTP/1.1", 200},
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

// rawLine writes req to addr and returns the response status.
func rawLine(t *testing.T, addr, req string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading response to %.60q: %v", req, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// FuzzServerRequests sends arbitrary requests with a Depth header, as an
// authenticated user, to a server whose address book also holds objects
// that bypassed validation. It must never panic or answer 5xx.
func FuzzServerRequests(f *testing.F) {
	st := memory.New()
	ctx := context.Background()
	if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
		f.Fatal(err)
	}
	for name, data := range map[string]string{
		"a.vcf":       contactA,
		"b.vcf":       strings.Replace(strings.Replace(contactA, "a-1", "b-1", 1), "Ada", "Bob", 2),
		"bad.vcf":     "\x00\xffnot a card",
		"partial.vcf": "BEGIN:VCARD\r\nVERSION:4.0\r\n",
	} {
		if _, err := st.PutObject(ctx, aliceContacts, name, []byte(data), store.PutOptions{}); err != nil {
			f.Fatal(err)
		}
	}
	srv, err := groupkit.NewServer(basicConfig(st))
	if err != nil {
		f.Fatal(err)
	}
	h := srv.Handler()

	const cr = `xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav"`
	f.Add("PROPFIND", contactsHref, "1", propfindBody)
	f.Add("PROPFIND", "/dav/addressbooks/alice/", "infinity", `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`)
	f.Add("REPORT", contactsHref, "1", `<C:addressbook-query `+cr+`><D:prop><C:address-data><C:prop name="FN"/></C:address-data></D:prop>`+
		`<C:filter test="allof"><C:prop-filter name="FN"><C:text-match collation="i;octet" match-type="ends-with">e</C:text-match>`+
		`<C:param-filter name="TYPE"><C:is-not-defined/></C:param-filter></C:prop-filter></C:filter><C:limit><C:nresults>1</C:nresults></C:limit></C:addressbook-query>`)
	f.Add("REPORT", contactsHref, "1", `<C:addressbook-multiget `+cr+`><D:prop><D:getetag/><C:address-data/></D:prop>`+
		`<D:href>`+contactsHref+`a.vcf</D:href><D:href>`+contactsHref+`bad.vcf</D:href><D:href>/elsewhere</D:href></C:addressbook-multiget>`)
	f.Add("REPORT", contactsHref, "0", `<D:sync-collection xmlns:D="DAV:"><D:sync-token>urn:groupkit:sync:x:1</D:sync-token>`+
		`<D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`)
	f.Add("PROPPATCH", contactsHref, "", `<D:propertyupdate `+cr+`><D:set><D:prop><D:displayname>X</D:displayname>`+
		`<C:addressbook-description>d</C:addressbook-description></D:prop></D:set><D:remove><D:prop><D:getetag/></D:prop></D:remove></D:propertyupdate>`)
	f.Add("MKCOL", "/dav/addressbooks/alice/new/", "", `<D:mkcol `+cr+`><D:set><D:prop><D:resourcetype><D:collection/><C:addressbook/></D:resourcetype></D:prop></D:set></D:mkcol>`)
	f.Add("PUT", contactsHref+"c.vcf", "", "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:a-1\r\nFN:C\r\nEND:VCARD\r\n")
	f.Add("DELETE", contactsHref+"bad.vcf", "", "")
	f.Add("GET", contactsHref+"partial.vcf", "", "")
	f.Add("PROPFIND", "/dav/principals/alice/", "0", "")
	f.Add("MOVE", contactsHref+"a.vcf", "", "")

	f.Fuzz(func(t *testing.T, method, path, depth, body string) {
		if !validMethod(method) || !strings.HasPrefix(path, "/") {
			t.Skip()
		}
		r, err := http.NewRequest(method, "http://example.test/", strings.NewReader(body))
		if err != nil {
			t.Skip()
		}
		u, err := r.URL.Parse(path)
		if err != nil {
			t.Skip()
		}
		r.URL = u
		r.SetBasicAuth("alice", basicUsers["alice"])
		if depth != "" {
			r.Header.Set("Depth", depth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code >= 500 {
			t.Fatalf("%s %q Depth %q = %d: %s", method, path, depth, w.Code, w.Body)
		}
	})
}
