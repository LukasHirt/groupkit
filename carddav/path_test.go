// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

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
		{"home", "/dav/addressbooks/alice/", target{kind: kindHome, user: "alice"}, 0},
		{"home without slash", "/dav/addressbooks/alice", target{kind: kindHome, user: "alice"}, 0},
		{"collection", "/dav/addressbooks/alice/contacts/", target{kind: kindCollection, user: "alice", book: "contacts"}, 0},
		{"collection without slash", "/dav/addressbooks/alice/contacts", target{kind: kindCollection, user: "alice", book: "contacts"}, 0},
		{"object", "/dav/addressbooks/alice/contacts/ada.vcf", target{kind: kindObject, user: "alice", book: "contacts", name: "ada.vcf"}, 0},
		{"space and unicode", "/dav/addressbooks/jan%20novak/k%C3%B3d/Ad%C3%A1%20L.vcf",
			target{kind: kindObject, user: "jan novak", book: "kód", name: "Adá L.vcf"}, 0},
		{"raw unicode", "/dav/addressbooks/alice/contacts/日本.vcf",
			target{kind: kindObject, user: "alice", book: "contacts", name: "日本.vcf"}, 0},
		{"255 byte name", "/dav/addressbooks/alice/contacts/" + long, target{kind: kindObject, user: "alice", book: "contacts", name: long}, 0},
		{"256 byte name", "/dav/addressbooks/alice/contacts/" + long + "a", target{}, 400},
		{"dot dot", "/dav/addressbooks/alice/%2e%2e/x", target{}, 400},
		{"dot dot user", "/dav/addressbooks/%2E%2E/", target{}, 400},
		{"single dot", "/dav/addressbooks/alice/./x", target{}, 400},
		{"encoded slash", "/dav/addressbooks/alice/contacts/a%2Fb", target{}, 400},
		{"encoded slash lower", "/dav/addressbooks/alice/contacts/a%2fb", target{}, 400},
		{"nul", "/dav/addressbooks/alice/contacts/a%00b", target{}, 400},
		{"control", "/dav/addressbooks/alice/contacts/a%0Ab", target{}, 400},
		{"bad escape", "/dav/addressbooks/alice/contacts/a%zz", target{}, 400},
		{"invalid utf8", "/dav/addressbooks/alice/contacts/%ff", target{}, 400},
		{"double encoded stays once decoded", "/dav/addressbooks/alice/contacts/%252e%252e",
			target{kind: kindObject, user: "alice", book: "contacts", name: "%2e%2e"}, 0},
		{"empty user segment", "/dav/addressbooks//contacts/", target{}, 400},
		{"empty middle segment", "/dav/addressbooks/alice//x", target{}, 400},
		{"empty last segment", "/dav/addressbooks/alice/contacts//", target{}, 400},
		{"object with trailing slash", "/dav/addressbooks/alice/contacts/ada.vcf/", target{}, 404},
		{"four segments", "/dav/addressbooks/alice/contacts/ada.vcf/more", target{}, 404},
		{"four segments trailing", "/dav/addressbooks/a/b/c/d/", target{}, 404},
		{"outside prefix", "/other/addressbooks/alice/", target{}, 404},
		{"prefix only", "/dav", target{}, 404},
		{"prefix slash", "/dav/", target{}, 404},
		{"addressbooks root", "/dav/addressbooks/", target{}, 404},
		{"addressbooks no slash", "/dav/addressbooks", target{}, 404},
		{"prefix lookalike", "/davx/addressbooks/alice/", target{}, 404},
		{"calendars", "/dav/calendars/alice/", target{}, 404},
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
	got, err := h.parsePath("/addressbooks/alice/contacts/")
	if err != nil || got != (target{kind: kindCollection, user: "alice", book: "contacts"}) {
		t.Errorf("got %+v, %v", got, err)
	}
	for _, p := range []string{"/", "/dav/addressbooks/alice/", "/calendars/alice/"} {
		if _, err := h.parsePath(p); err == nil || err.Status != 404 {
			t.Errorf("parsePath(%q) = %v, want 404", p, err)
		}
	}
}

func FuzzParsePath(f *testing.F) {
	for _, s := range []string{
		"/dav/addressbooks/alice/", "/dav/addressbooks/a/b/c", "/dav/addressbooks//", "/dav/addressbooks/%2e%2e/",
		"/dav/addressbooks/a%2Fb/", "/dav/addressbooks/%00", "/dav/addressbooks/a/b/c/d", "", "/",
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
			segs = append(segs, got.book)
		case kindObject:
			segs = append(segs, got.book, got.name)
		}
		for _, s := range segs {
			if s == "" || s == "." || s == ".." || len(s) > 255 || strings.ContainsAny(s, "/\x00") {
				t.Fatalf("unsafe segment %q in %+v", s, got)
			}
		}
		if (got.kind != kindObject && got.name != "") || (got.kind == kindHome && got.book != "") {
			t.Fatalf("stray fields in %+v", got)
		}
	})
}
