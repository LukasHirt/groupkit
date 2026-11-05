// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

var (
	accessLevels = []store.Access{store.AccessFull, store.AccessObjects, store.AccessRead}
	contactsRef  = store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
)

// newAccessHandler serves alice's address book "contacts", holding a.vcf,
// through a store that reports level for every collection.
func newAccessHandler(t *testing.T, level store.Access) *Handler {
	t.Helper()
	mem := memory.New()
	if _, err := mem.CreateCollection(context.Background(), contactsRef, store.Props{DisplayName: "Contacts"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.PutObject(context.Background(), contactsRef, "a.vcf", vcardFor("a.vcf"), store.PutOptions{UID: "a.vcf"}); err != nil {
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
		mkcol     = `<?xml version="1.0"?><D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav"><D:set><D:prop><D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype></D:prop></D:set></D:mkcol>`
		bookQuery = `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:prop><D:getetag/></D:prop></CR:addressbook-query>`
		multiget  = `<?xml version="1.0"?><CR:addressbook-multiget ` + nsDecl + `><D:prop><D:getetag/></D:prop><D:href>` + bookURL + `a.vcf</D:href></CR:addressbook-multiget>`
		sync      = `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	)
	cases := []struct {
		name, method, target, body string
		// want is the outcome at full, objects and read access.
		want [3]string
	}{
		{"PUT new object", "PUT", bookURL + "new.vcf", string(vcardFor("new.vcf")), [3]string{"201", "201", "403 need-privileges"}},
		{"PUT existing object", "PUT", bookURL + "a.vcf", strings.Replace(string(vcardFor("a.vcf")), "FN:a.vcf", "FN:changed", 1), [3]string{"204", "204", "403 need-privileges"}},
		// A read-only caller is refused before the body is looked at.
		{"PUT garbage", "PUT", bookURL + "a.vcf", "garbage", [3]string{"403 valid-address-data", "403 valid-address-data", "403 need-privileges"}},
		{"DELETE object", "DELETE", bookURL + "a.vcf", "", [3]string{"204", "204", "403 need-privileges"}},
		{"PROPPATCH", "PROPPATCH", bookURL, proppatch, [3]string{"207", "403 need-privileges", "403 need-privileges"}},
		{"MKCOL", "MKCOL", "/dav/addressbooks/alice/new/", mkcol, [3]string{"201", "403 need-privileges", "403 need-privileges"}},
		{"DELETE address book", "DELETE", bookURL, "", [3]string{"204", "403 need-privileges", "403 need-privileges"}},
		{"GET", "GET", bookURL + "a.vcf", "", [3]string{"200", "200", "200"}},
		{"PROPFIND", "PROPFIND", bookURL, "", [3]string{"207", "207", "207"}},
		{"REPORT addressbook-query", "REPORT", bookURL, bookQuery, [3]string{"207", "207", "207"}},
		{"REPORT addressbook-multiget", "REPORT", bookURL, multiget, [3]string{"207", "207", "207"}},
		{"REPORT sync-collection", "REPORT", bookURL, sync, [3]string{"207", "207", "207"}},
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
		store.AccessFull:    {"read,all,write,write-properties,write-content,bind,unbind", "read,write,write-content"},
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
			// The book and its object, listed together and asked for alone.
			if got := privs(bookURL, "1"); len(got) != 2 || got[0] != want[level][0] || got[1] != want[level][1] {
				t.Errorf("depth 1 privileges = %v, want %v", got, want[level])
			}
			if got := privs(bookURL+"a.vcf", "0"); len(got) != 1 || got[0] != want[level][1] {
				t.Errorf("object privileges = %v, want %q", got, want[level][1])
			}
			// The home is the caller's own and keeps the full set.
			if got := privs("/dav/addressbooks/alice/", "0"); len(got) != 1 || got[0] != want[store.AccessFull][0] {
				t.Errorf("home privileges = %v", got)
			}
			// Report responses carry the object privileges too.
			body := `<?xml version="1.0"?><CR:addressbook-multiget ` + nsDecl + `><D:prop><D:current-user-privilege-set/></D:prop><D:href>` + bookURL + `a.vcf</D:href></CR:addressbook-multiget>`
			req := httptest.NewRequest("REPORT", bookURL, strings.NewReader(body))
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

func TestReadOnlySyncCollection(t *testing.T) {
	h := newAccessHandler(t, store.AccessRead)
	body := `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	req := httptest.NewRequest("REPORT", bookURL, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := parseReply(t, rec)
	if len(r.hrefs) != 1 || r.hrefs[0] != bookURL+"a.vcf" {
		t.Errorf("hrefs = %v, want the one object", r.hrefs)
	}
	if r.must(t, 0, dav.GetETag).Text == "" {
		t.Error("no getetag")
	}
	if tok := r.root.Child(dav.SyncToken); tok == nil || !strings.HasPrefix(tok.Text, "urn:groupkit:sync:") {
		t.Errorf("sync-token = %v", tok)
	}
}
