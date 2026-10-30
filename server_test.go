// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

const userHeader = "X-Test-User"

const testChallenge = `Basic realm="groupkit"`

// testConfig returns a valid Config whose users come from userHeader. A user
// may only reach its own principal, address books and calendars.
func testConfig(prefix string) groupkit.Config {
	return groupkit.Config{
		Store:  memory.New(),
		Prefix: prefix,
		CurrentUser: func(r *http.Request) (string, error) {
			u := r.Header.Get(userHeader)
			if u == "" {
				return "", groupkit.ErrUnauthenticated
			}
			return u, nil
		},
		Authorize: func(r *http.Request, urlUser string) (string, error) {
			u := r.Header.Get(userHeader)
			switch {
			case u == "":
				return "", groupkit.ErrUnauthenticated
			case u != urlUser:
				return "", groupkit.ErrForbidden
			}
			return u, nil
		},
		Challenge: testChallenge,
	}
}

func newServer(t *testing.T, cfg groupkit.Config) *groupkit.Server {
	t.Helper()
	s, err := groupkit.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

// do sends one request to h as user ("" sends no user header).
func do(t *testing.T, h http.Handler, method, target, user string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if user != "" {
		r.Header.Set(userHeader, user)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type multistatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				Items []struct {
					XMLName xml.Name
					Hrefs   []string `xml:"href"`
					Text    string   `xml:",chardata"`
					Inner   string   `xml:",innerxml"`
				} `xml:",any"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func parseMS(t *testing.T, w *httptest.ResponseRecorder) multistatus {
	t.Helper()
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207; body %s", w.Code, w.Body)
	}
	var ms multistatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("bad multistatus: %v\n%s", err, w.Body)
	}
	return ms
}

func (ms multistatus) hrefs() []string {
	var out []string
	for _, r := range ms.Responses {
		out = append(out, r.Href)
	}
	return out
}

// prop returns the hrefs, text and inner XML of the named property of the
// first response, and the status of its propstat.
func (ms multistatus) prop(local string) (hrefs []string, text, inner, status string) {
	for _, ps := range ms.Responses[0].Propstats {
		for _, it := range ps.Prop.Items {
			if it.XMLName.Local == local {
				return it.Hrefs, strings.TrimSpace(it.Text), it.Inner, ps.Status
			}
		}
	}
	return nil, "", "", ""
}

const propfindBody = `<?xml version="1.0"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

func TestNewServerErrors(t *testing.T) {
	tests := map[string]func(*groupkit.Config){
		"nil store":         func(c *groupkit.Config) { c.Store = nil },
		"nil current user":  func(c *groupkit.Config) { c.CurrentUser = nil },
		"nil authorize":     func(c *groupkit.Config) { c.Authorize = nil },
		"no leading slash":  func(c *groupkit.Config) { c.Prefix = "dav" },
		"trailing slash":    func(c *groupkit.Config) { c.Prefix = "/dav/" },
		"slash only":        func(c *groupkit.Config) { c.Prefix = "/" },
		"host-like prefix":  func(c *groupkit.Config) { c.Prefix = "//evil.com" },
		"prefix CRLF":       func(c *groupkit.Config) { c.Prefix = "/a\r\nb" },
		"prefix space":      func(c *groupkit.Config) { c.Prefix = "/my dav" },
		"prefix dot":        func(c *groupkit.Config) { c.Prefix = "/./dav" },
		"prefix escape":     func(c *groupkit.Config) { c.Prefix = "/d%61v" },
		"prefix query":      func(c *groupkit.Config) { c.Prefix = "/dav?x" },
		"prefix unicode":    func(c *groupkit.Config) { c.Prefix = "/d\u00e1v" },
		"negative size":     func(c *groupkit.Config) { c.MaxObjectSize = -1 },
		"challenge LF":      func(c *groupkit.Config) { c.Challenge = "Basic realm=\"a\"\nSet-Cookie: x=1" },
		"challenge CR":      func(c *groupkit.Config) { c.Challenge = "Basic\rrealm" },
		"challenge CRLF":    func(c *groupkit.Config) { c.Challenge = "Basic\r\nX: y" },
		"challenge NUL ctl": func(c *groupkit.Config) { c.Challenge = "Basic\x00" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig("/dav")
			mutate(&cfg)
			if s, err := groupkit.NewServer(cfg); err == nil || s != nil {
				t.Fatalf("NewServer = %v, %v; want an error", s, err)
			}
		})
	}
	for _, prefix := range []string{"", "/dav", "/a/b", "/dav/sub"} {
		if _, err := groupkit.NewServer(testConfig(prefix)); err != nil {
			t.Errorf("prefix %q: %v", prefix, err)
		}
	}
}

func TestErrorAliases(t *testing.T) {
	if groupkit.ErrUnauthenticated == nil || groupkit.ErrForbidden == nil || groupkit.ErrUserNotFound == nil {
		t.Fatal("nil sentinel")
	}
}

func TestRootPropfind(t *testing.T) {
	for _, prefix := range []string{"/dav", ""} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			h := newServer(t, testConfig(prefix)).Handler()
			for _, target := range []string{prefix + "/", prefix} {
				if target == "" {
					continue
				}
				w := do(t, h, "PROPFIND", target, "alice", map[string]string{"Depth": "0"}, propfindBody)
				ms := parseMS(t, w)
				if got, want := ms.hrefs(), []string{prefix + "/"}; len(got) != 1 || got[0] != want[0] {
					t.Errorf("%q hrefs = %v, want %v", target, got, want)
				}
				hrefs, _, _, st := ms.prop("current-user-principal")
				if len(hrefs) != 1 || hrefs[0] != prefix+"/principals/alice/" || !strings.Contains(st, "200") {
					t.Errorf("current-user-principal = %v (%s)", hrefs, st)
				}
				if _, _, inner, _ := ms.prop("resourcetype"); !strings.Contains(inner, "collection") {
					t.Errorf("resourcetype = %q", inner)
				}
			}
		})
	}
}

func TestRootDepth1ListsPrincipal(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/", "alice", map[string]string{"Depth": "1"}, propfindBody))
	got := ms.hrefs()
	if len(got) != 2 || got[0] != "/dav/" || got[1] != "/dav/principals/alice/" {
		t.Fatalf("hrefs = %v", got)
	}
	// No Depth header behaves like Depth 1.
	ms = parseMS(t, do(t, h, "PROPFIND", "/dav/", "alice", nil, ""))
	if len(ms.hrefs()) != 2 {
		t.Fatalf("hrefs = %v", ms.hrefs())
	}
}

func TestPrincipalPropfind(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	user := "Zoë Müller"
	esc := "Zo%C3%AB%20M%C3%BCller"
	for _, target := range []string{"/dav/principals/" + esc + "/", "/dav/principals/" + esc} {
		ms := parseMS(t, do(t, h, "PROPFIND", target, user, map[string]string{"Depth": "0"}, propfindBody))
		if got := ms.hrefs(); len(got) != 1 || got[0] != "/dav/principals/"+esc+"/" {
			t.Fatalf("hrefs = %v", got)
		}
		check := func(name, want string) {
			t.Helper()
			hrefs, _, _, st := ms.prop(name)
			if len(hrefs) != 1 || hrefs[0] != want || !strings.Contains(st, "200") {
				t.Errorf("%s = %v (%s), want %s", name, hrefs, st, want)
			}
		}
		check("addressbook-home-set", "/dav/addressbooks/"+esc+"/")
		check("principal-URL", "/dav/principals/"+esc+"/")
		check("current-user-principal", "/dav/principals/"+esc+"/")
		check("owner", "/dav/principals/"+esc+"/")
		if _, text, _, _ := ms.prop("displayname"); text != user {
			t.Errorf("displayname = %q", text)
		}
		if _, _, inner, _ := ms.prop("resourcetype"); !strings.Contains(inner, "collection") || !strings.Contains(inner, "principal") {
			t.Errorf("resourcetype = %q", inner)
		}
		if _, _, inner, _ := ms.prop("supported-report-set"); strings.TrimSpace(inner) != "" {
			t.Errorf("supported-report-set = %q", inner)
		}
		if _, _, inner, _ := ms.prop("current-user-privilege-set"); !strings.Contains(inner, "read") {
			t.Errorf("privileges = %q", inner)
		}
	}
	// Depth 1 lists nothing below the principal.
	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/principals/"+esc+"/", user, map[string]string{"Depth": "1"}, propfindBody))
	if len(ms.hrefs()) != 1 {
		t.Errorf("hrefs = %v", ms.hrefs())
	}
}

func TestUnknownPropertyIs404(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	body := `<D:propfind xmlns:D="DAV:" xmlns:X="urn:x"><D:prop><X:nope/><D:resourcetype/></D:prop></D:propfind>`
	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/principals/alice/", "alice", map[string]string{"Depth": "0"}, body))
	var found404 bool
	for _, ps := range ms.Responses[0].Propstats {
		if strings.Contains(ps.Status, "404") {
			found404 = true
		}
	}
	if !found404 {
		t.Errorf("no 404 propstat: %+v", ms)
	}
}

func TestPrincipalOfAnotherUserIsForbidden(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	other := do(t, h, "PROPFIND", "/dav/principals/bob/", "alice", nil, propfindBody)
	unknown := do(t, h, "PROPFIND", "/dav/principals/nobody-at-all/", "alice", nil, propfindBody)
	if other.Code != http.StatusForbidden || unknown.Code != http.StatusForbidden {
		t.Fatalf("status = %d, %d; want 403", other.Code, unknown.Code)
	}
	if other.Body.String() != unknown.Body.String() {
		t.Errorf("bodies differ:\n%s\n%s", other.Body, unknown.Body)
	}
	// Case matters: the user must match exactly.
	if w := do(t, h, "PROPFIND", "/dav/principals/Alice/", "alice", nil, propfindBody); w.Code != http.StatusForbidden {
		t.Errorf("case-different principal = %d", w.Code)
	}
}

func TestPrincipalBadPaths(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	for target, want := range map[string]int{
		"/dav/principals/":              http.StatusNotFound,
		"/dav/principals":               http.StatusNotFound,
		"/dav/principals/alice/extra":   http.StatusNotFound,
		"/dav/principals/alice/x/y":     http.StatusNotFound,
		"/dav/principals/%2e%2e/":       http.StatusBadRequest,
		"/dav/principals/a%2Fb/":        http.StatusBadRequest,
		"/dav/principals/a%00b/":        http.StatusBadRequest,
		"/dav/nothing":                  http.StatusNotFound,
		"/dav/":                         http.StatusMultiStatus,
		"/davx":                         http.StatusNotFound,
		"/other/":                       http.StatusNotFound,
		"/dav/addressbooksx/alice/":     http.StatusNotFound,
		"/dav/principalsx/alice/":       http.StatusNotFound,
		"/dav/principals/alice/..%2F..": http.StatusNotFound,
	} {
		if w := do(t, h, "PROPFIND", target, "alice", nil, propfindBody); w.Code != want {
			t.Errorf("%s = %d, want %d", target, w.Code, want)
		}
	}
}

func TestOptionsAndMethods(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	for _, target := range []string{"/dav/", "/dav", "/dav/principals/alice/"} {
		w := do(t, h, "OPTIONS", target, "alice", nil, "")
		if w.Code != http.StatusOK {
			t.Errorf("OPTIONS %s = %d", target, w.Code)
		}
		if got := w.Header().Get("DAV"); got != "1, 3, addressbook, extended-mkcol, calendar-access" {
			t.Errorf("DAV = %q", got)
		}
		if got := w.Header().Get("Allow"); got != "OPTIONS, PROPFIND" {
			t.Errorf("Allow = %q", got)
		}
		for _, m := range []string{"GET", "PUT", "DELETE", "MKCOL", "REPORT", "PROPPATCH"} {
			w := do(t, h, m, target, "alice", nil, "")
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "OPTIONS, PROPFIND" {
				t.Errorf("%s %s = %d (Allow %q)", m, target, w.Code, w.Header().Get("Allow"))
			}
		}
	}
	if w := do(t, h, "OPTIONS", "*", "", nil, ""); w.Code != http.StatusOK || w.Header().Get("DAV") == "" {
		t.Errorf("OPTIONS * = %d", w.Code)
	}
}

func TestUnauthenticated(t *testing.T) {
	targets := []string{
		"/dav/", "/dav/principals/alice/", "/dav/addressbooks/alice/",
		"/dav/addressbooks/alice/contacts/", "/dav/addressbooks/alice/contacts/x.vcf",
	}
	for _, tc := range []struct {
		name      string
		challenge string
	}{{"with challenge", testChallenge}, {"without challenge", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig("/dav")
			cfg.Challenge = tc.challenge
			h := newServer(t, cfg).Handler()
			for _, target := range targets {
				w := do(t, h, "PROPFIND", target, "", nil, propfindBody)
				if w.Code != http.StatusUnauthorized {
					t.Fatalf("%s = %d, want 401", target, w.Code)
				}
				vals := w.Header().Values("WWW-Authenticate")
				if tc.challenge == "" && len(vals) != 0 {
					t.Errorf("%s: unexpected WWW-Authenticate %v", target, vals)
				}
				if tc.challenge != "" && (len(vals) != 1 || vals[0] != tc.challenge) {
					t.Errorf("%s: WWW-Authenticate = %v", target, vals)
				}
			}
		})
	}
}

func TestChallengeOnlyOn401(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	if w := do(t, h, "PROPFIND", "/dav/addressbooks/alice/", "alice", nil, propfindBody); w.Header().Get("WWW-Authenticate") != "" {
		t.Error("challenge on a 207")
	}
	if w := do(t, h, "PROPFIND", "/dav/addressbooks/bob/", "alice", nil, propfindBody); w.Code != 403 || w.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("403 case = %d %v", w.Code, w.Header())
	}
	if w := do(t, h, "PROPFIND", "/dav/principals/bob/", "alice", nil, propfindBody); w.Code != 403 || w.Header().Get("WWW-Authenticate") != "" {
		t.Errorf("403 principal = %d %v", w.Code, w.Header())
	}
}

func TestCurrentUserErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{groupkit.ErrUnauthenticated, 401},
		{errors.Join(errors.New("ctx"), groupkit.ErrUnauthenticated), 401},
		{groupkit.ErrForbidden, 403},
		{groupkit.ErrUserNotFound, 404},
		{errors.New("database is on fire: secret-dsn"), 500},
	} {
		cfg := testConfig("/dav")
		err := tc.err
		cfg.CurrentUser = func(*http.Request) (string, error) { return "", err }
		h := newServer(t, cfg).Handler()
		for _, target := range []string{"/dav/", "/dav/principals/alice/"} {
			w := do(t, h, "PROPFIND", target, "alice", nil, propfindBody)
			if w.Code != tc.status {
				t.Errorf("%v %s = %d, want %d", tc.err, target, w.Code, tc.status)
			}
			if strings.Contains(w.Body.String(), "secret-dsn") {
				t.Errorf("internal detail leaked: %s", w.Body)
			}
			if got := w.Header().Get("WWW-Authenticate"); (tc.status == 401) != (got != "") {
				t.Errorf("%v: WWW-Authenticate = %q", tc.err, got)
			}
		}
	}
}

func TestEmptyCurrentUserIs500(t *testing.T) {
	cfg := testConfig("/dav")
	cfg.CurrentUser = func(*http.Request) (string, error) { return "", nil }
	h := newServer(t, cfg).Handler()
	for _, target := range []string{"/dav/", "/dav/principals/alice/"} {
		if w := do(t, h, "PROPFIND", target, "alice", nil, propfindBody); w.Code != 500 {
			t.Errorf("%s = %d, want 500", target, w.Code)
		}
	}
}

func TestAuthorizeErrorsReachAddressBooks(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{groupkit.ErrForbidden, 403}, {groupkit.ErrUserNotFound, 404}, {errors.New("boom"), 500},
	} {
		cfg := testConfig("/dav")
		err := tc.err
		cfg.Authorize = func(*http.Request, string) (string, error) { return "", err }
		h := newServer(t, cfg).Handler()
		if w := do(t, h, "PROPFIND", "/dav/addressbooks/alice/", "alice", nil, propfindBody); w.Code != tc.status {
			t.Errorf("%v = %d, want %d", tc.err, w.Code, tc.status)
		}
	}
}

func TestWellKnown(t *testing.T) {
	for _, prefix := range []string{"/dav", ""} {
		t.Run("prefix "+prefix, func(t *testing.T) {
			var called atomic.Int32
			cfg := testConfig(prefix)
			cfg.CurrentUser = func(*http.Request) (string, error) { called.Add(1); return "x", nil }
			cfg.Authorize = func(*http.Request, string) (string, error) { called.Add(1); return "x", nil }
			wk := newServer(t, cfg).WellKnown()
			for _, m := range []string{"GET", "HEAD", "PROPFIND"} {
				for _, target := range []string{"/.well-known/carddav", "/.well-known/caldav", "/anything"} {
					w := do(t, wk, m, target, "", nil, propfindBody)
					if w.Code != http.StatusMovedPermanently {
						t.Errorf("%s %s = %d", m, target, w.Code)
					}
					if got := w.Header().Get("Location"); got != prefix+"/" {
						t.Errorf("Location = %q, want %q", got, prefix+"/")
					}
				}
			}
			w := do(t, wk, "OPTIONS", "/.well-known/carddav", "", nil, "")
			if w.Code != http.StatusOK || w.Header().Get("Allow") == "" {
				t.Errorf("OPTIONS = %d %q", w.Code, w.Header().Get("Allow"))
			}
			for _, m := range []string{"PUT", "POST", "DELETE", "MKCOL", "REPORT"} {
				w := do(t, wk, m, "/.well-known/carddav", "", nil, "")
				if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") == "" {
					t.Errorf("%s = %d (Allow %q)", m, w.Code, w.Header().Get("Allow"))
				}
			}
			if n := called.Load(); n != 0 {
				t.Errorf("CurrentUser/Authorize called %d times", n)
			}
		})
	}
}

func TestEnsureDefaultCollections(t *testing.T) {
	ctx := t.Context()
	books := func(st store.Store, owner string) []store.Collection {
		t.Helper()
		cs, err := st.ListCollections(ctx, owner, store.KindAddressBook)
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}

	t.Run("twice makes one book", func(t *testing.T) {
		st := memory.New()
		for range 2 {
			if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
				t.Fatal(err)
			}
		}
		cs := books(st, "alice")
		if len(cs) != 1 || cs[0].Ref.Name != "contacts" || cs[0].Props.DisplayName != "Contacts" {
			t.Fatalf("books = %+v", cs)
		}
		if got := books(st, "bob"); len(got) != 0 {
			t.Errorf("other owner got %v", got)
		}
	})

	t.Run("existing differently named book", func(t *testing.T) {
		st := memory.New()
		ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "work"}
		if _, err := st.CreateCollection(ctx, ref, store.Props{DisplayName: "Work"}); err != nil {
			t.Fatal(err)
		}
		if err := groupkit.EnsureDefaultCollections(ctx, st, "alice"); err != nil {
			t.Fatal(err)
		}
		if cs := books(st, "alice"); len(cs) != 1 || cs[0].Ref.Name != "work" {
			t.Fatalf("books = %+v", cs)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		st := memory.New()
		errs := make(chan error, 16)
		for range cap(errs) {
			go func() { errs <- groupkit.EnsureDefaultCollections(ctx, st, "alice") }()
		}
		for range cap(errs) {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		if cs := books(st, "alice"); len(cs) != 1 {
			t.Fatalf("books = %+v", cs)
		}
	})

	t.Run("empty owner", func(t *testing.T) {
		if err := groupkit.EnsureDefaultCollections(ctx, memory.New(), ""); err == nil {
			t.Fatal("want an error")
		}
	})
}

const contactA = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:a-1\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\nEND:VCARD\r\n"

func TestFullWalk(t *testing.T) {
	h := newServer(t, testConfig("/dav")).Handler()
	user := "alice"

	// Root: discover the principal.
	ms := parseMS(t, do(t, h, "PROPFIND", "/dav/", user, map[string]string{"Depth": "0"}, propfindBody))
	principal, _, _, _ := ms.prop("current-user-principal")
	if len(principal) != 1 {
		t.Fatalf("no principal in %+v", ms)
	}
	// Principal: discover the home set.
	ms = parseMS(t, do(t, h, "PROPFIND", principal[0], user, map[string]string{"Depth": "0"}, propfindBody))
	home, _, _, _ := ms.prop("addressbook-home-set")
	if len(home) != 1 {
		t.Fatalf("no home set in %+v", ms)
	}
	book := home[0] + "work/"

	// An empty home, then create a book.
	ms = parseMS(t, do(t, h, "PROPFIND", home[0], user, map[string]string{"Depth": "1"}, propfindBody))
	if len(ms.hrefs()) != 1 {
		t.Fatalf("home hrefs = %v", ms.hrefs())
	}
	if w := do(t, h, "MKCOL", book, user, nil, ""); w.Code != http.StatusCreated {
		t.Fatalf("MKCOL = %d %s", w.Code, w.Body)
	}
	ms = parseMS(t, do(t, h, "PROPFIND", home[0], user, map[string]string{"Depth": "1"}, propfindBody))
	if got := ms.hrefs(); len(got) != 2 || got[1] != book {
		t.Fatalf("home hrefs = %v", got)
	}

	// Put a contact.
	obj := book + "ada.vcf"
	w := do(t, h, "PUT", obj, user, map[string]string{"Content-Type": "text/vcard"}, contactA)
	if w.Code != http.StatusCreated || w.Header().Get("ETag") == "" {
		t.Fatalf("PUT = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := do(t, h, "GET", obj, user, nil, ""); w.Code != 200 || w.Body.String() != contactA {
		t.Fatalf("GET = %d %q", w.Code, w.Body)
	}

	// Query.
	query := `<?xml version="1.0"?><C:addressbook-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">
<D:prop><D:getetag/><C:address-data/></D:prop>
<C:filter><C:prop-filter name="FN"><C:text-match collation="i;unicode-casemap" match-type="contains">lovelace</C:text-match></C:prop-filter></C:filter>
</C:addressbook-query>`
	w = do(t, h, "REPORT", book, user, map[string]string{"Depth": "1"}, query)
	ms = parseMS(t, w)
	if got := ms.hrefs(); len(got) != 1 || got[0] != obj {
		t.Fatalf("query hrefs = %v", got)
	}
	if !strings.Contains(w.Body.String(), "Ada Lovelace") {
		t.Errorf("no address data: %s", w.Body)
	}

	// Sync: initial, then after a change.
	syncBody := func(token string) string {
		return `<?xml version="1.0"?><D:sync-collection xmlns:D="DAV:"><D:sync-token>` + token +
			`</D:sync-token><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`
	}
	w = do(t, h, "REPORT", book, user, nil, syncBody(""))
	ms = parseMS(t, w)
	if got := ms.hrefs(); len(got) != 1 || got[0] != obj {
		t.Fatalf("initial sync hrefs = %v", got)
	}
	var tok struct {
		Token string `xml:"sync-token"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &tok); err != nil || tok.Token == "" {
		t.Fatalf("no sync token: %v\n%s", err, w.Body)
	}
	b := strings.Replace(contactA, "Ada Lovelace", "Ada King", 1)
	if w := do(t, h, "PUT", book+"second.vcf", user, nil, strings.Replace(b, "a-1", "a-2", 1)); w.Code != http.StatusCreated {
		t.Fatalf("PUT 2 = %d %s", w.Code, w.Body)
	}
	ms = parseMS(t, do(t, h, "REPORT", book, user, nil, syncBody(tok.Token)))
	if got := ms.hrefs(); len(got) != 1 || got[0] != book+"second.vcf" {
		t.Fatalf("incremental sync hrefs = %v", got)
	}

	// Another user cannot see any of it.
	if w := do(t, h, "GET", obj, "bob", nil, ""); w.Code != http.StatusForbidden {
		t.Errorf("bob GET = %d", w.Code)
	}

	// Delete the object, then the book.
	if w := do(t, h, "DELETE", obj, user, nil, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE object = %d", w.Code)
	}
	if w := do(t, h, "GET", obj, user, nil, ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d", w.Code)
	}
	if w := do(t, h, "DELETE", book, user, nil, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE book = %d", w.Code)
	}
	ms = parseMS(t, do(t, h, "PROPFIND", home[0], user, map[string]string{"Depth": "1"}, propfindBody))
	if len(ms.hrefs()) != 1 {
		t.Fatalf("home hrefs after delete = %v", ms.hrefs())
	}
}

func TestServerOverRealConnection(t *testing.T) {
	cfg := testConfig("/dav")
	srv := newServer(t, cfg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	send := func(method, path, user, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(userHeader, user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	mustStatus := func(resp *http.Response, want int) {
		t.Helper()
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("status = %d, want %d", resp.StatusCode, want)
		}
	}
	mustStatus(send("MKCOL", "/dav/addressbooks/alice/big/", "alice", ""), 201)
	const n = 50
	for i := range n {
		vc := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:u" + string(rune('a'+i%26)) + strings.Repeat("x", i) + "\r\nFN:N\r\nN:N;;;;\r\nEND:VCARD\r\n"
		name := "/dav/addressbooks/alice/big/c" + strconv.Itoa(i) + ".vcf"
		mustStatus(send("PUT", name, "alice", vc), 201)
	}
	resp := send("PROPFIND", "/dav/addressbooks/alice/big/", "alice", propfindBody)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "<D:response>") + strings.Count(string(body), "<response"); got < n {
		t.Errorf("responses = %d, want >= %d", got, n)
	}
	// A 401 over a real connection carries the challenge.
	req, _ := http.NewRequestWithContext(t.Context(), "PROPFIND", ts.URL+"/dav/addressbooks/alice/", nil)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r2.Body.Close() }()
	if r2.StatusCode != 401 || r2.Header.Get("WWW-Authenticate") != testChallenge {
		t.Errorf("401 case = %d %v", r2.StatusCode, r2.Header)
	}
}

func TestAuthorizeAdapterMapsOwner(t *testing.T) {
	cfg := testConfig("/dav")
	st := cfg.Store
	var seen string
	cfg.Authorize = func(r *http.Request, urlUser string) (string, error) {
		seen = urlUser
		return "u-42", nil
	}
	h := newServer(t, cfg).Handler()
	if w := do(t, h, "MKCOL", "/dav/addressbooks/Zo%C3%AB/b/", "", nil, ""); w.Code != http.StatusCreated {
		t.Fatalf("MKCOL = %d %s", w.Code, w.Body)
	}
	if seen != "Zoë" {
		t.Errorf("Authorize got %q, want the decoded name", seen)
	}
	for owner, want := range map[string]int{"u-42": 1, "Zoë": 0} {
		cs, err := st.ListCollections(t.Context(), owner, store.KindAddressBook)
		if err != nil || len(cs) != want {
			t.Errorf("owner %q: %d books, %v; want %d", owner, len(cs), err, want)
		}
	}
}

// racyStore reports no collections and then ErrExists on every create, as a
// store does when another caller wins the race.
type racyStore struct{ store.Store }

func (racyStore) ListCollections(context.Context, string, store.Kind) ([]store.Collection, error) {
	return nil, nil
}

func (racyStore) CreateCollection(context.Context, store.CollectionRef, store.Props) (store.Collection, error) {
	return store.Collection{}, store.ErrExists
}

func TestEnsureDefaultCollectionsLosingTheRace(t *testing.T) {
	if err := groupkit.EnsureDefaultCollections(t.Context(), racyStore{}, "alice"); err != nil {
		t.Fatalf("ErrExists must be ignored: %v", err)
	}
}

func TestEnsureDefaultCollectionsNilStore(t *testing.T) {
	if err := groupkit.EnsureDefaultCollections(t.Context(), nil, "alice"); err == nil {
		t.Fatal("want an error")
	}
}

// NewServer checks the prefix itself, before any handler does, so the error
// names the server's own option.
func TestNewServerChecksPrefixItself(t *testing.T) {
	for _, prefix := range []string{"dav", "/dav/", "/", "//evil.com", "/a b"} {
		_, err := groupkit.NewServer(testConfig(prefix))
		if err == nil || !strings.HasPrefix(err.Error(), "groupkit: invalid Config.Prefix") {
			t.Errorf("prefix %q: err = %v", prefix, err)
		}
	}
}

func TestNewServerNilStoreMessage(t *testing.T) {
	cfg := testConfig("/dav")
	cfg.Store = nil
	_, err := groupkit.NewServer(cfg)
	if err == nil || err.Error() != "groupkit: Config.Store is nil" {
		t.Errorf("err = %v", err)
	}
}
