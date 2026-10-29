// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func TestFileNameExamples(t *testing.T) {
	cases := map[string]string{
		"event.ics":             "event.ics",
		"A.ics":                 "%41.ics",
		".hidden":               "%2Ehidden",
		"trailing.":             "trailing%2E",
		"a b":                   "a%20b",
		"a/b":                   "a%2Fb",
		"a%2Fb":                 "a%252%46b",
		"ä":                     "%C3%A4",
		"con.ics":               "%63on.ics",
		"CON":                   "%43%4F%4E",
		"nul":                   "%6Eul",
		"console":               "console",
		"12345@example.com.ics": "12345%40example.com.ics",
	}
	for in, want := range cases {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFileNameLongNamesAreHashed(t *testing.T) {
	hashed := regexp.MustCompile(`^~[0-9a-f]{64}$`)
	a := strings.Repeat("a", 255)
	b := strings.Repeat("ä", 100)
	ga, gb := fileName(a), fileName(b)
	for _, g := range []string{ga, gb} {
		if !hashed.MatchString(g) {
			t.Errorf("unexpected result %q", g)
		}
	}
	if ga == gb {
		t.Error("distinct names share a hash")
	}
	if fileName(a) != ga || fileName(b) != gb {
		t.Error("hash is not deterministic")
	}
}

func TestFileNameIsSafeAndInjective(t *testing.T) {
	alphabet := []string{"A", "a", "0", " ", ".", "-", "_", "~", "%", ":", "/", "\\", "*", "?", `"`,
		"<", ">", "|", "@", "ä", "ß", "😀", "é", "\x00", "\x01", "\x7f"}
	safe := regexp.MustCompile(`^[a-z0-9%A-F._~-]+$`)
	rng := rand.New(rand.NewSource(1))
	seen := make(map[string]string)
	seenLower := make(map[string]string)
	for range 200000 {
		var sb strings.Builder
		for range 1 + rng.Intn(8) {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		in := sb.String()
		out := fileName(in)
		if !safe.MatchString(out) {
			t.Fatalf("fileName(%q) = %q is not safe", in, out)
		}
		if strings.HasPrefix(out, ".") || strings.HasSuffix(out, ".") {
			t.Fatalf("fileName(%q) = %q has a leading or trailing dot", in, out)
		}
		if len(out) > 200 && out[0] != '~' {
			t.Fatalf("fileName(%q) = %q is too long", in, out)
		}
		if prev, ok := seen[out]; ok && prev != in {
			t.Fatalf("%q and %q both map to %q", prev, in, out)
		}
		seen[out] = in
		lower := strings.ToLower(out)
		if prev, ok := seenLower[lower]; ok && prev != in {
			t.Fatalf("%q and %q collide after lower-casing: %q", prev, in, lower)
		}
		seenLower[lower] = in
	}
}
