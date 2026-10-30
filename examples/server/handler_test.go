// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testUser     = "demo"
	testPassword = "correct-horse-battery"
)

func testConfig(t *testing.T) config {
	t.Helper()
	return config{Addr: "127.0.0.1:0", DataDir: filepath.Join(t.TempDir(), "data"), User: testUser, Password: testPassword}
}

func newTestHandler(t *testing.T, cfg config) http.Handler {
	t.Helper()
	h, closeStore, err := newHandler(cfg)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	t.Cleanup(func() { _ = closeStore() })
	return h
}

type reqOpt func(*http.Request)

func asDemo() reqOpt {
	return func(r *http.Request) { r.SetBasicAuth(testUser, testPassword) }
}

func withHeader(k, v string) reqOpt {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

func do(t *testing.T, h http.Handler, method, target, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConfigValidation(t *testing.T) {
	dir := t.TempDir()
	good := config{Addr: "127.0.0.1:0", DataDir: dir, User: "demo", Password: "123456789012"}
	tests := []struct {
		name string
		edit func(*config)
		ok   bool
	}{
		{"valid", func(*config) {}, true},
		{"password of exactly 12", func(c *config) { c.Password = "abcdefghijkl" }, true},
		{"unicode user", func(c *config) { c.User = "zoë" }, true},
		{"user of 64 bytes", func(c *config) { c.User = strings.Repeat("u", 64) }, true},
		{"empty user", func(c *config) { c.User = "" }, false},
		{"user dot", func(c *config) { c.User = "." }, false},
		{"user dot dot", func(c *config) { c.User = ".." }, false},
		{"user with leading space", func(c *config) { c.User = " a" }, false},
		{"user with trailing space", func(c *config) { c.User = "a " }, false},
		{"11 multibyte characters", func(c *config) { c.Password = strings.Repeat("é", 11) }, false},
		{"12 multibyte characters", func(c *config) { c.Password = strings.Repeat("é", 12) }, true},
		{"address without port", func(c *config) { c.Addr = "127.0.0.1" }, false},
		{"address with empty port", func(c *config) { c.Addr = "127.0.0.1:" }, false},
		{"empty address", func(c *config) { c.Addr = "" }, false},
		{"user with colon", func(c *config) { c.User = "a:b" }, false},
		{"user with slash", func(c *config) { c.User = "a/b" }, false},
		{"user with newline", func(c *config) { c.User = "a\nb" }, false},
		{"user with NUL", func(c *config) { c.User = "a\x00b" }, false},
		{"user with DEL", func(c *config) { c.User = "a\x7fb" }, false},
		{"user of 65 bytes", func(c *config) { c.User = strings.Repeat("u", 65) }, false},
		{"empty password", func(c *config) { c.Password = "" }, false},
		{"password of 256 bytes", func(c *config) { c.Password = strings.Repeat("p", 256) }, true},
		{"password of 257 bytes", func(c *config) { c.Password = strings.Repeat("p", 257) }, false},
		{"password of 11", func(c *config) { c.Password = "abcdefghijk" }, false},
		{"empty data dir", func(c *config) { c.DataDir = "" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := good
			cfg.DataDir = filepath.Join(t.TempDir(), "d")
			tt.edit(&cfg)
			h, closeStore, err := newHandler(cfg)
			if tt.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if h == nil || closeStore == nil {
					t.Fatal("nil handler or close function")
				}
				_ = closeStore()
				return
			}
			if err == nil {
				_ = closeStore()
				t.Fatal("expected an error")
			}
			if h != nil || closeStore != nil {
				t.Error("handler or close function returned with an error")
			}
			if strings.Contains(err.Error(), cfg.Password) && cfg.Password != "" {
				t.Errorf("error repeats the password: %v", err)
			}
		})
	}
}

func TestMissingPasswordNamesVariable(t *testing.T) {
	cfg := testConfig(t)
	cfg.Password = ""
	_, _, err := newHandler(cfg)
	if err == nil || !strings.Contains(err.Error(), "GROUPKIT_PASSWORD") {
		t.Fatalf("error = %v, want it to name GROUPKIT_PASSWORD", err)
	}
}

// snapshot lists every path under dir with its size.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(p)
		b.WriteString(" ")
		b.WriteString(info.Mode().String())
		if !d.IsDir() {
			b.WriteString(" ")
			b.WriteString(strings.Repeat("x", int(info.Size()%1000)))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestRejectedRequestsNeverReachTheServer(t *testing.T) {
	cfg := testConfig(t)
	h := newTestHandler(t, cfg)
	before := snapshot(t, cfg.DataDir)

	creds := map[string]reqOpt{
		"none":           func(*http.Request) {},
		"wrong user":     func(r *http.Request) { r.SetBasicAuth("other", testPassword) },
		"wrong password": func(r *http.Request) { r.SetBasicAuth(testUser, "wrong-password-xx") },
		"empty password": func(r *http.Request) { r.SetBasicAuth(testUser, "") },
		"empty both":     func(r *http.Request) { r.SetBasicAuth("", "") },
		"prefix of pw":   func(r *http.Request) { r.SetBasicAuth(testUser, testPassword[:12]) },
		"pw plus more":   func(r *http.Request) { r.SetBasicAuth(testUser, testPassword+"x") },
		"bearer":         withHeader("Authorization", "Bearer "+testPassword),
		"not base64":     withHeader("Authorization", "Basic !!!notbase64"),
		"no colon":       withHeader("Authorization", "Basic ZGVtbw=="),
		"only scheme":    withHeader("Authorization", "Basic"),
	}
	requests := []struct{ method, target, body string }{
		{"MKCALENDAR", "/dav/calendars/demo/evil/", ""},
		{"MKCOL", "/dav/addressbooks/demo/evil/", ""},
		{"PUT", "/dav/calendars/demo/personal/x.ics", "BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n"},
		{"DELETE", "/dav/calendars/demo/personal/", ""},
		{"PROPFIND", "/dav/", ""},
		{"PROPFIND", "/dav", ""},
		{"GET", "/dav/principals/demo/", ""},
		{"HEAD", "/dav/", ""},
		{"OPTIONS", "/dav/", ""},
		{"OPTIONS", "/dav/calendars/demo/personal/", ""},
		{"GET", "/dav/nothing-here", ""},
	}
	for name, cred := range creds {
		for _, rq := range requests {
			w := do(t, h, rq.method, rq.target, rq.body, cred)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s %s: status %d, want 401", name, rq.method, rq.target, w.Code)
			}
			if got := w.Header().Get("WWW-Authenticate"); got != `Basic realm="groupkit", charset="UTF-8"` {
				t.Errorf("%s %s %s: WWW-Authenticate %q, want %q", name, rq.method, rq.target, got, challenge)
			}
			if strings.Contains(w.Body.String(), testPassword) {
				t.Errorf("%s: body repeats the password", name)
			}
		}
	}
	if after := snapshot(t, cfg.DataDir); after != before {
		t.Errorf("data directory changed by rejected requests:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestWellKnownWithoutCredentials(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	for _, p := range []string{"/.well-known/caldav", "/.well-known/carddav"} {
		for _, m := range []string{"GET", "PROPFIND"} {
			w := do(t, h, m, p, "")
			if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/dav/" {
				t.Errorf("%s %s: %d Location %q, want 301 /dav/", m, p, w.Code, w.Header().Get("Location"))
			}
		}
	}
}

func TestRootIsPublicAndPlain(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	w := do(t, h, "GET", "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := strings.TrimSpace(w.Body.String())
	if body == "" || strings.Contains(body, "\n") || strings.Contains(body, testUser) || strings.Contains(body, testPassword) {
		t.Errorf("body %q is not a one-line text", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type %q", ct)
	}
	if w := do(t, h, "GET", "/other", ""); w.Code != http.StatusNotFound {
		t.Errorf("GET /other: %d, want 404", w.Code)
	}
}

func TestDiscovery(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	for _, target := range []string{"/dav/", "/dav"} {
		w := do(t, h, "PROPFIND", target, `<?xml version="1.0"?><propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>`,
			asDemo(), withHeader("Depth", "0"))
		if w.Code != http.StatusMultiStatus {
			t.Fatalf("PROPFIND %s: %d %s", target, w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "/dav/principals/demo/") {
			t.Errorf("PROPFIND %s: no principal href in %s", target, w.Body)
		}
	}
	w := do(t, h, "PROPFIND", "/dav/principals/demo/", `<?xml version="1.0"?>
<propfind xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:card="urn:ietf:params:xml:ns:carddav">
<prop><c:calendar-home-set/><card:addressbook-home-set/></prop></propfind>`, asDemo(), withHeader("Depth", "0"))
	if w.Code != http.StatusMultiStatus {
		t.Fatalf("principal: %d %s", w.Code, w.Body)
	}
	for _, want := range []string{"/dav/calendars/demo/", "/dav/addressbooks/demo/"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("principal lacks %s: %s", want, w.Body)
		}
	}
	// The default collections exist.
	for _, target := range []string{"/dav/calendars/demo/personal/", "/dav/addressbooks/demo/contacts/"} {
		w := do(t, h, "PROPFIND", target, "", asDemo(), withHeader("Depth", "0"))
		if w.Code != http.StatusMultiStatus {
			t.Errorf("PROPFIND %s: %d", target, w.Code)
		}
	}
}

const event = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//example//EN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\n" +
	"TZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nTZNAME:CET\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n" +
	"TZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nTZNAME:CEST\r\nEND:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:weekly-1@example\r\nDTSTAMP:20260101T000000Z\r\n" +
	"DTSTART;TZID=Europe/Prague:20260105T100000\r\nDTEND;TZID=Europe/Prague:20260105T110000\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO\r\nSUMMARY:Weekly sync\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

const card = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:card-1@example\r\nFN:Ada Lovelace\r\nN:Lovelace;Ada;;;\r\n" +
	"EMAIL:ada@example.org\r\nEND:VCARD\r\n"

func TestCalendarRoundTrip(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	if w := do(t, h, "MKCALENDAR", "/dav/calendars/demo/work/", "", asDemo()); w.Code != http.StatusCreated {
		t.Fatalf("MKCALENDAR: %d %s", w.Code, w.Body)
	}
	w := do(t, h, "PUT", "/dav/calendars/demo/work/weekly.ics", event, asDemo(),
		withHeader("Content-Type", "text/calendar; charset=utf-8"))
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	// A window in March that holds a later occurrence of the series.
	query := `<?xml version="1.0"?>
<c:calendar-query xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
<prop><getetag/></prop>
<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
<c:time-range start="20260316T000000Z" end="20260317T000000Z"/>
</c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`
	w = do(t, h, "REPORT", "/dav/calendars/demo/work/", query, asDemo(), withHeader("Depth", "1"))
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "/dav/calendars/demo/work/weekly.ics") {
		t.Fatalf("calendar-query: %d %s", w.Code, w.Body)
	}
	// A window before the series starts finds nothing.
	empty := strings.NewReplacer("20260316", "20251201", "20260317", "20251202").Replace(query)
	w = do(t, h, "REPORT", "/dav/calendars/demo/work/", empty, asDemo(), withHeader("Depth", "1"))
	if w.Code != http.StatusMultiStatus || strings.Contains(w.Body.String(), "weekly.ics") {
		t.Fatalf("early window: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "GET", "/dav/calendars/demo/work/weekly.ics", "", asDemo())
	if w.Code != http.StatusOK || w.Body.String() != event {
		t.Fatalf("GET: %d, byte-identical %v", w.Code, w.Body.String() == event)
	}
}

func TestAddressBookRoundTrip(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	w := do(t, h, "PUT", "/dav/addressbooks/demo/contacts/ada.vcf", card, asDemo(),
		withHeader("Content-Type", "text/vcard; charset=utf-8"))
	if w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	query := `<?xml version="1.0"?>
<card:addressbook-query xmlns="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
<prop><getetag/></prop>
<card:filter><card:prop-filter name="FN"><card:text-match collation="i;unicode-casemap" match-type="contains">lovelace</card:text-match></card:prop-filter></card:filter>
</card:addressbook-query>`
	w = do(t, h, "REPORT", "/dav/addressbooks/demo/contacts/", query, asDemo(), withHeader("Depth", "1"))
	if w.Code != http.StatusMultiStatus || !strings.Contains(w.Body.String(), "/dav/addressbooks/demo/contacts/ada.vcf") {
		t.Fatalf("addressbook-query: %d %s", w.Code, w.Body)
	}
	w = do(t, h, "GET", "/dav/addressbooks/demo/contacts/ada.vcf", "", asDemo())
	if w.Code != http.StatusOK || w.Body.String() != card {
		t.Fatalf("GET: %d, byte-identical %v", w.Code, w.Body.String() == card)
	}
	// A new address book through extended MKCOL.
	mk := `<?xml version="1.0"?><mkcol xmlns="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav">
<set><prop><resourcetype><collection/><card:addressbook/></resourcetype><displayname>Friends</displayname></prop></set></mkcol>`
	if w := do(t, h, "MKCOL", "/dav/addressbooks/demo/friends/", mk, asDemo()); w.Code != http.StatusCreated {
		t.Fatalf("MKCOL: %d %s", w.Code, w.Body)
	}
}

func TestOtherUsersAreForbidden(t *testing.T) {
	cfg := testConfig(t)
	h := newTestHandler(t, cfg)
	before := snapshot(t, cfg.DataDir)
	tests := []struct{ method, target, body string }{
		{"PROPFIND", "/dav/calendars/mallory/", ""},
		{"PROPFIND", "/dav/calendars/mallory/personal/", ""},
		{"MKCALENDAR", "/dav/calendars/mallory/x/", ""},
		{"PUT", "/dav/calendars/mallory/personal/x.ics", event},
		{"GET", "/dav/calendars/mallory/personal/x.ics", ""},
		{"PROPFIND", "/dav/addressbooks/mallory/", ""},
		{"PUT", "/dav/addressbooks/mallory/contacts/x.vcf", card},
		{"DELETE", "/dav/addressbooks/mallory/contacts/", ""},
		{"PROPFIND", "/dav/calendars/Demo/", ""},
	}
	for _, tt := range tests {
		w := do(t, h, tt.method, tt.target, tt.body, asDemo(), withHeader("Depth", "0"))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", tt.method, tt.target, w.Code)
		}
	}
	// An encoded slash cannot smuggle another path in.
	if w := do(t, h, "PROPFIND", "/dav/calendars/demo%2Fx/", "", asDemo()); w.Code < 400 {
		t.Errorf("encoded slash: %d, want an error", w.Code)
	}
	if after := snapshot(t, cfg.DataDir); after != before {
		t.Errorf("data directory changed by forbidden requests")
	}
}

func TestPrincipalOfAnotherUser(t *testing.T) {
	h := newTestHandler(t, testConfig(t))
	for _, m := range []string{"PROPFIND", "OPTIONS", "GET"} {
		w := do(t, h, m, "/dav/principals/mallory/", "", asDemo(), withHeader("Depth", "0"))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s principal of mallory: %d, want 403", m, w.Code)
		}
	}
}

func TestRestartKeepsData(t *testing.T) {
	cfg := testConfig(t)
	h1, closeStore, err := newHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if w := do(t, h1, "PUT", "/dav/addressbooks/demo/contacts/ada.vcf", card, asDemo()); w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if w := do(t, h1, "PUT", "/dav/calendars/demo/personal/weekly.ics", event, asDemo()); w.Code != http.StatusCreated {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if err := closeStore(); err != nil {
		t.Fatal(err)
	}
	h2 := newTestHandler(t, cfg)
	for target, want := range map[string]string{
		"/dav/addressbooks/demo/contacts/ada.vcf": card,
		"/dav/calendars/demo/personal/weekly.ics": event,
	} {
		w := do(t, h2, "GET", target, "", asDemo())
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("after restart GET %s: %d", target, w.Code)
		}
	}
}

func TestOneProcessPerDirectory(t *testing.T) {
	cfg := testConfig(t)
	_ = newTestHandler(t, cfg)
	if _, _, err := newHandler(cfg); err == nil {
		t.Fatal("second newHandler on the same directory succeeded")
	}
}

func TestPasswordNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	old := logOutput
	logOutput = &logs
	defer func() { logOutput = old }()

	h := newTestHandler(t, testConfig(t))
	var bodies strings.Builder
	for _, o := range []reqOpt{asDemo(), withHeader("Authorization", "Basic !!"), func(r *http.Request) { r.SetBasicAuth(testUser, "wrong-password-xx") }} {
		for _, target := range []string{"/", "/dav/", "/dav/calendars/mallory/", "/.well-known/caldav"} {
			w := do(t, h, "PROPFIND", target, "", o)
			bodies.WriteString(w.Body.String())
			for k, v := range w.Header() {
				bodies.WriteString(k + strings.Join(v, ","))
			}
		}
	}
	if strings.Contains(logs.String(), testPassword) || strings.Contains(bodies.String(), testPassword) {
		t.Error("the password appears in a response or a log")
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	env := func(string) string { return "" }
	var out bytes.Buffer
	if code := run(ctx, []string{"-data", t.TempDir()}, env, &out); code != 2 {
		t.Errorf("no password: exit %d, want 2", code)
	}
	if !strings.Contains(out.String(), "GROUPKIT_PASSWORD") {
		t.Errorf("message does not name the variable: %q", out.String())
	}
	out.Reset()
	if code := run(ctx, []string{"-h"}, env, &out); code != 0 {
		t.Errorf("-h: exit %d, want 0", code)
	}
	for _, want := range []string{"GROUPKIT_PASSWORD", "TLS", "reverse proxy"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help lacks %q", want)
		}
	}
	out.Reset()
	good := func(string) string { return testPassword }
	if code := run(ctx, []string{"-password", "another-long-password"}, good, &out); code != 2 ||
		!strings.Contains(out.String(), "flag provided but not defined") {
		t.Errorf("a password flag must not exist: exit %d, %q", code, out.String())
	}
}

func TestDefaults(t *testing.T) {
	var cfg config
	fs := newFlagSet(&cfg, io.Discard)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:8080" || cfg.DataDir != "./data" || cfg.User != "demo" {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestBadAddressCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	env := func(string) string { return testPassword }
	var out bytes.Buffer
	if code := run(context.Background(), []string{"-data", dir, "-addr", "nonsense"}, env, &out); code != 2 {
		t.Errorf("exit %d, want 2: %s", code, out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the data directory was created: %v", err)
	}
}

// syncBuffer is a bytes.Buffer safe for use by the server goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRunServesAndNeverLogsPasswords(t *testing.T) {
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := func(string) string { return testPassword }
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-data", filepath.Join(t.TempDir(), "d"), "-addr", "127.0.0.1:0"}, env, &logs)
	}()

	var addr string
	re := regexp.MustCompile(`listening addr=(\S+)`)
	for i := 0; i < 200 && addr == ""; i++ {
		if m := re.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
		}
		time.Sleep(25 * time.Millisecond)
	}
	if addr == "" {
		t.Fatalf("server did not start: %s", logs.String())
	}
	wrong := "wrong-submitted-password"
	for pass, want := range map[string]int{testPassword: 207, wrong: 401} {
		req, _ := http.NewRequest("PROPFIND", "http://"+addr+"/dav/", nil)
		req.SetBasicAuth(testUser, pass)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("status %d, want %d", resp.StatusCode, want)
		}
	}
	// OPTIONS * must not be answered without credentials.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(conn, "OPTIONS * HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	line, _ := bufio.NewReader(conn).ReadString('\n')
	_ = conn.Close()
	if strings.Contains(line, " 2") {
		t.Errorf("OPTIONS * answered: %q", line)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d, logs: %s", code, logs.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no shutdown")
	}
	for _, secret := range []string{testPassword, wrong} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("logs contain a password: %s", logs.String())
		}
	}
}

func TestConstantTimeEqual(t *testing.T) {
	long := strings.Repeat("a", 256)
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "secret-value", "secret-value", true},
		{"different", "secret-value", "secret-valuf", false},
		{"prefix", "abc", "abcd", false},
		{"padded equal, different length", "abc", "abc\x00", false},
		{"both empty", "", "", true},
		{"empty and zero byte", "", "\x00", false},
		{"256 bytes equal", long, long, true},
		{"256 against 257", long, long + "a", false},
		{"257 against 257", long + "a", long + "a", false},
		{"unicode equal", "zoë-ключ-鍵", "zoë-ключ-鍵", true},
		{"unicode different", "zoë-ключ-鍵", "zoe-ключ-鍵", false},
	}
	for _, tt := range tests {
		if got := constantTimeEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("%s: constantTimeEqual = %v, want %v", tt.name, got, tt.want)
		}
		if got := constantTimeEqual(tt.b, tt.a); got != tt.want {
			t.Errorf("%s (swapped): constantTimeEqual = %v, want %v", tt.name, got, tt.want)
		}
	}
}
