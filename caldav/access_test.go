// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

var accessLevels = []store.Access{store.AccessFull, store.AccessObjects, store.AccessRead}

// newAccessHandler serves alice's calendar "work", holding a.ics, through a
// store that reports level for every collection.
func newAccessHandler(t *testing.T, level store.Access) *Handler {
	t.Helper()
	mem := memory.New()
	if _, err := mem.CreateCollection(context.Background(), workRef, store.Props{DisplayName: "Work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.PutObject(context.Background(), workRef, "a.ics", icsFor("a.ics"), store.PutOptions{UID: "a.ics"}); err != nil {
		t.Fatal(err)
	}
	h, err := New(Config{Store: storetest.WithAccess(mem, level), Prefix: "/dav", Resolve: resolveAs("alice")})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// outcome describes a response as its status, followed by the precondition
// of a DAV:error body when there is one: "201" or "403 need-privileges".
func outcome(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	out := strconv.Itoa(rec.Code)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/xml") {
		return out
	}
	root, err := dav.ParseXML(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("parse %d body: %v\n%s", rec.Code, err, rec.Body)
	}
	if root.Name == dav.ErrorEl && len(root.Children) == 1 {
		out += " " + root.Children[0].Name.Local
	}
	return out
}

func TestMethodsByAccessLevel(t *testing.T) {
	const (
		proppatch = `<?xml version="1.0"?><D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Renamed</D:displayname></D:prop></D:set></D:propertyupdate>`
		mkcol     = `<?xml version="1.0"?><D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:set><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype></D:prop></D:set></D:mkcol>`
		query     = `<?xml version="1.0"?><C:calendar-query ` + nsDecl + `><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`
		multiget  = `<?xml version="1.0"?><C:calendar-multiget ` + nsDecl + `><D:prop><D:getetag/></D:prop><D:href>` + calURL + `a.ics</D:href></C:calendar-multiget>`
		sync      = `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	)
	cases := []struct {
		name, method, target, body string
		// want is the outcome at full, objects and read access.
		want [3]string
	}{
		{"PUT new object", "PUT", calURL + "new.ics", string(icsFor("new.ics")), [3]string{"201", "201", "403 need-privileges"}},
		{"PUT existing object", "PUT", calURL + "a.ics", strings.Replace(string(icsFor("a.ics")), "SUMMARY:a.ics", "SUMMARY:changed", 1), [3]string{"204", "204", "403 need-privileges"}},
		// A read-only caller is refused before the body is looked at.
		{"PUT garbage", "PUT", calURL + "a.ics", "garbage", [3]string{"403 valid-calendar-data", "403 valid-calendar-data", "403 need-privileges"}},
		{"DELETE object", "DELETE", calURL + "a.ics", "", [3]string{"204", "204", "403 need-privileges"}},
		{"PROPPATCH", "PROPPATCH", calURL, proppatch, [3]string{"207", "403 need-privileges", "403 need-privileges"}},
		{"MKCALENDAR", "MKCALENDAR", "/dav/calendars/alice/new/", "", [3]string{"201", "403 need-privileges", "403 need-privileges"}},
		{"MKCOL", "MKCOL", "/dav/calendars/alice/new/", mkcol, [3]string{"201", "403 need-privileges", "403 need-privileges"}},
		{"DELETE calendar", "DELETE", calURL, "", [3]string{"204", "403 need-privileges", "403 need-privileges"}},
		{"GET", "GET", calURL + "a.ics", "", [3]string{"200", "200", "200"}},
		{"PROPFIND", "PROPFIND", calURL, "", [3]string{"207", "207", "207"}},
		{"REPORT calendar-query", "REPORT", calURL, query, [3]string{"207", "207", "207"}},
		{"REPORT calendar-multiget", "REPORT", calURL, multiget, [3]string{"207", "207", "207"}},
		{"REPORT sync-collection", "REPORT", calURL, sync, [3]string{"207", "207", "207"}},
	}
	for _, c := range cases {
		for i, level := range accessLevels {
			t.Run(c.name+"/"+level.String(), func(t *testing.T) {
				h := newAccessHandler(t, level)
				req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
				req.Header.Set("Depth", "1")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if got := outcome(t, rec); got != c.want[i] {
					t.Errorf("%s %s at %s access = %q, want %q", c.method, c.target, level, got, c.want[i])
				}
			})
		}
	}
}

func TestPrivilegesByAccessLevel(t *testing.T) {
	want := map[store.Access][2]string{
		store.AccessFull:    {"read,write,write-properties,write-content,bind,unbind", "read,write,write-content"},
		store.AccessObjects: {"read,write-content,bind,unbind", "read,write,write-content"},
		store.AccessRead:    {"read", "read"},
	}
	for _, level := range accessLevels {
		t.Run(level.String(), func(t *testing.T) {
			h := newAccessHandler(t, level)
			privs := func(target, depth string) []string {
				req := httptest.NewRequest("PROPFIND", target, strings.NewReader(propBody(`<D:current-user-privilege-set/>`)))
				req.Header.Set("Depth", depth)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				r := parseReply(t, rec)
				var out []string
				for i := range r.hrefs {
					var names []string
					for _, p := range r.must(t, i, dav.CurrentUserPrivilegeSet).Kids(dav.Privilege) {
						names = append(names, p.Children[0].Name.Local)
					}
					out = append(out, strings.Join(names, ","))
				}
				return out
			}
			// The calendar and its object, listed together and asked for alone.
			if got := privs(calURL, "1"); len(got) != 2 || got[0] != want[level][0] || got[1] != want[level][1] {
				t.Errorf("depth 1 privileges = %v, want %v", got, want[level])
			}
			if got := privs(calURL+"a.ics", "0"); len(got) != 1 || got[0] != want[level][1] {
				t.Errorf("object privileges = %v, want %q", got, want[level][1])
			}
			// The home is the caller's own and keeps its privileges.
			if got := privs("/dav/calendars/alice/", "0"); len(got) != 1 || got[0] != "read,bind" {
				t.Errorf("home privileges = %v", got)
			}
			// Report responses carry the object privileges too.
			body := `<?xml version="1.0"?><C:calendar-multiget ` + nsDecl + `><D:prop><D:current-user-privilege-set/></D:prop><D:href>` + calURL + `a.ics</D:href></C:calendar-multiget>`
			req := httptest.NewRequest("REPORT", calURL, strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			r := parseReply(t, rec)
			var names []string
			for _, p := range r.must(t, 0, dav.CurrentUserPrivilegeSet).Kids(dav.Privilege) {
				names = append(names, p.Children[0].Name.Local)
			}
			if got := strings.Join(names, ","); got != want[level][1] {
				t.Errorf("multiget privileges = %q, want %q", got, want[level][1])
			}
		})
	}
}

// TestReadOnlyPropfindGolden pins the exact 207 a read-only calendar and its
// object answer for current-user-privilege-set.
func TestReadOnlyPropfindGolden(t *testing.T) {
	h := newAccessHandler(t, store.AccessRead)
	req := httptest.NewRequest("PROPFIND", calURL, strings.NewReader(propBody(`<D:current-user-privilege-set/>`)))
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	privilege := `<D:current-user-privilege-set><D:privilege><D:read/></D:privilege></D:current-user-privilege-set>`
	response := func(href string) string {
		return `<D:response><D:href>` + href + `</D:href><D:propstat><D:prop>` + privilege + `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`
	}
	want := `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:CS="http://calendarserver.org/ns/" xmlns:A="http://apple.com/ns/ical/">` +
		response(calURL) + response(calURL+"a.ics") + `</D:multistatus>`
	if got := rec.Body.String(); rec.Code != http.StatusMultiStatus || got != want {
		t.Errorf("status %d, body\n%s\nwant\n%s", rec.Code, got, want)
	}
}

func TestReadOnlySyncCollection(t *testing.T) {
	h := newAccessHandler(t, store.AccessRead)
	body := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	req := httptest.NewRequest("REPORT", calURL, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := parseReply(t, rec)
	if len(r.hrefs) != 1 || r.hrefs[0] != calURL+"a.ics" {
		t.Errorf("hrefs = %v, want the one object", r.hrefs)
	}
	if r.must(t, 0, dav.GetETag).Text == "" {
		t.Error("no getetag")
	}
	if tok := r.root.Child(dav.SyncToken); tok == nil || !strings.HasPrefix(tok.Text, "urn:groupkit:sync:") {
		t.Errorf("sync-token = %v", tok)
	}
}

// TestPermissionErrorIs403 pins the mapping for a store that refuses on its
// own, without the engine's pre-check: an object DELETE goes straight to the
// store.
func TestPermissionErrorIs403(t *testing.T) {
	h := newAccessHandler(t, store.AccessRead)
	req := httptest.NewRequest("DELETE", calURL+"a.ics", nil)
	req.Header.Set("If-Match", "*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := outcome(t, rec); got != "403 need-privileges" {
		t.Errorf("DELETE with If-Match at read access = %q", got)
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d", rec.Code)
	}
}
