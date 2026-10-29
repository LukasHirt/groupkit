// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/filestore"
	"github.com/LukasHirt/groupkit/store/memory"
)

const objBase = "/dav/addressbooks/alice/contacts/"

var objRef = store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}

type objFixture struct {
	h  *Handler
	st store.Store
}

func newObjFixture(t *testing.T, st store.Store, max int64) *objFixture {
	t.Helper()
	if _, err := st.CreateCollection(context.Background(), objRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: max})
	if err != nil {
		t.Fatal(err)
	}
	return &objFixture{h: h, st: st}
}

func memFixture(t *testing.T) *objFixture { return newObjFixture(t, memory.New(), 1<<20) }

// storeFactories lists the stores the conformance-critical tests run against.
var storeFactories = map[string]func(t *testing.T) store.Store{
	"memory": func(*testing.T) store.Store { return memory.New() },
	"filestore": func(t *testing.T) store.Store {
		st, err := filestore.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	},
}

type hdrs map[string]string

func (f *objFixture) do(method, name string, body []byte, h hdrs) *httptest.ResponseRecorder {
	return f.doPath(method, objBase+name, body, h)
}

func (f *objFixture) doPath(method, path string, body []byte, h hdrs) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

var vcType = hdrs{"Content-Type": "text/vcard; charset=utf-8"}

func card(uid string) []byte {
	return []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:" + uid + "\r\nFN:" + uid + "\r\nEND:VCARD\r\n")
}

func with(h hdrs, kv ...string) hdrs {
	out := hdrs{}
	for k, v := range h {
		out[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %q", rec.Code, want, rec.Body.String())
	}
}

// wantPrecondition checks that the response is a DAV:error holding name.
func wantPrecondition(t *testing.T, rec *httptest.ResponseRecorder, status int, name string) *dav.Node {
	t.Helper()
	wantStatus(t, rec, status)
	root, err := dav.ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("body is not XML: %v; %q", err, rec.Body.String())
	}
	if root.Name != dav.ErrorEl {
		t.Fatalf("root = %v", root.Name)
	}
	for _, c := range root.Children {
		if c.Name.Local == name {
			return c
		}
	}
	t.Fatalf("no %s in %q", name, rec.Body.String())
	return nil
}

func (f *objFixture) stored(t *testing.T, name string) store.Object {
	t.Helper()
	o, err := f.st.GetObject(context.Background(), objRef, name)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRoundTripBytes(t *testing.T) {
	bodies := map[string]string{
		"crlf":   "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nUID:a\r\nEND:VCARD\r\n",
		"lf":     "BEGIN:VCARD\nVERSION:3.0\nFN:A\nUID:b\nEND:VCARD\n",
		"mixed":  "BEGIN:VCARD\r\nVERSION:3.0\nFN:A\r\nUID:c\nEND:VCARD\r\n",
		"folded": "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:d\r\nFN:A very long name that is\r\n  folded\r\nNOTE:one\r\n\ttwo\r\nEND:VCARD\r\n",
		"x-prop": "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:e\r\nFN:A\r\nX-CUSTOM;X-P=1:value\r\nitem1.X-ABLabel:friend\r\nEND:VCARD\r\n",
		"nonl":   "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:f\r\nFN:A\r\nEND:VCARD",
	}
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			for n, b := range bodies {
				name := n + ".vcf"
				wantStatus(t, f.do("PUT", name, []byte(b), vcType), http.StatusCreated)
				rec := f.do("GET", name, nil, nil)
				wantStatus(t, rec, 200)
				if rec.Body.String() != b {
					t.Errorf("%s: GET = %q, want %q", n, rec.Body.String(), b)
				}
			}
		})
	}
}

func TestGetHead(t *testing.T) {
	f := memFixture(t)
	body := card("g1")
	wantStatus(t, f.do("PUT", "g1.vcf", body, vcType), 201)
	o := f.stored(t, "g1.vcf")

	for _, m := range []string{"GET", "HEAD"} {
		rec := f.do(m, "g1.vcf", nil, nil)
		wantStatus(t, rec, 200)
		hd := rec.Header()
		if hd.Get("ETag") != `"`+o.ETag+`"` {
			t.Errorf("%s ETag = %q", m, hd.Get("ETag"))
		}
		if hd.Get("Last-Modified") != dav.HTTPDate(o.ModTime) {
			t.Errorf("%s Last-Modified = %q", m, hd.Get("Last-Modified"))
		}
		if hd.Get("Content-Type") != "text/vcard; charset=utf-8" {
			t.Errorf("%s Content-Type = %q", m, hd.Get("Content-Type"))
		}
		if hd.Get("Content-Length") != fmt.Sprint(len(body)) {
			t.Errorf("%s Content-Length = %q", m, hd.Get("Content-Length"))
		}
		if hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s nosniff missing", m)
		}
		if m == "HEAD" && rec.Body.Len() != 0 {
			t.Errorf("HEAD has a body")
		}
		if m == "GET" && !bytes.Equal(rec.Body.Bytes(), body) {
			t.Errorf("GET body = %q", rec.Body.String())
		}
	}
}

func TestGetMissing(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("GET", "nope.vcf", nil, nil), 404)
	wantStatus(t, f.do("HEAD", "nope.vcf", nil, nil), 404)
	wantStatus(t, f.doPath("GET", "/dav/addressbooks/alice/nobook/x.vcf", nil, nil), 404)
}

func TestGetIfNoneMatch(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "g.vcf", card("g"), vcType), 201)
	etag := `"` + f.stored(t, "g.vcf").ETag + `"`
	for _, m := range []string{"GET", "HEAD"} {
		for _, inm := range []string{etag, "W/" + etag, "*", `"other", ` + etag} {
			rec := f.do(m, "g.vcf", nil, hdrs{"If-None-Match": inm})
			wantStatus(t, rec, 304)
			if rec.Body.Len() != 0 {
				t.Errorf("304 has a body")
			}
			if rec.Header().Get("ETag") != etag {
				t.Errorf("304 ETag = %q", rec.Header().Get("ETag"))
			}
		}
		wantStatus(t, f.do(m, "g.vcf", nil, hdrs{"If-None-Match": `"other"`}), 200)
		wantStatus(t, f.do(m, "g.vcf", nil, hdrs{"If-None-Match": `"unterminated`}), 200)
	}
}

func TestPutCreateReplace(t *testing.T) {
	f := memFixture(t)
	rec := f.do("PUT", "a.vcf", card("a"), vcType)
	wantStatus(t, rec, 201)
	e1 := f.stored(t, "a.vcf").ETag
	if rec.Header().Get("ETag") != `"`+e1+`"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Body.Len() != 0 {
		t.Errorf("PUT has a body")
	}
	rec = f.do("PUT", "a.vcf", []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:a\r\nFN:B\r\nEND:VCARD\r\n"), vcType)
	wantStatus(t, rec, 204)
	e2 := f.stored(t, "a.vcf").ETag
	if e1 == e2 || rec.Header().Get("ETag") != `"`+e2+`"` {
		t.Errorf("ETag %q after replace, stored %q (was %q)", rec.Header().Get("ETag"), e2, e1)
	}
}

func TestPutMissingBook(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.doPath("PUT", "/dav/addressbooks/alice/nobook/a.vcf", card("a"), vcType), 404)
	if _, err := f.st.GetCollection(context.Background(), store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "nobook"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("book was created: %v", err)
	}
}

func TestPutConditions(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), 201)
	cur := `"` + f.stored(t, "a.vcf").ETag + `"`
	orig := f.stored(t, "a.vcf").Data

	other := card("a")
	other = append(other[:len(other):len(other)], '\r', '\n')

	tests := []struct {
		name string
		h    hdrs
		want int
	}{
		{"if-none-match star on existing", hdrs{"If-None-Match": "*"}, 412},
		{"if-none-match current", hdrs{"If-None-Match": cur}, 412},
		{"stale if-match", hdrs{"If-Match": `"stale"`}, 412},
		{"weak if-match", hdrs{"If-Match": "W/" + cur}, 412},
		{"malformed if-match", hdrs{"If-Match": `"unterminated`}, 412},
		{"malformed if-none-match", hdrs{"If-None-Match": `"unterminated`}, 412},
		{"list without current", hdrs{"If-Match": `"x", "y"`}, 412},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantStatus(t, f.do("PUT", "a.vcf", other, with(vcType, flat(tt.h)...)), tt.want)
			if !bytes.Equal(f.stored(t, "a.vcf").Data, orig) {
				t.Error("object changed")
			}
		})
	}
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), with(vcType, "If-Match", cur)), 204)
	cur = `"` + f.stored(t, "a.vcf").ETag + `"`
	wantStatus(t, f.do("PUT", "a.vcf", other, with(vcType, "If-Match", `"x", `+cur)), 204)
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), with(vcType, "If-Match", "*")), 204)
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), with(vcType, "If-None-Match", `"zzz"`)), 204)
}

func flat(h hdrs) []string {
	var out []string
	for k, v := range h {
		out = append(out, k, v)
	}
	return out
}

func TestPutConditionsMissing(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "n.vcf", card("n"), with(vcType, "If-Match", "*")), 412)
	wantStatus(t, f.do("PUT", "n.vcf", card("n"), with(vcType, "If-Match", `"x"`)), 412)
	if _, err := f.st.GetObject(context.Background(), objRef, "n.vcf"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("object created: %v", err)
	}
	wantStatus(t, f.do("PUT", "n.vcf", card("n"), with(vcType, "If-None-Match", "*")), 201)
}

func TestPutInvalid(t *testing.T) {
	f := memFixture(t)
	wantPrecondition(t, f.do("PUT", "bad.vcf", []byte("hello"), vcType), 403, "valid-address-data")
	wantPrecondition(t, f.do("PUT", "bad.vcf", nil, vcType), 403, "valid-address-data")
	v21 := []byte("BEGIN:VCARD\r\nVERSION:2.1\r\nFN:A\r\nEND:VCARD\r\n")
	wantPrecondition(t, f.do("PUT", "bad.vcf", v21, vcType), 403, "supported-address-data")
	two := append(card("a"), card("b")...)
	wantPrecondition(t, f.do("PUT", "bad.vcf", two, vcType), 403, "valid-address-data")
	if _, err := f.st.GetObject(context.Background(), objRef, "bad.vcf"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("invalid object stored: %v", err)
	}
}

func TestPutContentType(t *testing.T) {
	f := memFixture(t)
	for i, ct := range []string{"text/plain", "application/json", "text/calendar", "garbage;;", "application/octet-stream"} {
		rec := f.do("PUT", fmt.Sprintf("x%d.vcf", i), card("x"), hdrs{"Content-Type": ct})
		wantStatus(t, rec, 415)
		if rec.Body.Len() != 0 {
			t.Errorf("%s: 415 has a body %q", ct, rec.Body.String())
		}
	}
	for i, ct := range []string{"text/vcard", "TEXT/VCARD; charset=UTF-8", "text/x-vcard", "text/vcard; version=4.0; foo=bar"} {
		wantStatus(t, f.do("PUT", fmt.Sprintf("ok%d.vcf", i), card(fmt.Sprint("ok", i)), hdrs{"Content-Type": ct}), 201)
	}
	// httptest sets no Content-Type unless asked.
	wantStatus(t, f.do("PUT", "none.vcf", card("none"), nil), 201)
}

func TestPutSize(t *testing.T) {
	const max = 200
	f := newObjFixture(t, memory.New(), max)
	base := string(card("big"))
	pad := func(n int) []byte {
		fill := n - len(base) - len("NOTE:\r\n")
		return []byte(strings.Replace(base, "END:VCARD", "NOTE:"+strings.Repeat("x", fill)+"\r\nEND:VCARD", 1))
	}
	exact := pad(max)
	if len(exact) != max {
		t.Fatalf("len = %d", len(exact))
	}
	wantStatus(t, f.do("PUT", "big.vcf", exact, vcType), 201)
	rec := f.do("PUT", "big2.vcf", pad(max+1), vcType)
	wantPrecondition(t, rec, 413, "max-resource-size")
	if _, err := f.st.GetObject(context.Background(), objRef, "big2.vcf"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("oversized object stored: %v", err)
	}
}

func TestPutUIDConflict(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "first%20one.vcf", card("same"), vcType), 201)
	rec := f.do("PUT", "second.vcf", card("same"), vcType)
	n := wantPrecondition(t, rec, 409, "no-uid-conflict")
	if n.Name != dav.CRNoUIDConflict {
		t.Errorf("name = %v", n.Name)
	}
	href := n.Child(dav.Href)
	if href == nil || href.Text != objBase+"first%20one.vcf" {
		t.Errorf("href = %+v, want %q", href, objBase+"first%20one.vcf")
	}
	if _, err := f.st.GetObject(context.Background(), objRef, "second.vcf"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("conflicting object stored: %v", err)
	}
}

func TestPutChangeUIDFreesOld(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.vcf", card("one"), vcType), 201)
	wantStatus(t, f.do("PUT", "a.vcf", card("two"), vcType), 204)
	wantStatus(t, f.do("PUT", "b.vcf", card("one"), vcType), 201)
	wantPrecondition(t, f.do("PUT", "c.vcf", card("two"), vcType), 409, "no-uid-conflict")
}

func TestHostileNames(t *testing.T) {
	f := memFixture(t)
	for _, name := range []string{"%2e%2e", "%2e", "%00", strings.Repeat("a", 300), "a%2Fb", "a%0Ab"} {
		for _, m := range []string{"PUT", "GET", "DELETE", "HEAD"} {
			rec := f.do(m, name, card("h"), vcType)
			wantStatus(t, rec, 400)
		}
	}
}

// invalidNameStore reports ErrInvalidName for object writes.
type invalidNameStore struct{ store.Store }

func (invalidNameStore) PutObject(context.Context, store.CollectionRef, string, []byte, store.PutOptions) (store.Object, error) {
	return store.Object{}, fmt.Errorf("wrapped: %w", store.ErrInvalidName)
}

func TestPutStoreInvalidName(t *testing.T) {
	f := memFixture(t)
	f.h.store = invalidNameStore{f.st}
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), 400)
}

// failingStore fails object writes with a secret-bearing error.
type failingStore struct{ store.Store }

func (failingStore) PutObject(context.Context, store.CollectionRef, string, []byte, store.PutOptions) (store.Object, error) {
	return store.Object{}, errors.New("secret disk path /var/x")
}

func TestPutStoreFailure(t *testing.T) {
	f := memFixture(t)
	f.h.store = failingStore{f.st}
	rec := f.do("PUT", "a.vcf", card("a"), vcType)
	wantStatus(t, rec, 500)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("error detail leaked")
	}
}

func TestDelete(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("DELETE", "nope.vcf", nil, nil), 404)
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), 201)
	cur := `"` + f.stored(t, "a.vcf").ETag + `"`
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": `"stale"`}), 412)
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": `"unterminated`}), 412)
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": "W/" + cur}), 412)
	f.stored(t, "a.vcf")
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": cur}), 204)
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": cur}), 404)

	wantStatus(t, f.do("PUT", "b.vcf", card("b"), vcType), 201)
	wantStatus(t, f.do("DELETE", "b.vcf", nil, nil), 204)
	wantStatus(t, f.do("PUT", "c.vcf", card("c"), vcType), 201)
	wantStatus(t, f.do("DELETE", "c.vcf", nil, hdrs{"If-Match": "*"}), 204)
	wantStatus(t, f.doPath("DELETE", "/dav/addressbooks/alice/nobook/x.vcf", nil, nil), 404)
}

func TestConcurrentCreate(t *testing.T) {
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			const n = 32
			codes := make([]int, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					codes[i] = f.do("PUT", "race.vcf", card(fmt.Sprint("u", i)), with(vcType, "If-None-Match", "*")).Code
				}()
			}
			close(start)
			wg.Wait()
			count := map[int]int{}
			for _, c := range codes {
				count[c]++
			}
			if count[201] != 1 || count[412] != n-1 {
				t.Errorf("codes = %v, want one 201 and %d 412", count, n-1)
			}
		})
	}
}

func TestConcurrentStaleIfMatch(t *testing.T) {
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			wantStatus(t, f.do("PUT", "race.vcf", card("orig"), vcType), 201)
			cur := `"` + f.stored(t, "race.vcf").ETag + `"`
			const n = 32
			codes := make([]int, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					codes[i] = f.do("PUT", "race.vcf", card(fmt.Sprint("u", i)), with(vcType, "If-Match", cur)).Code
				}()
			}
			close(start)
			wg.Wait()
			count := map[int]int{}
			for _, c := range codes {
				count[c]++
			}
			if count[204] != 1 || count[412] != n-1 {
				t.Errorf("codes = %v, want one 204 and %d 412", count, n-1)
			}
		})
	}
}

func FuzzPutObject(f *testing.F) {
	f.Add(card("a"), "text/vcard")
	f.Add([]byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:x\r\nEND:VCARD"), "text/x-vcard; charset=utf-8")
	f.Add([]byte("junk"), "")
	f.Add([]byte{0, 0xff, '\r', '\n'}, "\x00;;;")
	f.Fuzz(func(t *testing.T, body []byte, ct string) {
		if strings.ContainsAny(ct, "\r\n") {
			t.Skip()
		}
		st := memory.New()
		if _, err := st.CreateCollection(context.Background(), objRef, store.Props{}); err != nil {
			t.Fatal(err)
		}
		h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice"), MaxObjectSize: 4096})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("PUT", objBase+"f.vcf", bytes.NewReader(body))
		req.Header["Content-Type"] = []string{ct}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("status %d", rec.Code)
		}
		if rec.Code == 201 || rec.Code == 204 {
			o, err := st.GetObject(context.Background(), objRef, "f.vcf")
			if err != nil || !bytes.Equal(o.Data, body) {
				t.Fatalf("stored %q, %v; want %q", o.Data, err, body)
			}
		}
	})
}

func TestPutMediaTypeBadParameter(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "p.vcf", card("p"), hdrs{"Content-Type": "text/vcard; charset"}), 201)
	wantStatus(t, f.do("PUT", "q.vcf", card("q"), hdrs{"Content-Type": "text/plain; charset"}), 415)
}

func TestDeleteIfNoneMatch(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), 201)
	cur := `"` + f.stored(t, "a.vcf").ETag + `"`
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-None-Match": "*"}), 412)
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-None-Match": cur}), 412)
	f.stored(t, "a.vcf")
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-None-Match": `"other"`}), 204)
	wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-None-Match": "*"}), 404)
}

func TestConcurrentUnconditionalPut(t *testing.T) {
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			const n = 32
			codes := make([]int, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					codes[i] = f.do("PUT", "race.vcf", card("same"), vcType).Code
				}()
			}
			close(start)
			wg.Wait()
			for _, c := range codes {
				if c != 201 && c != 204 {
					t.Errorf("status %d, want 201 or 204", c)
				}
			}
		})
	}
}

func TestConcurrentConditionalDelete(t *testing.T) {
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			wantStatus(t, f.do("PUT", "race.vcf", card("d"), vcType), 201)
			cur := `"` + f.stored(t, "race.vcf").ETag + `"`
			const n = 32
			codes := make([]int, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					codes[i] = f.do("DELETE", "race.vcf", nil, hdrs{"If-Match": cur}).Code
				}()
			}
			close(start)
			wg.Wait()
			count := map[int]int{}
			for _, c := range codes {
				count[c]++
			}
			if count[204] != 1 || count[204]+count[404]+count[412] != n {
				t.Errorf("codes = %v, want exactly one 204 and the rest 404 or 412", count)
			}
		})
	}
}

func TestWritesLandInResolvedOwner(t *testing.T) {
	st := memory.New()
	bobRef := store.CollectionRef{Owner: "owner-bob", Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := st.CreateCollection(context.Background(), bobRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: func(_ *http.Request, u string) (string, error) {
		return "owner-" + u, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f := &objFixture{h: h, st: st}
	p := "/dav/addressbooks/bob/contacts/a.vcf"
	wantStatus(t, f.doPath("PUT", p, card("a"), vcType), 201)
	if _, err := st.GetObject(context.Background(), bobRef, "a.vcf"); err != nil {
		t.Fatalf("not stored for owner-bob: %v", err)
	}
	wantStatus(t, f.doPath("GET", p, nil, nil), 200)
	wantStatus(t, f.doPath("DELETE", p, nil, nil), 204)
	if _, err := st.GetObject(context.Background(), bobRef, "a.vcf"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("object still there: %v", err)
	}
}

// errStore fails reads and deletes with an error that must not reach clients.
type errStore struct{ store.Store }

var errSecret = errors.New("secret disk path /var/x")

func (errStore) GetObject(context.Context, store.CollectionRef, string) (store.Object, error) {
	return store.Object{}, errSecret
}
func (errStore) ListObjects(context.Context, store.CollectionRef, store.ListOptions) ([]store.Object, error) {
	return nil, errSecret
}
func (errStore) DeleteObject(context.Context, store.CollectionRef, string, store.DeleteOptions) error {
	return errSecret
}

func TestStoreErrorsDoNotLeak(t *testing.T) {
	f := memFixture(t)
	f.h.store = errStore{f.st}
	for _, tt := range []struct {
		m string
		h hdrs
	}{{"GET", nil}, {"HEAD", nil}, {"DELETE", nil}, {"DELETE", hdrs{"If-Match": "*"}}, {"PUT", vcType}} {
		rec := f.do(tt.m, "a.vcf", card("a"), tt.h)
		wantStatus(t, rec, 500)
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "/var") {
			t.Errorf("%s leaked %q", tt.m, rec.Body.String())
		}
	}
}

// corrupt overwrites the data file holding body somewhere under dir.
func corrupt(t *testing.T, dir string, body []byte) {
	t.Helper()
	done := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, rerr := os.ReadFile(p); rerr == nil && bytes.Equal(b, body) {
			done = true
			return os.WriteFile(p, []byte("evil"), 0o600)
		}
		return nil
	})
	if err != nil || !done {
		t.Fatalf("corrupt: done=%v err=%v", done, err)
	}
}

func TestDamagedObjectCanBeRepaired(t *testing.T) {
	setup := func(t *testing.T) (*objFixture, string) {
		dir := t.TempDir()
		st, err := filestore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "a.vcf", card("a"), vcType), 201)
		etag := `"` + f.stored(t, "a.vcf").ETag + `"`
		corrupt(t, dir, card("a"))
		return f, etag
	}
	t.Run("get is a generic 500", func(t *testing.T) {
		f, _ := setup(t)
		rec := f.do("GET", "a.vcf", nil, nil)
		wantStatus(t, rec, 500)
		if strings.Contains(rec.Body.String(), "corrupt") || strings.Contains(rec.Body.String(), os.TempDir()) {
			t.Errorf("leaked %q", rec.Body.String())
		}
	})
	t.Run("put replaces", func(t *testing.T) {
		f, _ := setup(t)
		fixed := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:a\r\nFN:fixed\r\nEND:VCARD\r\n")
		wantStatus(t, f.do("PUT", "a.vcf", fixed, vcType), 204)
		if rec := f.do("GET", "a.vcf", nil, nil); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), fixed) {
			t.Errorf("GET after repair = %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("put with if-match replaces", func(t *testing.T) {
		f, etag := setup(t)
		wantStatus(t, f.do("PUT", "a.vcf", card("a"), with(vcType, "If-Match", etag)), 204)
	})
	t.Run("delete removes", func(t *testing.T) {
		f, _ := setup(t)
		wantStatus(t, f.do("DELETE", "a.vcf", nil, nil), 204)
		wantStatus(t, f.do("GET", "a.vcf", nil, nil), 404)
	})
	t.Run("conditional delete removes", func(t *testing.T) {
		f, etag := setup(t)
		wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": `"stale"`}), 412)
		wantStatus(t, f.do("DELETE", "a.vcf", nil, hdrs{"If-Match": etag}), 204)
	})
}
