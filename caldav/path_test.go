// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	h := newTestHandler(t, "/dav", resolveAs("o"))
	long := strings.Repeat("a", 255)
	tests := []struct {
		name   string
		path   string
		want   target
		status int // 0 means success
	}{
		{"home", "/dav/calendars/alice/", target{kind: kindHome, user: "alice"}, 0},
		{"home without slash", "/dav/calendars/alice", target{kind: kindHome, user: "alice"}, 0},
		{"collection", "/dav/calendars/alice/work/", target{kind: kindCollection, user: "alice", calendar: "work"}, 0},
		{"collection without slash", "/dav/calendars/alice/work", target{kind: kindCollection, user: "alice", calendar: "work"}, 0},
		{"object", "/dav/calendars/alice/work/ada.ics", target{kind: kindObject, user: "alice", calendar: "work", name: "ada.ics"}, 0},
		{"space and unicode", "/dav/calendars/jan%20novak/k%C3%B3d/Ad%C3%A1%20L.ics",
			target{kind: kindObject, user: "jan novak", calendar: "kód", name: "Adá L.ics"}, 0},
		{"raw unicode", "/dav/calendars/alice/work/日本.ics",
			target{kind: kindObject, user: "alice", calendar: "work", name: "日本.ics"}, 0},
		{"255 byte name", "/dav/calendars/alice/work/" + long, target{kind: kindObject, user: "alice", calendar: "work", name: long}, 0},
		{"256 byte name", "/dav/calendars/alice/work/" + long + "a", target{}, 400},
		{"dot dot", "/dav/calendars/alice/%2e%2e/x", target{}, 400},
		{"dot dot user", "/dav/calendars/%2E%2E/", target{}, 400},
		{"single dot", "/dav/calendars/alice/./x", target{}, 400},
		{"encoded slash", "/dav/calendars/alice/work/a%2Fb", target{}, 400},
		{"encoded slash lower", "/dav/calendars/alice/work/a%2fb", target{}, 400},
		{"nul", "/dav/calendars/alice/work/a%00b", target{}, 400},
		{"control", "/dav/calendars/alice/work/a%0Ab", target{}, 400},
		{"bad escape", "/dav/calendars/alice/work/a%zz", target{}, 400},
		{"invalid utf8", "/dav/calendars/alice/work/%ff", target{}, 400},
		{"double encoded stays once decoded", "/dav/calendars/alice/work/%252e%252e",
			target{kind: kindObject, user: "alice", calendar: "work", name: "%2e%2e"}, 0},
		{"empty user segment", "/dav/calendars//work/", target{}, 400},
		{"empty middle segment", "/dav/calendars/alice//x", target{}, 400},
		{"empty last segment", "/dav/calendars/alice/work//", target{}, 400},
		{"object with trailing slash", "/dav/calendars/alice/work/ada.ics/", target{}, 404},
		{"four segments", "/dav/calendars/alice/work/ada.ics/more", target{}, 404},
		{"four segments trailing", "/dav/calendars/a/b/c/d/", target{}, 404},
		{"outside prefix", "/other/calendars/alice/", target{}, 404},
		{"prefix only", "/dav", target{}, 404},
		{"prefix slash", "/dav/", target{}, 404},
		{"calendars root", "/dav/calendars/", target{}, 404},
		{"calendars no slash", "/dav/calendars", target{}, 404},
		{"prefix lookalike", "/davx/calendars/alice/", target{}, 404},
		{"addressbooks", "/dav/addressbooks/alice/", target{}, 404},
		{"empty path", "", target{}, 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.parsePath(tt.path)
			if tt.status != 0 {
				if err == nil || err.Status != tt.status {
					t.Fatalf("parsePath(%q) = %+v, %v; want status %d", tt.path, got, err, tt.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePath(%q) error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("parsePath(%q) = %+v, want %+v", tt.path, got, tt.want)
			}
		})
	}
}

func TestParsePathEmptyPrefix(t *testing.T) {
	h := newTestHandler(t, "", resolveAs("o"))
	got, err := h.parsePath("/calendars/alice/work/")
	if err != nil || got != (target{kind: kindCollection, user: "alice", calendar: "work"}) {
		t.Errorf("got %+v, %v", got, err)
	}
	for _, p := range []string{"/", "/dav/calendars/alice/", "/addressbooks/alice/"} {
		if _, err := h.parsePath(p); err == nil || err.Status != 404 {
			t.Errorf("parsePath(%q) = %v, want 404", p, err)
		}
	}
}

func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"/dav/calendars/alice/", "/dav/calendars/a/b/c", "/dav/calendars//", "/dav/calendars/%2e%2e/",
		"/dav/calendars/a%2Fb/", "/dav/calendars/%00", "/dav/calendars/a/b/c/d", "", "/",
	} {
		f.Add(s)
	}
	h, err := New(Config{Store: panicStore{}, Prefix: "/dav", Resolve: resolveAs("o")})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got, derr := h.parsePath(p)
		if derr != nil {
			if derr.Status != 400 && derr.Status != 404 {
				t.Fatalf("status = %d", derr.Status)
			}
			return
		}
		if got.kind < kindHome || got.kind > kindObject {
			t.Fatalf("kind = %d", got.kind)
		}
		segs := []string{got.user}
		switch got.kind {
		case kindCollection:
			segs = append(segs, got.calendar)
		case kindObject:
			segs = append(segs, got.calendar, got.name)
		}
		for _, s := range segs {
			if s == "" || s == "." || s == ".." || len(s) > 255 || strings.ContainsAny(s, "/\x00") {
				t.Fatalf("unsafe segment %q in %+v", s, got)
			}
		}
		if (got.kind != kindObject && got.name != "") || (got.kind == kindHome && got.calendar != "") {
			t.Fatalf("stray fields in %+v", got)
		}
	})
}
