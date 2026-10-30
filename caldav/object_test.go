// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

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
	"sync/atomic"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/filestore"
	"github.com/LukasHirt/groupkit/store/memory"
)

const objBase = "/dav/calendars/alice/work/"

var objRef = store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}

type objFixture struct {
	h  *Handler
	st store.Store
}

func newObjFixture(t *testing.T, st store.Store, max int64) *objFixture {
	t.Helper()
	if _, err := st.CreateCollection(context.Background(), objRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	return emptyFixtureMax(t, st, max)
}

func emptyFixtureMax(t *testing.T, st store.Store, max int64) *objFixture {
	t.Helper()
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

var icsType = hdrs{"Content-Type": "text/calendar; charset=utf-8"}

func event(uid string) []byte {
	return []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
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
			wantSpace := dav.NSCalDAV
			if name == "valid-resourcetype" || name == "cannot-modify-protected-property" || name == "valid-sync-token" {
				wantSpace = "DAV:"
			}
			if c.Name.Space != wantSpace {
				t.Fatalf("%s is in namespace %q, want %q", name, c.Name.Space, wantSpace)
			}
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

func (f *objFixture) wantAbsent(t *testing.T, name string) {
	t.Helper()
	if _, err := f.st.GetObject(context.Background(), objRef, name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("%s: object exists or other error: %v", name, err)
	}
}

const pragueZone = "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\n" +
	"TZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

func TestRoundTripBytes(t *testing.T) {
	head := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n"
	bodies := map[string]string{
		"crlf":   string(event("a")),
		"lf":     "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//t//EN\nBEGIN:VEVENT\nUID:b\nDTSTAMP:20260101T000000Z\nEND:VEVENT\nEND:VCALENDAR\n",
		"mixed":  head + "BEGIN:VEVENT\nUID:c\r\nDTSTAMP:20260101T000000Z\nEND:VEVENT\r\nEND:VCALENDAR\n",
		"folded": head + "BEGIN:VEVENT\r\nUID:d\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:A very long summary that is\r\n  folded\r\nDESCRIPTION:one\r\n\ttwo\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"x-prop": head + "X-WR-CALNAME:Mine\r\nBEGIN:VEVENT\r\nUID:e\r\nDTSTAMP:20260101T000000Z\r\nX-CUSTOM;X-P=1:value\r\nX-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"vtimezone": head + pragueZone + "BEGIN:VEVENT\r\nUID:f\r\nDTSTAMP:20260101T000000Z\r\n" +
			"DTSTART;TZID=Europe/Prague:20260102T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		"nonl":      strings.TrimSuffix(string(event("g")), "\r\n"),
		"non-ascii": head + "BEGIN:VEVENT\r\nUID:h\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:Schöne Grüße 日本\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
	}
	for sn, mk := range storeFactories {
		t.Run(sn, func(t *testing.T) {
			f := newObjFixture(t, mk(t), 1<<20)
			for n, b := range bodies {
				name := n + ".ics"
				wantStatus(t, f.do("PUT", name, []byte(b), icsType), http.StatusCreated)
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
	body := event("g1")
	wantStatus(t, f.do("PUT", "g1.ics", body, icsType), 201)
	o := f.stored(t, "g1.ics")

	for _, m := range []string{"GET", "HEAD"} {
		rec := f.do(m, "g1.ics", nil, nil)
		wantStatus(t, rec, 200)
		hd := rec.Header()
		if hd.Get("ETag") != `"`+o.ETag+`"` {
			t.Errorf("%s ETag = %q", m, hd.Get("ETag"))
		}
		if hd.Get("Last-Modified") != dav.HTTPDate(o.ModTime) {
			t.Errorf("%s Last-Modified = %q", m, hd.Get("Last-Modified"))
		}
		if hd.Get("Content-Type") != "text/calendar; charset=utf-8" {
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
	wantStatus(t, f.do("GET", "nope.ics", nil, nil), 404)
	wantStatus(t, f.do("HEAD", "nope.ics", nil, nil), 404)
	wantStatus(t, f.doPath("GET", "/dav/calendars/alice/nocal/x.ics", nil, nil), 404)
}

func TestGetIfNoneMatch(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "g.ics", event("g"), icsType), 201)
	etag := `"` + f.stored(t, "g.ics").ETag + `"`
	for _, m := range []string{"GET", "HEAD"} {
		for _, inm := range []string{etag, "W/" + etag, "*", `"other", ` + etag} {
			rec := f.do(m, "g.ics", nil, hdrs{"If-None-Match": inm})
			wantStatus(t, rec, 304)
			if rec.Body.Len() != 0 {
				t.Errorf("304 has a body")
			}
			if rec.Header().Get("ETag") != etag {
				t.Errorf("304 ETag = %q", rec.Header().Get("ETag"))
			}
		}
		wantStatus(t, f.do(m, "g.ics", nil, hdrs{"If-None-Match": `"other"`}), 200)
		wantStatus(t, f.do(m, "g.ics", nil, hdrs{"If-None-Match": `"unterminated`}), 200)
	}
}

func TestPutCreateReplace(t *testing.T) {
	f := memFixture(t)
	rec := f.do("PUT", "a.ics", event("a"), icsType)
	wantStatus(t, rec, 201)
	e1 := f.stored(t, "a.ics").ETag
	if rec.Header().Get("ETag") != `"`+e1+`"` {
		t.Errorf("ETag = %q", rec.Header().Get("ETag"))
	}
	if rec.Body.Len() != 0 {
		t.Errorf("PUT has a body")
	}
	if f.stored(t, "a.ics").UID != "a" {
		t.Errorf("stored UID = %q", f.stored(t, "a.ics").UID)
	}
	moved := bytes.Replace(event("a"), []byte("SUMMARY:a"), []byte("SUMMARY:b"), 1)
	rec = f.do("PUT", "a.ics", moved, icsType)
	wantStatus(t, rec, 204)
	e2 := f.stored(t, "a.ics").ETag
	if e1 == e2 || rec.Header().Get("ETag") != `"`+e2+`"` {
		t.Errorf("ETag %q after replace, stored %q (was %q)", rec.Header().Get("ETag"), e2, e1)
	}
}

func TestPutMissingCalendar(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.doPath("PUT", "/dav/calendars/alice/nocal/a.ics", event("a"), icsType), 404)
	if _, err := f.st.GetCollection(context.Background(), store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "nocal"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("calendar was created: %v", err)
	}
}

// A missing calendar is 404 before the media type or the size is looked at.
func TestPutMissingCalendarBeatsOtherErrors(t *testing.T) {
	f := newObjFixture(t, memory.New(), 300)
	gone := "/dav/calendars/alice/nocal/a.ics"
	wantStatus(t, f.doPath("PUT", gone, event("a"), hdrs{"Content-Type": "text/plain"}), 404)
	big := bytes.Repeat([]byte("x"), 1000)
	wantStatus(t, f.doPath("PUT", gone, big, icsType), 404)
	wantStatus(t, f.doPath("PUT", gone, big, hdrs{"Content-Type": "text/plain"}), 404)
	wantStatus(t, f.doPath("PUT", gone, []byte("junk"), icsType), 404)
}

func flat(h hdrs) []string {
	var out []string
	for k, v := range h {
		out = append(out, k, v)
	}
	return out
}

func TestPutConditions(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
	cur := `"` + f.stored(t, "a.ics").ETag + `"`
	orig := f.stored(t, "a.ics").Data

	other := append(event("a")[:len(event("a")):len(event("a"))], '\r', '\n')

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
			wantStatus(t, f.do("PUT", "a.ics", other, with(icsType, flat(tt.h)...)), tt.want)
			if !bytes.Equal(f.stored(t, "a.ics").Data, orig) {
				t.Error("object changed")
			}
		})
	}
	wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-Match", cur)), 204)
	cur = `"` + f.stored(t, "a.ics").ETag + `"`
	wantStatus(t, f.do("PUT", "a.ics", other, with(icsType, "If-Match", `"x", `+cur)), 204)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-Match", "*")), 204)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-None-Match", `"zzz"`)), 204)
}

func TestPutConditionsMissing(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "n.ics", event("n"), with(icsType, "If-Match", "*")), 412)
	wantStatus(t, f.do("PUT", "n.ics", event("n"), with(icsType, "If-Match", `"x"`)), 412)
	f.wantAbsent(t, "n.ics")
	wantStatus(t, f.do("PUT", "n.ics", event("n"), with(icsType, "If-None-Match", "*")), 201)
}

// recordingStore records the options of PutObject and DeleteObject calls.
type recordingStore struct {
	store.Store
	mu      sync.Mutex
	puts    []store.PutOptions
	deletes []store.DeleteOptions
}

func (r *recordingStore) PutObject(ctx context.Context, ref store.CollectionRef, name string, data []byte, opts store.PutOptions) (store.Object, error) {
	r.mu.Lock()
	r.puts = append(r.puts, opts)
	r.mu.Unlock()
	return r.Store.PutObject(ctx, ref, name, data, opts)
}

func (r *recordingStore) DeleteObject(ctx context.Context, ref store.CollectionRef, name string, opts store.DeleteOptions) error {
	r.mu.Lock()
	r.deletes = append(r.deletes, opts)
	r.mu.Unlock()
	return r.Store.DeleteObject(ctx, ref, name, opts)
}

// Conditions reach the store only when the client sent a precondition header.
func TestStoreOptionsOnlyForConditionalRequests(t *testing.T) {
	rs := &recordingStore{Store: memory.New()}
	f := newObjFixture(t, rs, 1<<20)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 204)
	wantStatus(t, f.do("PUT", "b.ics", event("b"), with(icsType, "If-None-Match", "*")), 201)
	cur := `"` + f.stored(t, "a.ics").ETag + `"`
	wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-Match", cur)), 204)
	want := []store.PutOptions{
		{UID: "a"},
		{UID: "a"},
		{UID: "b", IfNoneMatch: true},
		{UID: "a", IfMatch: strings.Trim(cur, `"`)},
	}
	if len(rs.puts) != len(want) {
		t.Fatalf("puts = %+v", rs.puts)
	}
	for i := range want {
		if rs.puts[i].UID != want[i].UID || rs.puts[i].IfMatch != want[i].IfMatch || rs.puts[i].IfNoneMatch != want[i].IfNoneMatch {
			t.Errorf("put %d options = %+v, want %+v", i, rs.puts[i], want[i])
		}
	}

	wantStatus(t, f.do("DELETE", "b.ics", nil, nil), 204)
	cur = `"` + f.stored(t, "a.ics").ETag + `"`
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": cur}), 204)
	if len(rs.deletes) != 2 || rs.deletes[0].IfMatch != "" || rs.deletes[1].IfMatch != strings.Trim(cur, `"`) {
		t.Errorf("deletes = %+v", rs.deletes)
	}
}

func TestPutMapping(t *testing.T) {
	head := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n"
	ev := func(uid string) string {
		s := "BEGIN:VEVENT\r\n"
		if uid != "" {
			s += "UID:" + uid + "\r\n"
		}
		return s + "DTSTAMP:20260101T000000Z\r\nEND:VEVENT\r\n"
	}
	todo := "BEGIN:VTODO\r\nUID:t1\r\nDTSTAMP:20260101T000000Z\r\nEND:VTODO\r\n"
	tests := []struct {
		name string
		body string
		pre  string
	}{
		{"no uid", head + ev("") + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"two uids", head + ev("one") + ev("two") + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"method request", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nMETHOD:REQUEST\r\n" + ev("m") + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"todo in an event calendar", head + todo + "END:VCALENDAR\r\n", "supported-calendar-component"},
		{"mixed event and todo", head + ev("t1") + todo + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"not icalendar", "hello world", "valid-calendar-data"},
		{"empty", "", "valid-calendar-data"},
		{"vcard", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:A\r\nEND:VCARD\r\n", "valid-calendar-data"},
		{"no component", head + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"wrong version", "BEGIN:VCALENDAR\r\nVERSION:1.0\r\nPRODID:-//t//EN\r\n" + ev("v") + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"no prodid", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + ev("v") + "END:VCALENDAR\r\n", "valid-calendar-data"},
		{"binary", "\x00\xff\x00BEGIN:VCALENDAR", "valid-calendar-data"},
		{"two calendars", string(event("a")) + string(event("b")), "valid-calendar-data"},
		{"only a timezone", head + pragueZone + "END:VCALENDAR\r\n", "valid-calendar-data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := newObjFixture(t, st, 1<<20)
				wantPrecondition(t, f.do("PUT", "bad.ics", []byte(tt.body), icsType), 403, tt.pre)
				f.wantAbsent(t, "bad.ics")
			})
		})
	}
}

func eachStore(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	for name, mk := range storeFactories {
		t.Run(name, func(t *testing.T) { fn(t, mk(t)) })
	}
}

func TestPutAccepted(t *testing.T) {
	head := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n"
	master := "BEGIN:VEVENT\r\nUID:rec\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T100000Z\r\nRRULE:FREQ=DAILY;COUNT=5\r\nEND:VEVENT\r\n"
	override := "BEGIN:VEVENT\r\nUID:rec\r\nRECURRENCE-ID:20260103T100000Z\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260103T130000Z\r\nEND:VEVENT\r\n"
	zone := head + pragueZone + "BEGIN:VEVENT\r\nUID:z\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;TZID=Europe/Prague:20260102T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	for name, body := range map[string]string{
		"event and its moved override": head + master + override + "END:VCALENDAR\r\n",
		"event with a timezone":        zone,
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, st store.Store) {
				f := newObjFixture(t, st, 1<<20)
				wantStatus(t, f.do("PUT", "ok.ics", []byte(body), icsType), 201)
				if got := f.stored(t, "ok.ics"); string(got.Data) != body || got.UID == "" {
					t.Errorf("stored %q uid %q", got.Data, got.UID)
				}
			})
		})
	}
}

func TestPutContentType(t *testing.T) {
	f := memFixture(t)
	for i, ct := range []string{"text/plain", "application/json", "text/vcard", "garbage;;", "application/octet-stream", "text/calendarx", "xtext/calendar"} {
		rec := f.do("PUT", fmt.Sprintf("x%d.ics", i), event("x"), hdrs{"Content-Type": ct})
		wantStatus(t, rec, 415)
		if rec.Body.Len() != 0 {
			t.Errorf("%s: 415 has a body %q", ct, rec.Body.String())
		}
		f.wantAbsent(t, fmt.Sprintf("x%d.ics", i))
	}
	for i, ct := range []string{"text/calendar", "TEXT/CALENDAR; charset=UTF-8", "application/ics", "Application/ICS", "text/calendar; component=vevent; foo=bar", "text/calendar; charset"} {
		wantStatus(t, f.do("PUT", fmt.Sprintf("ok%d.ics", i), event(fmt.Sprint("ok", i)), hdrs{"Content-Type": ct}), 201)
	}
	// httptest sets no Content-Type unless asked.
	wantStatus(t, f.do("PUT", "none.ics", event("none"), nil), 201)
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) { c.n++; return 0, fmt.Errorf("read") }

func TestPutWrongMediaTypeIsAnsweredWithoutReadingTheBody(t *testing.T) {
	f := memFixture(t)
	body := &countingReader{}
	req := httptest.NewRequest("PUT", objBase+"x.ics", body)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType || body.n != 0 {
		t.Errorf("status %d, body read %d times", rec.Code, body.n)
	}
}

func TestPutSize(t *testing.T) {
	const max = 300
	f := newObjFixture(t, memory.New(), max)
	base := string(event("big"))
	pad := func(n int) []byte {
		fill := n - len(base) - len("X-PAD:\r\n")
		return []byte(strings.Replace(base, "END:VEVENT", "X-PAD:"+strings.Repeat("x", fill)+"\r\nEND:VEVENT", 1))
	}
	exact := pad(max)
	if len(exact) != max {
		t.Fatalf("len = %d", len(exact))
	}
	wantStatus(t, f.do("PUT", "big.ics", exact, icsType), 201)
	rec := f.do("PUT", "big2.ics", pad(max+1), icsType)
	wantPrecondition(t, rec, 413, "max-resource-size")
	f.wantAbsent(t, "big2.ics")
}

func TestPutTwelveMiB(t *testing.T) {
	st := memory.New()
	if _, err := st.CreateCollection(context.Background(), objRef, store.Props{}); err != nil {
		t.Fatal(err)
	}
	// The default limit is 10 MiB.
	h, err := New(Config{Store: st, Prefix: "/dav", Resolve: resolveAs("alice")})
	if err != nil {
		t.Fatal(err)
	}
	f := &objFixture{h: h, st: st}
	body := bytes.Replace(event("huge"), []byte("END:VEVENT"), []byte("DESCRIPTION:"+strings.Repeat("x", 12<<20)+"\r\nEND:VEVENT"), 1)
	rec := f.do("PUT", "huge.ics", body, icsType)
	wantPrecondition(t, rec, 413, "max-resource-size")
	f.wantAbsent(t, "huge.ics")
}

func TestPutUIDConflict(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "first%20one.ics", event("same"), icsType), 201)
		rec := f.do("PUT", "second.ics", event("same"), icsType)
		n := wantPrecondition(t, rec, 409, "no-uid-conflict")
		if n.Name != dav.CalNoUIDConflict {
			t.Errorf("name = %v", n.Name)
		}
		href := n.Child(dav.Href)
		if href == nil || href.Text != objBase+"first%20one.ics" {
			t.Errorf("href = %+v, want %q", href, objBase+"first%20one.ics")
		}
		f.wantAbsent(t, "second.ics")
	})
}

func TestPutChangeUIDFreesOld(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.ics", event("one"), icsType), 201)
	wantStatus(t, f.do("PUT", "a.ics", event("two"), icsType), 204)
	wantStatus(t, f.do("PUT", "b.ics", event("one"), icsType), 201)
	wantPrecondition(t, f.do("PUT", "c.ics", event("two"), icsType), 409, "no-uid-conflict")
}

func TestHostileNames(t *testing.T) {
	f := memFixture(t)
	for _, name := range []string{"%2e%2e", "%2e", "%00", strings.Repeat("a", 300), "a%2Fb", "a%0Ab"} {
		for _, m := range []string{"PUT", "GET", "DELETE", "HEAD"} {
			wantStatus(t, f.do(m, name, event("h"), icsType), 400)
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
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 400)
}

// failingStore fails object writes with a secret-bearing error.
type failingStore struct{ store.Store }

func (failingStore) PutObject(context.Context, store.CollectionRef, string, []byte, store.PutOptions) (store.Object, error) {
	return store.Object{}, errors.New("secret disk path /var/x")
}

func TestPutStoreFailure(t *testing.T) {
	f := memFixture(t)
	f.h.store = failingStore{f.st}
	rec := f.do("PUT", "a.ics", event("a"), icsType)
	wantStatus(t, rec, 500)
	if strings.Contains(rec.Body.String(), "secret") {
		t.Error("error detail leaked")
	}
}

func TestDelete(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("DELETE", "nope.ics", nil, nil), 404)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
	cur := `"` + f.stored(t, "a.ics").ETag + `"`
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": `"stale"`}), 412)
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": `"unterminated`}), 412)
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": "W/" + cur}), 412)
	f.stored(t, "a.ics")
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": cur}), 204)
	f.wantAbsent(t, "a.ics")
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": cur}), 404)

	wantStatus(t, f.do("PUT", "b.ics", event("b"), icsType), 201)
	wantStatus(t, f.do("DELETE", "b.ics", nil, nil), 204)
	f.wantAbsent(t, "b.ics")
	wantStatus(t, f.do("PUT", "c.ics", event("c"), icsType), 201)
	wantStatus(t, f.do("DELETE", "c.ics", nil, hdrs{"If-Match": "*"}), 204)
	wantStatus(t, f.doPath("DELETE", "/dav/calendars/alice/nocal/x.ics", nil, nil), 404)
}

func TestDeleteIfNoneMatch(t *testing.T) {
	f := memFixture(t)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
	cur := `"` + f.stored(t, "a.ics").ETag + `"`
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-None-Match": "*"}), 412)
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-None-Match": cur}), 412)
	f.stored(t, "a.ics")
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-None-Match": `"other"`}), 204)
	wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-None-Match": "*"}), 404)
}

// runRace runs n requests at once and returns the status counts.
func runRace(n int, req func(i int) int) map[int]int {
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes[i] = req(i)
		}()
	}
	close(start)
	wg.Wait()
	count := map[int]int{}
	for _, c := range codes {
		count[c]++
	}
	return count
}

func TestConcurrentCreate(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		const n = 32
		count := runRace(n, func(i int) int {
			return f.do("PUT", "race.ics", event(fmt.Sprint("u", i)), with(icsType, "If-None-Match", "*")).Code
		})
		if count[201] != 1 || count[412] != n-1 {
			t.Errorf("codes = %v, want one 201 and %d 412", count, n-1)
		}
	})
}

func TestConcurrentStaleIfMatch(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "race.ics", event("orig"), icsType), 201)
		cur := `"` + f.stored(t, "race.ics").ETag + `"`
		const n = 32
		count := runRace(n, func(i int) int {
			return f.do("PUT", "race.ics", event(fmt.Sprint("u", i)), with(icsType, "If-Match", cur)).Code
		})
		if count[204] != 1 || count[412] != n-1 {
			t.Errorf("codes = %v, want one 204 and %d 412", count, n-1)
		}
	})
}

func TestConcurrentUnconditionalPut(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		count := runRace(32, func(int) int { return f.do("PUT", "race.ics", event("same"), icsType).Code })
		for c := range count {
			if c != 201 && c != 204 {
				t.Errorf("codes = %v, want only 201 or 204", count)
			}
		}
		if count[201] < 1 {
			t.Errorf("codes = %v, want at least one 201", count)
		}
	})
}

func TestConcurrentConditionalDelete(t *testing.T) {
	eachStore(t, func(t *testing.T, st store.Store) {
		f := newObjFixture(t, st, 1<<20)
		wantStatus(t, f.do("PUT", "race.ics", event("d"), icsType), 201)
		cur := `"` + f.stored(t, "race.ics").ETag + `"`
		const n = 32
		count := runRace(n, func(int) int { return f.do("DELETE", "race.ics", nil, hdrs{"If-Match": cur}).Code })
		if count[204] != 1 || count[204]+count[404]+count[412] != n {
			t.Errorf("codes = %v, want exactly one 204 and the rest 404 or 412", count)
		}
		f.wantAbsent(t, "race.ics")
	})
}

func TestWritesLandInResolvedOwner(t *testing.T) {
	st := memory.New()
	bobRef := store.CollectionRef{Owner: "owner-bob", Kind: store.KindCalendar, Name: "work"}
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
	p := "/dav/calendars/bob/work/a.ics"
	wantStatus(t, f.doPath("PUT", p, event("a"), icsType), 201)
	if _, err := st.GetObject(context.Background(), bobRef, "a.ics"); err != nil {
		t.Fatalf("not stored for owner-bob: %v", err)
	}
	if _, err := st.GetObject(context.Background(), store.CollectionRef{Owner: "bob", Kind: store.KindCalendar, Name: "work"}, "a.ics"); err == nil {
		t.Error("stored under the URL user")
	}
	wantStatus(t, f.doPath("GET", p, nil, nil), 200)
	wantStatus(t, f.doPath("DELETE", p, nil, nil), 204)
	if _, err := st.GetObject(context.Background(), bobRef, "a.ics"); !errors.Is(err, store.ErrNotFound) {
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
	}{{"GET", nil}, {"HEAD", nil}, {"DELETE", nil}, {"DELETE", hdrs{"If-Match": "*"}}, {"PUT", icsType}} {
		rec := f.do(tt.m, "a.ics", event("a"), tt.h)
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
		wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
		etag := `"` + f.stored(t, "a.ics").ETag + `"`
		corrupt(t, dir, event("a"))
		return f, etag
	}
	t.Run("get is a generic 500", func(t *testing.T) {
		f, _ := setup(t)
		rec := f.do("GET", "a.ics", nil, nil)
		wantStatus(t, rec, 500)
		if strings.Contains(rec.Body.String(), "corrupt") || strings.Contains(rec.Body.String(), os.TempDir()) {
			t.Errorf("leaked %q", rec.Body.String())
		}
	})
	t.Run("put replaces", func(t *testing.T) {
		f, _ := setup(t)
		fixed := bytes.Replace(event("a"), []byte("SUMMARY:a"), []byte("SUMMARY:fixed"), 1)
		wantStatus(t, f.do("PUT", "a.ics", fixed, icsType), 204)
		if rec := f.do("GET", "a.ics", nil, nil); rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), fixed) {
			t.Errorf("GET after repair = %d %q", rec.Code, rec.Body.String())
		}
	})
	t.Run("put with if-match replaces", func(t *testing.T) {
		f, etag := setup(t)
		wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-Match", etag)), 204)
	})
	t.Run("put with a stale if-match is refused", func(t *testing.T) {
		f, _ := setup(t)
		wantStatus(t, f.do("PUT", "a.ics", event("a"), with(icsType, "If-Match", `"stale"`)), 412)
	})
	t.Run("delete removes", func(t *testing.T) {
		f, _ := setup(t)
		wantStatus(t, f.do("DELETE", "a.ics", nil, nil), 204)
		wantStatus(t, f.do("GET", "a.ics", nil, nil), 404)
	})
	t.Run("conditional delete removes", func(t *testing.T) {
		f, etag := setup(t)
		wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": `"stale"`}), 412)
		wantStatus(t, f.do("DELETE", "a.ics", nil, hdrs{"If-Match": etag}), 204)
	})
}

// A plain DELETE goes to the store without reading the object first.
type getCountingStore struct {
	store.Store
	gets atomic.Int64
}

func (c *getCountingStore) GetObject(ctx context.Context, ref store.CollectionRef, name string) (store.Object, error) {
	c.gets.Add(1)
	return c.Store.GetObject(ctx, ref, name)
}

func TestPlainDeleteReadsNothing(t *testing.T) {
	cs := &getCountingStore{Store: memory.New()}
	f := newObjFixture(t, cs, 1<<20)
	wantStatus(t, f.do("PUT", "a.ics", event("a"), icsType), 201)
	cs.gets.Store(0)
	wantStatus(t, f.do("DELETE", "a.ics", nil, nil), 204)
	if n := cs.gets.Load(); n != 0 {
		t.Errorf("DELETE read the object %d times", n)
	}
}

func FuzzPutObject(f *testing.F) {
	f.Add(event("a"), "text/calendar")
	f.Add([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:x\r\nBEGIN:VEVENT\r\nUID:u\r\nEND:VEVENT\r\nEND:VCALENDAR"), "application/ics; charset=utf-8")
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
		req := httptest.NewRequest("PUT", objBase+"f.ics", bytes.NewReader(body))
		req.Header["Content-Type"] = []string{ct}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("status %d", rec.Code)
		}
		if rec.Code == 201 || rec.Code == 204 {
			o, err := st.GetObject(context.Background(), objRef, "f.ics")
			if err != nil || !bytes.Equal(o.Data, body) {
				t.Fatalf("stored %q, %v; want %q", o.Data, err, body)
			}
		}
	})
}
