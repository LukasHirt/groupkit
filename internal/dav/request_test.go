// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

func TestReadBody(t *testing.T) {
	read := func(n int, max int64) ([]byte, error) {
		r := httptest.NewRequest("PUT", "/", bytes.NewReader(bytes.Repeat([]byte("a"), n)))
		return ReadBody(httptest.NewRecorder(), r, max)
	}
	if b, err := read(10, 10); err != nil || len(b) != 10 {
		t.Fatalf("at limit: %d %v", len(b), err)
	}
	_, err := read(11, 10)
	var de *Error
	if !errors.As(err, &de) || de.Status != http.StatusRequestEntityTooLarge || de.Message != "request body too large" {
		t.Fatalf("over limit: %v", err)
	}
	if b, err := read(MaxXMLBytes, 0); err != nil || len(b) != MaxXMLBytes {
		t.Fatalf("default limit: %d %v", len(b), err)
	}
	if _, err := read(MaxXMLBytes+1, -1); err == nil {
		t.Fatal("default limit not enforced")
	}
	r := httptest.NewRequest("PUT", "/", nil)
	r.Body = nil
	if b, err := ReadBody(httptest.NewRecorder(), r, 10); err != nil || len(b) != 0 {
		t.Fatalf("nil body: %v %v", b, err)
	}
}

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadBodyReadError(t *testing.T) {
	r := httptest.NewRequest("PUT", "/", nil)
	r.Body = readCloser{failReader{}}
	_, err := ReadBody(httptest.NewRecorder(), r, 10)
	var de *Error
	if !errors.As(err, &de) || de.Status != http.StatusBadRequest {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "boom") {
		t.Fatal("leaks read error")
	}
}

type readCloser struct{ failReader }

func (readCloser) Close() error { return nil }

func TestParseDepth(t *testing.T) {
	ok := map[string]Depth{"": Depth1, "0": Depth0, "1": Depth1, "infinity": Depth1, "Infinity": Depth1, " 1 ": Depth1, " 0 ": Depth0}
	for in, want := range ok {
		got, err := ParseDepth(in, Depth1)
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if got, _ := ParseDepth("", Depth0); got != Depth0 {
		t.Error("default not honored")
	}
	for _, in := range []string{"2", "x", "1,noroot", "-1"} {
		_, err := ParseDepth(in, Depth1)
		var de *Error
		if !errors.As(err, &de) || de.Status != 400 {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestParseCond(t *testing.T) {
	tests := []struct {
		in     string
		star   bool
		etags  []string
		isSet  bool
		strong map[string]bool
		weak   map[string]bool
	}{
		{in: ""},
		{in: "   "},
		{in: "*", star: true, isSet: true, strong: map[string]bool{"x": true, "": false}, weak: map[string]bool{"x": true}},
		{in: `"abc"`, etags: []string{"abc"}, isSet: true, strong: map[string]bool{"abc": true, "abd": false}, weak: map[string]bool{"abc": true}},
		{in: `W/"abc"`, etags: []string{"abc"}, isSet: true, strong: map[string]bool{"abc": false}, weak: map[string]bool{"abc": true, "x": false}},
		{in: `"a", "b"`, etags: []string{"a", "b"}, isSet: true, strong: map[string]bool{"a": true, "b": true, "c": false}},
		{in: `"a,b"`, etags: []string{"a,b"}, isSet: true, strong: map[string]bool{"a,b": true, "a": false}},
		{in: `abc`, etags: []string{"abc"}, isSet: true, strong: map[string]bool{"abc": true}},
		{in: `""`, etags: []string{""}, isSet: true, strong: map[string]bool{"": true}},
		{in: `  "a"  ,   W/"b"  `, etags: []string{"a", "b"}, isSet: true, strong: map[string]bool{"a": true, "b": false}, weak: map[string]bool{"b": true}},
		{in: `W/abc`, etags: []string{"abc"}, isSet: true, strong: map[string]bool{"abc": false}, weak: map[string]bool{"abc": true}},
		{in: `, ,`},
		{in: `"a" garbage "b"`, etags: []string{"a", "b"}, isSet: true},
		{in: `"unterminated`},
	}
	for _, tt := range tests {
		c := ParseCond(tt.in)
		if c.Star != tt.star || !reflect.DeepEqual(c.ETags, tt.etags) || c.IsSet() != tt.isSet {
			t.Errorf("%q: %+v isSet=%v", tt.in, c, c.IsSet())
		}
		for e, want := range tt.strong {
			if c.Matches(e) != want {
				t.Errorf("%q Matches(%q) != %v", tt.in, e, want)
			}
		}
		for e, want := range tt.weak {
			if c.MatchesWeak(e) != want {
				t.Errorf("%q MatchesWeak(%q) != %v", tt.in, e, want)
			}
		}
	}
}

func TestQuoteETag(t *testing.T) {
	for in, want := range map[string]string{
		"abc":      `"abc"`,
		"":         `""`,
		`a"b`:      `"ab"`,
		"a\x00b\n": `"ab"`,
	} {
		if got := QuoteETag(in); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
}

func TestHTTPDate(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	got := HTTPDate(time.Date(2026, 10, 29, 14, 5, 6, 0, loc))
	if got != "Thu, 29 Oct 2026 12:05:06 GMT" {
		t.Fatal(got)
	}
}

func TestEscapeHref(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"", "dav", "u s"}, "/dav/u%20s"},
		{[]string{"dav", "a"}, "dav/a"},
		{[]string{"", "100%", "a?b", "c#d"}, "/100%25/a%3Fb/c%23d"},
		{[]string{"", "ä€"}, "/%C3%A4%E2%82%AC"},
		{[]string{"", "a/b"}, "/a%2Fb"},
		{[]string{"", "a\xffb"}, "/a%EF%BF%BDb"},
		{[]string{"", "x", ""}, "/x/"},
		{nil, ""},
		{[]string{""}, ""},
	}
	for _, tt := range tests {
		if got := EscapeHref(tt.in...); got != tt.want {
			t.Errorf("%q: got %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestCleanSegment(t *testing.T) {
	good := map[string]string{
		"abc":                    "abc",
		"a%20b":                  "a b",
		"%C3%A4":                 "ä",
		"a.ics":                  "a.ics",
		"...":                    "...",
		"a%25b":                  "a%b",
		"%252e%252e":             "%2e%2e",
		strings.Repeat("a", 255): strings.Repeat("a", 255),
	}
	for in, want := range good {
		got, err := CleanSegment(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	bad := []string{"", ".", "..", "%2e%2e", "%2E", "%00", "a%00b", "%2F", "a/b", "%0a", "%1f", "%7f", "\x01",
		"%ff", "%zz", "a%", strings.Repeat("a", 256), strings.Repeat("%C3%A4", 128)}
	for _, in := range bad {
		_, err := CleanSegment(in)
		var de *Error
		if !errors.As(err, &de) || de.Status != 400 {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestStoreError(t *testing.T) {
	if StoreError(nil) != nil {
		t.Fatal("nil")
	}
	tests := []struct {
		name string
		err  error
		code int
		pre  string
		msg  string
	}{
		{"notfound", store.ErrNotFound, 404, "", "not found"},
		{"wrapped notfound", fmt.Errorf("x: %w", store.ErrNotFound), 404, "", "not found"},
		{"precondition", store.ErrPreconditionFailed, 412, "", "precondition failed"},
		{"exists", fmt.Errorf("x: %w", store.ErrExists), 405, "", "already exists"},
		{"conflict", &store.ConflictError{UID: "u", Name: "n"}, 409, "", "uid conflict"},
		{"wrapped conflict", fmt.Errorf("x: %w", &store.ConflictError{UID: "u", Name: "n"}), 409, "", "uid conflict"},
		{"uid", store.ErrUIDConflict, 409, "", "uid conflict"},
		{"name", fmt.Errorf("x: %w", store.ErrInvalidName), 400, "", "invalid name"},
		{"changes", store.ErrChangesUnavailable, 403, "valid-sync-token", ""},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), 503, "", "request cancelled"},
		{"deadline", context.DeadlineExceeded, 503, "", "request cancelled"},
		{"other", errors.New("secret detail"), 500, "", "internal error"},
	}
	for _, tt := range tests {
		e := StoreError(tt.err)
		if e == nil || e.Status != tt.code || e.Precondition.Local != tt.pre || e.Message != tt.msg {
			t.Errorf("%s: %+v", tt.name, e)
		}
	}
	if strings.Contains(StoreError(errors.New("secret detail")).Error(), "secret") {
		t.Error("leaks")
	}
}

func FuzzParseCond(f *testing.F) {
	for _, s := range []string{"", "*", `"a"`, `W/"a", "b,c"`, `abc`, `"`, `W/`, `, ,`, "\x00\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c := ParseCond(s)
		_ = c.IsSet()
		_ = c.Matches("a")
		_ = c.MatchesWeak("a")
	})
}

func FuzzCleanSegment(f *testing.F) {
	for _, s := range []string{"", "a", "%2e%2e", "%00", "%2F", "a%20b", "%", "%ff", "..", "."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := CleanSegment(s)
		if err != nil {
			return
		}
		if got == "" || got == "." || got == ".." || strings.ContainsAny(got, "/\x00") || len(got) > 255 {
			t.Fatalf("%q -> %q", s, got)
		}
	})
}

func TestCondMalformed(t *testing.T) {
	tests := []struct {
		in        string
		present   bool
		malformed bool
	}{
		{"", false, false},
		{"   \t ", false, false},
		{`"unterminated`, true, true},
		{"a b", true, true},
		{", ,", true, true},
		{"*", true, false},
		{`"a"`, true, false},
		{`W/"a", "b"`, true, false},
		{"abc", true, false},
	}
	for _, tt := range tests {
		c := ParseCond(tt.in)
		if c.Present != tt.present || c.Malformed() != tt.malformed {
			t.Errorf("%q: Present=%v Malformed=%v", tt.in, c.Present, c.Malformed())
		}
	}
}

func TestCondLargeAndMixed(t *testing.T) {
	big := ParseCond(strings.Repeat(`"a",`, 512*1024))
	if len(big.ETags) != 512*1024 {
		t.Fatalf("got %d tags", len(big.ETags))
	}
	c := ParseCond(`*, "a", W/"b"`)
	if !c.Star || !reflect.DeepEqual(c.ETags, []string{"a", "b"}) || !c.Matches("zzz") || !c.Matches("a") {
		t.Fatalf("%+v", c)
	}
}

func TestReadBodyTooLargeClosesConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := ReadBody(w, r, 4); err != nil {
			var de *Error
			if errors.As(err, &de) {
				WriteError(w, de)
			}
		}
	}))
	defer srv.Close()
	resp, err := http.Post(srv.URL, "text/plain", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 413 || !resp.Close {
		t.Fatalf("status %d close %v", resp.StatusCode, resp.Close)
	}
}

func TestSegmentRoundTrip(t *testing.T) {
	for _, s := range []string{"%", "+", "?", "#", "a..b", ".hidden", "ä€ 日本", "%2e%2e", "a b", "a:b@c", "100%25"} {
		esc := EscapeHref(s)
		got, err := CleanSegment(esc)
		if err != nil || got != s {
			t.Errorf("%q -> %q -> %q, %v", s, esc, got, err)
		}
	}
	if got, err := CleanSegment("%252e%252e"); err != nil || got != "%2e%2e" {
		t.Errorf("%q %v", got, err)
	}
}

func TestStoreErrorJoin(t *testing.T) {
	e := StoreError(errors.Join(errors.New("other"), store.ErrNotFound))
	if e.Status != 404 {
		t.Fatalf("%+v", e)
	}
	e = StoreError(errors.Join(errors.New("a"), errors.New("b")))
	if e.Status != 500 {
		t.Fatalf("%+v", e)
	}
}
