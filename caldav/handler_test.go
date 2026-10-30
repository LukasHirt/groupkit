// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/access"
	"github.com/LukasHirt/groupkit/store"
)

// panicStore fails the test run if the handler touches the store.
type panicStore struct{ store.Store }

func resolveAs(owner string) func(*http.Request, string) (string, error) {
	return func(*http.Request, string) (string, error) { return owner, nil }
}

func newTestHandler(t *testing.T, prefix string, resolve func(*http.Request, string) (string, error)) *Handler {
	t.Helper()
	h, err := New(Config{Store: panicStore{}, Prefix: prefix, Resolve: resolve})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNew(t *testing.T) {
	st := panicStore{}
	res := resolveAs("o")
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"ok", Config{Store: st, Prefix: "/dav", Resolve: res}, ""},
		{"empty prefix", Config{Store: st, Resolve: res}, ""},
		{"nil store", Config{Prefix: "/dav", Resolve: res}, "Store is nil"},
		{"nil resolve", Config{Store: st, Prefix: "/dav"}, "Resolve is nil"},
		{"no leading slash", Config{Store: st, Prefix: "dav", Resolve: res}, "start with a slash"},
		{"trailing slash", Config{Store: st, Prefix: "/dav/", Resolve: res}, "end with a slash"},
		{"only slash", Config{Store: st, Prefix: "/", Resolve: res}, "end with a slash"},
		{"sub prefix", Config{Store: st, Prefix: "/dav/sub", Resolve: res}, ""},
		{"host-like prefix", Config{Store: st, Prefix: "//evil.com", Resolve: res}, "two slashes"},
		{"control characters", Config{Store: st, Prefix: "/a\r\nb", Resolve: res}, "prefix"},
		{"space", Config{Store: st, Prefix: "/my dav", Resolve: res}, "prefix"},
		{"dot segment", Config{Store: st, Prefix: "/./dav", Resolve: res}, "clean"},
		{"escape", Config{Store: st, Prefix: "/d%61v", Resolve: res}, "prefix"},
		{"query", Config{Store: st, Prefix: "/dav?x", Resolve: res}, "prefix"},
		{"unicode", Config{Store: st, Prefix: "/d\u00e1v", Resolve: res}, "prefix"},
		{"negative size", Config{Store: st, Resolve: res, MaxObjectSize: -1}, "negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := New(tt.cfg)
			if tt.wantErr == "" {
				if err != nil || h == nil {
					t.Fatalf("New = %v, %v", h, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if h != nil {
				t.Error("handler returned with an error")
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	if h.maxSize != 10<<20 {
		t.Errorf("maxSize = %d, want 10 MiB", h.maxSize)
	}
	if h.log == nil {
		t.Fatal("logger is nil")
	}
	h.log.Error("must not panic")

	cfg := Config{Store: panicStore{}, Resolve: resolveAs("o"), MaxObjectSize: 5, Logger: slog.New(slog.DiscardHandler)}
	h2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxObjectSize = 99
	if h2.maxSize != 5 {
		t.Errorf("config was not copied: maxSize = %d", h2.maxSize)
	}
}

func TestHomeHref(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	for user, want := range map[string]string{
		"alice":     "/dav/calendars/alice/",
		"jan novak": "/dav/calendars/jan%20novak/",
		"a/b":       "/dav/calendars/a%2Fb/",
	} {
		if got := h.HomeHref(user); got != want {
			t.Errorf("HomeHref(%q) = %q, want %q", user, got, want)
		}
	}
	if got := newTestHandler(t, "", resolveAs("o")).HomeHref("u"); got != "/calendars/u/" {
		t.Errorf("empty prefix: HomeHref = %q", got)
	}
}

func TestResolveErrorsMapToStatus(t *testing.T) {
	secret := errors.New("db password is hunter2")
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"unauthenticated", access.ErrUnauthenticated, 401},
		{"wrapped unauthenticated", fmt.Errorf("token: %w", access.ErrUnauthenticated), 401},
		{"forbidden", access.ErrForbidden, 403},
		{"user not found", access.ErrUserNotFound, 404},
		{"other", secret, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged strings.Builder
			h, err := New(Config{
				Store:   panicStore{},
				Prefix:  "/dav",
				Resolve: func(*http.Request, string) (string, error) { return "", tt.err },
				Logger:  slog.New(slog.NewTextHandler(&logged, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range []string{"OPTIONS", "PROPFIND", "GET"} {
				rec := do(h, m, "/dav/calendars/alice/work/")
				if rec.Code != tt.status {
					t.Errorf("%s: status = %d, want %d", m, rec.Code, tt.status)
				}
				if got := rec.Header().Get("WWW-Authenticate"); got != "" {
					t.Errorf("%s: WWW-Authenticate = %q, want none", m, got)
				}
			}
			body := do(h, "PROPFIND", "/dav/calendars/alice/").Body.String()
			if tt.status == 500 {
				if strings.Contains(body, "hunter2") {
					t.Errorf("500 body leaks the error: %q", body)
				}
				if body != "internal error" {
					t.Errorf("500 body = %q", body)
				}
				if !strings.Contains(logged.String(), "hunter2") {
					t.Errorf("500 was not logged with its error: %q", logged.String())
				}
			} else if logged.Len() != 0 {
				t.Errorf("unexpected log: %q", logged.String())
			}
		})
	}
}

func TestResolveNotCalledForBadPaths(t *testing.T) {
	called := false
	h := newTestHandler(t, "/dav", func(*http.Request, string) (string, error) {
		called = true
		return "o", nil
	})
	for _, p := range []string{"/other/calendars/alice/", "/dav/calendars/a//b", "/dav/calendars/%2e%2e/"} {
		do(h, "PROPFIND", p)
	}
	if called {
		t.Error("Resolve was called for a path that does not parse")
	}
}

func TestOptionsAllowByKind(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	tests := []struct{ path, allow string }{
		{"/dav/calendars/alice/", "OPTIONS, PROPFIND"},
		{"/dav/calendars/alice/work/", "OPTIONS, PROPFIND, PROPPATCH, MKCALENDAR, MKCOL, DELETE, REPORT"},
		{"/dav/calendars/alice/work", "OPTIONS, PROPFIND, PROPPATCH, MKCALENDAR, MKCOL, DELETE, REPORT"},
		{"/dav/calendars/alice/work/ada.ics", "OPTIONS, HEAD, GET, PUT, DELETE, PROPFIND"},
	}
	for _, tt := range tests {
		rec := do(h, "OPTIONS", tt.path)
		if rec.Code != 200 {
			t.Errorf("%s: status = %d", tt.path, rec.Code)
		}
		if got := rec.Header().Get("DAV"); got != "1, 3, calendar-access, extended-mkcol" {
			t.Errorf("%s: DAV = %q", tt.path, got)
		}
		if got := rec.Header().Get("Allow"); got != tt.allow {
			t.Errorf("%s: Allow = %q, want %q", tt.path, got, tt.allow)
		}
		if rec.Body.Len() != 0 {
			t.Errorf("%s: body = %q, want empty", tt.path, rec.Body.String())
		}
	}
}

func TestResolveReceivesTheURLUser(t *testing.T) {
	tests := []struct{ path, user string }{
		{"/dav/calendars/jan%20novak/", "jan novak"},
		{"/dav/calendars/J%C3%A1n/work/", "Ján"},
		{"/dav/calendars/alice/work/a.ics", "alice"},
		{"/dav/calendars/%E6%97%A5%E6%9C%AC", "日本"},
	}
	for _, tt := range tests {
		var got string
		h := newTestHandler(t, "/dav", func(_ *http.Request, u string) (string, error) {
			got = u
			return "owner", nil
		})
		if rec := do(h, "OPTIONS", tt.path); rec.Code != 200 {
			t.Errorf("%s: status = %d", tt.path, rec.Code)
		}
		if got != tt.user {
			t.Errorf("%s: Resolve got %q, want %q", tt.path, got, tt.user)
		}
	}
}

func TestUnknownMethodIs405WithAllow(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	tests := []struct{ method, path, allow string }{
		{"BREW", "/dav/calendars/alice/", "OPTIONS, PROPFIND"},
		{"PATCH", "/dav/calendars/alice/work/", "OPTIONS, PROPFIND, PROPPATCH, MKCALENDAR, MKCOL, DELETE, REPORT"},
		{"POST", "/dav/calendars/alice/work/a.ics", "OPTIONS, HEAD, GET, PUT, DELETE, PROPFIND"},
		// Not valid for the kind, whatever is implemented later.
		{"PUT", "/dav/calendars/alice/", "OPTIONS, PROPFIND"},
		{"MKCOL", "/dav/calendars/alice/work/a.ics", "OPTIONS, HEAD, GET, PUT, DELETE, PROPFIND"},
		{"REPORT", "/dav/calendars/alice/work/a.ics", "OPTIONS, HEAD, GET, PUT, DELETE, PROPFIND"},
	}
	for _, tt := range tests {
		rec := do(h, tt.method, tt.path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status = %d, want 405", tt.method, tt.path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != tt.allow {
			t.Errorf("%s %s: Allow = %q, want %q", tt.method, tt.path, got, tt.allow)
		}
	}
}

func TestPathErrorsAnswerWithoutTheStore(t *testing.T) {
	for _, prefix := range []string{"/dav", ""} {
		h := newTestHandler(t, prefix, resolveAs("o"))
		tests := []struct {
			path   string
			status int
		}{
			{prefix + "/calendars/alice//", 400},
			{prefix + "/calendars//work/", 400},
			{prefix + "/calendars/alice/work/a.ics/", 404},
			{prefix + "/calendars/alice/c/n/extra", 404},
			{prefix + "/calendars/", 404},
			{prefix + "/addressbooks/alice/", 404},
			{"/elsewhere/calendars/alice/", 404},
			{prefix + "/calendars/%2e%2e/", 400},
			{prefix + "/calendars/alice/a%2Fb", 400},
			{prefix + "/calendars/alice/a%00b", 400},
		}
		for _, tt := range tests {
			if tt.path == "/elsewhere/calendars/alice/" && prefix == "" {
				continue // an empty prefix has no "elsewhere"
			}
			rec := do(h, "OPTIONS", tt.path)
			if rec.Code != tt.status {
				t.Errorf("prefix %q: %s = %d, want %d", prefix, tt.path, rec.Code, tt.status)
			}
		}
	}
}

func TestPathWithoutTrailingSlashIsTheCollection(t *testing.T) {
	var gotUser string
	h := newTestHandler(t, "/dav", func(_ *http.Request, u string) (string, error) { gotUser = u; return "o", nil })
	rec := do(h, "OPTIONS", "/dav/calendars/alice/work")
	if rec.Header().Get("Allow") != "OPTIONS, PROPFIND, PROPPATCH, MKCALENDAR, MKCOL, DELETE, REPORT" || gotUser != "alice" {
		t.Errorf("Allow = %q, user = %q", rec.Header().Get("Allow"), gotUser)
	}
}

func TestMethodNotAllowedResponse(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	rec := do(h, "BREW", "/dav/calendars/alice/")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Allow") == "" {
		t.Error("no Allow header")
	}
	if rec.Body.String() != "method not allowed" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestEmptyOwnerIs500(t *testing.T) {
	var logged strings.Builder
	h, err := New(Config{
		Store:   panicStore{},
		Resolve: resolveAs(""),
		Logger:  slog.New(slog.NewTextHandler(&logged, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, "OPTIONS", "/calendars/alice/")
	if rec.Code != 500 || rec.Body.String() != "internal error" {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logged.String(), "empty owner") {
		t.Errorf("not logged: %q", logged.String())
	}
}

func TestOptionsStar(t *testing.T) {
	called := false
	h := newTestHandler(t, "/dav", func(*http.Request, string) (string, error) { called = true; return "o", nil })
	req := httptest.NewRequest("OPTIONS", "/", nil)
	req.URL.Path, req.RequestURI = "*", "*"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("DAV") != "1, 3, calendar-access, extended-mkcol" {
		t.Errorf("got %d, DAV %q", rec.Code, rec.Header().Get("DAV"))
	}
	if called {
		t.Error("Resolve called for *")
	}
}
