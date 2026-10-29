// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

func syncBody(token, level, props, extra string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><D:sync-collection ` + nsDecl + `>`)
	b.WriteString(`<D:sync-token>` + token + `</D:sync-token>`)
	if level != "" {
		b.WriteString(`<D:sync-level>` + level + `</D:sync-level>`)
	}
	if props != "" {
		b.WriteString(`<D:prop>` + props + `</D:prop>`)
	}
	b.WriteString(extra + `</D:sync-collection>`)
	return b.String()
}

func etagSync(token string) string { return syncBody(token, "1", `<D:getetag/>`, "") }

func (f *objFixture) sync(body string) *httptest.ResponseRecorder {
	return f.doPath("REPORT", objBase, []byte(body), nil)
}

// syncResult is a parsed sync-collection answer.
type syncResult struct {
	reply
	token string
	// status[i] is the status of response i when it is status-only, else 0.
	status []int
}

func parseSync(t *testing.T, rec *httptest.ResponseRecorder) syncResult {
	t.Helper()
	r := parseReply(t, rec)
	res := syncResult{reply: r}
	if tk := r.root.Child(dav.SyncToken); tk != nil {
		res.token = tk.Text
	}
	last := r.root.Children[len(r.root.Children)-1]
	if last.Name != dav.SyncToken {
		t.Errorf("sync-token is not last: %v", last.Name)
	}
	for _, resp := range r.root.Kids(dav.ResponseEl) {
		code := 0
		if st := resp.Child(dav.Status); st != nil {
			if _, err := fmt.Sscanf(st.Text, "HTTP/1.1 %d", &code); err != nil {
				t.Fatalf("status: %v", err)
			}
		}
		res.status = append(res.status, code)
	}
	return res
}

func (s syncResult) hrefNames() []string {
	var out []string
	for _, h := range s.hrefs {
		out = append(out, h[strings.LastIndex(h, "/")+1:])
	}
	return out
}

func putN(t *testing.T, f *objFixture, names ...string) {
	t.Helper()
	for _, n := range names {
		wantStatus2xx(t, f.do("PUT", n, card(n), vcType))
	}
}

// touchN rewrites the objects with new content.
func touchN(t *testing.T, f *objFixture, names ...string) {
	t.Helper()
	for _, n := range names {
		b := strings.Replace(string(card(n)), "FN:"+n, "FN:"+n+" v2", 1)
		wantStatus2xx(t, f.do("PUT", n, []byte(b), vcType))
	}
}

func wantStatus2xx(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code/100 != 2 {
		t.Fatalf("status = %d; body %q", rec.Code, rec.Body.String())
	}
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSyncInitialAndDeltas(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		putN(t, f, "a.vcf", "b.vcf", "c.vcf")

		s := parseSync(t, f.sync(etagSync("")))
		eq(t, s.hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf"})
		for i := range s.hrefs {
			if s.find(t, i, 200, dav.GetETag) == nil {
				t.Errorf("response %d lacks getetag", i)
			}
		}
		col, _ := st.GetCollection(context.Background(), objRef)
		id, rev, err := parseSyncToken(s.token)
		if err != nil || id != col.ID || rev != col.Revision {
			t.Fatalf("token %q: id=%q rev=%d err=%v; collection %q/%d", s.token, id, rev, err, col.ID, col.Revision)
		}

		// Unchanged: no responses, same token.
		s2 := parseSync(t, f.sync(etagSync(s.token)))
		if len(s2.hrefs) != 0 || s2.token != s.token {
			t.Errorf("unchanged: %v token %q, want none and %q", s2.hrefs, s2.token, s.token)
		}

		// A PUT shows only that object.
		wantStatus2xx(t, f.do("PUT", "b.vcf", []byte(strings.Replace(string(card("b.vcf")), "FN:b.vcf", "FN:Bee", 1)), vcType))
		s3 := parseSync(t, f.sync(etagSync(s.token)))
		eq(t, s3.hrefNames(), []string{"b.vcf"})
		if s3.token == s.token {
			t.Error("token did not advance")
		}

		// A DELETE is a status-only 404.
		wantStatus(t, f.do("DELETE", "a.vcf", nil, nil), http.StatusNoContent)
		s4 := parseSync(t, f.sync(etagSync(s3.token)))
		eq(t, s4.hrefNames(), []string{"a.vcf"})
		if s4.status[0] != 404 || len(s4.props[0]) != 0 {
			t.Errorf("deleted: status %v props %v", s4.status, s4.props[0])
		}
		if s4.hrefs[0] != objBase+"a.vcf" {
			t.Errorf("href = %q", s4.hrefs[0])
		}

		// Initial sync does not report tombstones.
		s5 := parseSync(t, f.sync(etagSync("")))
		eq(t, s5.hrefNames(), []string{"b.vcf", "c.vcf"})
	})
}

func TestSyncReplacedThenDeletedReportedOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		putN(t, f, "a.vcf")
		tok := parseSync(t, f.sync(etagSync(""))).token
		touchN(t, f, "a.vcf")
		wantStatus(t, f.do("DELETE", "a.vcf", nil, nil), http.StatusNoContent)
		s := parseSync(t, f.sync(etagSync(tok)))
		eq(t, s.hrefNames(), []string{"a.vcf"})
		if s.status[0] != 404 {
			t.Errorf("status = %v", s.status)
		}
	})
}

func (f *objFixture) wantInvalidToken(t *testing.T, body string) {
	t.Helper()
	rec := f.sync(body)
	wantPrecondition(t, rec, http.StatusForbidden, "valid-sync-token")
}

func TestSyncInvalidTokens(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		putN(t, f, "a.vcf")
		col, _ := st.GetCollection(context.Background(), objRef)
		tokens := map[string]string{
			"garbage":     "hello",
			"huge rev":    "urn:groupkit:sync:" + col.ID + ":99999999999999999999999",
			"future rev":  syncToken(store.Collection{ID: col.ID, Revision: col.Revision + 1}),
			"other id":    syncToken(store.Collection{ID: "other", Revision: 1}),
			"no revision": "urn:groupkit:sync:" + col.ID,
			"negative":    "urn:groupkit:sync:" + col.ID + ":-1",
			"too long":    "urn:groupkit:sync:" + strings.Repeat("x", 600) + ":1",
		}
		for name, tok := range tokens {
			t.Run(name, func(t *testing.T) { f.wantInvalidToken(t, etagSync(tok)) })
		}
	})
}

func TestSyncTokenAfterRecreate(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		putN(t, f, "a.vcf", "b.vcf")
		tok := parseSync(t, f.sync(etagSync(""))).token
		wantStatus(t, f.doPath("DELETE", objBase, nil, nil), http.StatusNoContent)
		wantStatus(t, f.doPath("MKCOL", objBase, nil, nil), http.StatusCreated)
		putN(t, f, "a.vcf", "b.vcf", "c.vcf")
		f.wantInvalidToken(t, etagSync(tok))
		// A fresh initial sync works.
		s := parseSync(t, f.sync(etagSync("")))
		eq(t, s.hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf"})
	})
}

func TestSyncLevelAndShape(t *testing.T) {
	f := memFixture(t)
	putN(t, f, "a.vcf")
	for _, lvl := range []string{"infinity", "2", "0", "x"} {
		wantStatus(t, f.sync(syncBody("", lvl, `<D:getetag/>`, "")), http.StatusBadRequest)
	}
	// Absent sync-level is level 1.
	s := parseSync(t, f.sync(syncBody("", "", `<D:getetag/>`, "")))
	eq(t, s.hrefNames(), []string{"a.vcf"})
	// A missing sync-token element is an initial sync.
	body := `<?xml version="1.0"?><D:sync-collection ` + nsDecl + `><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	eq(t, parseSync(t, f.sync(body)).hrefNames(), []string{"a.vcf"})
	// No prop is a bad request.
	wantStatus(t, f.sync(syncBody("", "1", "", "")), http.StatusBadRequest)
	// limit is ignored.
	lim := `<D:limit><D:nresults>1</D:nresults></D:limit>`
	putN(t, f, "b.vcf")
	eq(t, parseSync(t, f.sync(syncBody("", "1", `<D:getetag/>`, lim))).hrefNames(), []string{"a.vcf", "b.vcf"})
	// Missing book is 404.
	rec := f.doPath("REPORT", "/dav/addressbooks/alice/nope/", []byte(etagSync("")), nil)
	wantStatus(t, rec, http.StatusNotFound)
}

func TestSyncPropertySelection(t *testing.T) {
	cs := &countingStore{Store: memory.New()}
	f := newObjFixture(t, cs, 1<<20)
	putN(t, f, "a.vcf", "b.vcf", "c.vcf")
	cs.gets.Store(0) // PUT reads the current object for its preconditions
	tok := parseSync(t, f.sync(etagSync(""))).token
	if n := cs.gets.Load(); n != 0 {
		t.Fatalf("etag-only initial sync read %d objects", n)
	}
	touchN(t, f, "a.vcf", "c.vcf")
	wantStatus(t, f.do("DELETE", "b.vcf", nil, nil), http.StatusNoContent)

	cs.gets.Store(0)
	s := parseSync(t, f.sync(syncBody(tok, "1", `<D:getetag/><CR:address-data/>`, "")))
	eq(t, s.hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf"})
	if n := cs.gets.Load(); n != 2 {
		t.Errorf("GetObject calls = %d, want 2 (live changed objects only)", n)
	}
	if got := s.must(t, 0, dav.AddressData).Text; !strings.Contains(got, "FN:a.vcf v2") {
		t.Errorf("address-data = %q", got)
	}
	if s.status[1] != 404 {
		t.Errorf("deleted status = %v", s.status)
	}

	cs.gets.Store(0)
	parseSync(t, f.sync(syncBody(tok, "1", `<D:getetag/><D:getcontentlength/>`, "")))
	if n := cs.gets.Load(); n != 0 {
		t.Errorf("GetObject calls without address-data = %d", n)
	}
}

func TestSyncCTagEqualsTokenRevision(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		putN(t, f, "a.vcf", "b.vcf")
		wantStatus(t, f.do("DELETE", "a.vcf", nil, nil), http.StatusNoContent)
		s := parseSync(t, f.sync(etagSync("")))
		_, rev, err := parseSyncToken(s.token)
		if err != nil {
			t.Fatal(err)
		}
		if got := ctag(t, f); got != strconv.FormatUint(rev, 10) {
			t.Errorf("getctag = %q, token revision = %d", got, rev)
		}
	})
}

func TestSyncInvalidTokenBodyIsDAVError(t *testing.T) {
	f := memFixture(t)
	rec := f.sync(etagSync("junk"))
	n := wantPrecondition(t, rec, http.StatusForbidden, "valid-sync-token")
	if n.Name != dav.ValidSyncToken {
		t.Errorf("element = %v", n.Name)
	}
}

func TestSyncLargeBookReadsNoData(t *testing.T) {
	cs := &countingStore{Store: memory.New()}
	f := newObjFixture(t, cs, 1<<20)
	for i := range 5000 {
		n := fmt.Sprintf("c%04d.vcf", i)
		if _, err := cs.PutObject(context.Background(), objRef, n, card(n), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rec := f.sync(etagSync(""))
	wantStatus(t, rec, http.StatusMultiStatus)
	if n := strings.Count(rec.Body.String(), "<D:response>"); n != 5000 {
		t.Fatalf("got %d responses", n)
	}
	if cs.gets.Load() != 0 || cs.dataLists.Load() != 0 {
		t.Errorf("read data: gets=%d dataLists=%d", cs.gets.Load(), cs.dataLists.Load())
	}
}

func TestSyncOwnerIsolation(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	aliceRef := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := st.CreateCollection(ctx, aliceRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutObject(ctx, aliceRef, "secret.vcf", card("secret"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	// Whoever is in the URL, the request resolves to "bob", who has no book.
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("bob"), MaxObjectSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("REPORT", "/dav/addressbooks/alice/contacts/", strings.NewReader(etagSync("")))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusNotFound)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("leaked object name")
	}

	// Bob has a book of the same name; alice's token does not work there.
	bobRef := aliceRef
	bobRef.Owner = "bob"
	if _, err := st.CreateCollection(ctx, bobRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	aliceCol, _ := st.GetCollection(ctx, aliceRef)
	req = httptest.NewRequest("REPORT", "/dav/addressbooks/alice/contacts/", strings.NewReader(etagSync(syncToken(aliceCol))))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	wantPrecondition(t, rec, http.StatusForbidden, "valid-sync-token")
}

func TestSyncCap(t *testing.T) {
	old := maxSyncChanges
	maxSyncChanges = 3
	t.Cleanup(func() { maxSyncChanges = old })

	f := memFixture(t)
	putN(t, f, "a.vcf", "b.vcf", "c.vcf", "d.vcf", "e.vcf")
	// An initial sync is never cut: all objects, no 507, the current token.
	rec := f.sync(etagSync(""))
	if strings.Contains(rec.Body.String(), "507") {
		t.Errorf("initial sync truncated: %s", rec.Body)
	}
	s := parseSync(t, rec)
	eq(t, s.hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf", "d.vcf", "e.vcf"})
	col, _ := f.st.GetCollection(context.Background(), objRef)
	if s.token != syncToken(col) {
		t.Errorf("token = %q, want %q", s.token, syncToken(col))
	}
	// Following the token finds nothing left to do.
	if s2 := parseSync(t, f.sync(etagSync(s.token))); len(s2.hrefs) != 0 {
		t.Errorf("follow-up = %v", s2.hrefs)
	}

	// A delta over the cap forces a resync, which is uncapped.
	base := col
	touchN(t, f, "a.vcf", "b.vcf", "c.vcf", "d.vcf")
	f.wantInvalidToken(t, etagSync(syncToken(base)))
	eq(t, parseSync(t, f.sync(etagSync(""))).hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf", "d.vcf", "e.vcf"})

	// At the cap is fine.
	maxSyncChanges = 4
	eq(t, parseSync(t, f.sync(etagSync(syncToken(base)))).hrefNames(), []string{"a.vcf", "b.vcf", "c.vcf", "d.vcf"})
}

// changesFailStore fails Changes with err; changesStore wraps it.
type changesFailStore struct {
	store.Store
	err error
}

func (c changesFailStore) Changes(context.Context, store.CollectionRef, uint64) (store.Changes, error) {
	return store.Changes{}, c.err
}

func TestSyncStoreErrors(t *testing.T) {
	// Changes unavailable: valid-sync-token.
	f := newObjFixture(t, changesFailStore{Store: memory.New(), err: fmt.Errorf("x: %w", store.ErrChangesUnavailable)}, 1<<20)
	f.wantInvalidToken(t, etagSync(""))
	// Other failures are 500.
	f = newObjFixture(t, changesFailStore{Store: memory.New(), err: fmt.Errorf("disk on fire")}, 1<<20)
	wantStatus(t, f.sync(etagSync("")), http.StatusInternalServerError)
	// ListObjects failing is 500.
	f = newObjFixture(t, listFailStore{Store: memory.New()}, 1<<20)
	if _, err := f.st.PutObject(context.Background(), objRef, "a.vcf", card("a"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, f.sync(etagSync("")), http.StatusInternalServerError)
}

// changeRacingStore deletes an object after Changes has answered.
type changeRacingStore struct {
	store.Store
	victim string
}

func (c changeRacingStore) Changes(ctx context.Context, ref store.CollectionRef, since uint64) (store.Changes, error) {
	out, err := c.Store.Changes(ctx, ref, since)
	if err == nil {
		_ = c.DeleteObject(ctx, ref, c.victim, store.DeleteOptions{})
	}
	return out, err
}

func TestSyncObjectDeletedMidRequest(t *testing.T) {
	mem := memory.New()
	f := newObjFixture(t, changeRacingStore{Store: mem, victim: "b.vcf"}, 1<<20)
	for _, n := range []string{"a.vcf", "b.vcf"} {
		if _, err := mem.PutObject(context.Background(), objRef, n, card(n), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	s := parseSync(t, f.sync(etagSync("")))
	eq(t, s.hrefNames(), []string{"a.vcf", "b.vcf"})
	if s.status[0] != 0 || s.status[1] != 404 {
		t.Errorf("status = %v", s.status)
	}
}

func FuzzSyncCollection(f *testing.F) {
	f.Add(etagSync(""), "")
	f.Add(syncBody("urn:groupkit:sync:x:1", "1", `<CR:address-data/>`, ""), "")
	f.Add(syncBody("", "infinity", `<D:getetag/>`, ""), "")
	f.Add(`<D:sync-collection xmlns:D="DAV:"/>`, "urn:groupkit:sync:::")
	st := memory.New()
	ref := objRef
	if _, err := st.CreateCollection(context.Background(), ref, store.Props{}); err != nil {
		f.Fatal(err)
	}
	for _, n := range []string{"a.vcf", "b.vcf"} {
		if _, err := st.PutObject(context.Background(), ref, n, card(n), store.PutOptions{}); err != nil {
			f.Fatal(err)
		}
	}
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: 1 << 20})
	if err != nil {
		f.Fatal(err)
	}
	col, _ := st.GetCollection(context.Background(), ref)
	f.Fuzz(func(t *testing.T, body, token string) {
		for _, b := range []string{body, strings.Replace(body, "</D:sync-token>", token+"</D:sync-token>", 1), etagSync(token), etagSync(syncToken(col) + token)} {
			req := httptest.NewRequest("REPORT", objBase, strings.NewReader(b))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code >= 500 {
				t.Fatalf("status %d for body %q", rec.Code, b)
			}
		}
	})
}

// beforeChangesStore runs fn inside Changes, before the store answers.
type beforeChangesStore struct {
	store.Store
	fn func()
}

func (c beforeChangesStore) Changes(ctx context.Context, ref store.CollectionRef, since uint64) (store.Changes, error) {
	c.fn()
	return c.Store.Changes(ctx, ref, since)
}

func TestSyncRecreatedDuringChanges(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		armed := false
		wrap := beforeChangesStore{Store: st, fn: func() {
			if !armed {
				return
			}
			ctx := context.Background()
			if err := st.DeleteCollection(ctx, objRef); err != nil {
				t.Error(err)
			}
			if _, err := st.CreateCollection(ctx, objRef, store.Props{}); err != nil {
				t.Error(err)
			}
		}}
		f := newObjFixture(t, wrap, 1<<20)
		putN(t, f, "a.vcf")
		armed = true
		f.wantInvalidToken(t, etagSync(""))
	})
}

func TestSyncTokenRevisionComesFromChanges(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		armed := false
		wrap := beforeChangesStore{Store: st, fn: func() {
			if armed {
				if _, err := st.PutObject(context.Background(), objRef, "late.vcf", card("late"), store.PutOptions{}); err != nil {
					t.Error(err)
				}
			}
		}}
		f := newObjFixture(t, wrap, 1<<20)
		putN(t, f, "a.vcf")
		armed = true
		s := parseSync(t, f.sync(etagSync("")))
		eq(t, s.hrefNames(), []string{"a.vcf", "late.vcf"})
		col, _ := st.GetCollection(context.Background(), objRef)
		if s.token != syncToken(col) {
			t.Errorf("token = %q, want %q (the revision Changes answered)", s.token, syncToken(col))
		}
	})
}

func TestSyncPaddedToken(t *testing.T) {
	f := memFixture(t)
	putN(t, f, "a.vcf")
	tok := parseSync(t, f.sync(etagSync(""))).token
	s := parseSync(t, f.sync(etagSync("\n  "+tok+"\t ")))
	if len(s.hrefs) != 0 || s.token != tok {
		t.Errorf("padded token: %v %q", s.hrefs, s.token)
	}
}
