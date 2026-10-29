// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/internal/collation"
)

func countFolds(t *testing.T) *int {
	t.Helper()
	n := 0
	collation.FoldHook = func() { n++ }
	t.Cleanup(func() { collation.FoldHook = nil })
	return &n
}

func cardWithEmails(t *testing.T, n int) *Card {
	t.Helper()
	var b strings.Builder
	b.WriteString("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:X\r\n")
	for i := range n {
		fmt.Fprintf(&b, "EMAIL:Person%d@Example.ORG\r\n", i)
	}
	b.WriteString("END:VCARD\r\n")
	c, err := Parse([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFoldCountIsPerPropertyNotPerTextMatch(t *testing.T) {
	var tms []TextMatch
	for i := range 50 {
		tms = append(tms, TextMatch{Value: fmt.Sprintf("PERSON%d", i), Negate: i%2 == 0})
	}
	f := &Filter{Props: []PropFilter{{Name: "EMAIL", Test: TestAllOf, TextMatches: tms}}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	card := cardWithEmails(t, 3)
	n := countFolds(t)
	f.Match(card)
	// allof over a mix of negated matches fails on the first instance, but
	// every instance is still tried: at most one fold per property instance.
	if *n > 3 {
		t.Errorf("%d folds for 3 property instances and 50 text-matches", *n)
	}
	if *n == 0 {
		t.Error("nothing was folded")
	}
	// The same filter on a bigger card costs proportionally, not 50x.
	card = cardWithEmails(t, 10)
	*n = 0
	f.Match(card)
	if *n > 10 {
		t.Errorf("%d folds for 10 property instances", *n)
	}
}

func TestNeedleFoldedOnceAtValidate(t *testing.T) {
	n := countFolds(t)
	f := &Filter{Props: []PropFilter{{Name: "FN", TextMatches: []TextMatch{
		{Value: "ǅ", MatchType: MatchEquals},
		{Value: "ß", MatchType: MatchContains},
	}}}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	if *n != 2 {
		t.Errorf("Validate folded %d times, want 2", *n)
	}
	card := cardWithEmails(t, 1)
	*n = 0
	for range 5 {
		f.Match(card)
	}
	if *n != 5 {
		t.Errorf("5 matches folded %d times, want 5 (the FN value only)", *n)
	}
}

// TestFoldedPathEqualsCollation compares the prepared path, the unprepared
// path and the collation's own functions.
func TestFoldedPathEqualsCollation(t *testing.T) {
	values := []string{"", "a", "A", "ǅ", "ǆ", "Ǆ", "ß", "SS", "ss", "ẞ", "İ", "i̇", "i", "I", "ı", "Straße", "STRASSE", "é", "É", "é", "日本", "Ünal"}
	collations := []string{"", "i;octet", "i;ascii-casemap", "i;unicode-casemap", "I;UNICODE-CASEMAP"}
	types := []string{"", MatchContains, MatchEquals, MatchStartsWith, MatchEndsWith}
	for _, cn := range collations {
		col, err := collation.Get(cn)
		if err != nil {
			t.Fatal(err)
		}
		for _, mt := range types {
			for _, hay := range values {
				for _, needle := range values {
					for _, neg := range []bool{false, true} {
						var want bool
						switch mt {
						case "", MatchContains:
							want = col.Contains(hay, needle)
						case MatchEquals:
							want = col.Equal(hay, needle)
						case MatchStartsWith:
							want = col.HasPrefix(hay, needle)
						case MatchEndsWith:
							want = col.HasSuffix(hay, needle)
						}
						want = want != neg
						tm := TextMatch{Value: needle, Collation: cn, MatchType: mt, Negate: neg}
						unprepared := (&tm).matches(hay, nil)
						f := &Filter{Props: []PropFilter{{Name: "NOTE", TextMatches: []TextMatch{tm}}}}
						if err := f.Validate(); err != nil {
							t.Fatal(err)
						}
						c, perr := Parse([]byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:x\r\nNOTE:" + hay + "\r\nEND:VCARD\r\n"))
						if perr != nil {
							t.Fatal(perr)
						}
						prepared := f.Match(c)
						if unprepared != want || prepared != want {
							t.Fatalf("coll %q type %q hay %q needle %q neg %v: unprepared %v prepared %v want %v",
								cn, mt, hay, needle, neg, unprepared, prepared, want)
						}
					}
				}
			}
		}
	}
}

func TestValidatedMatchFollowsLaterEdits(t *testing.T) {
	f := &Filter{Props: []PropFilter{{Name: "FN", TextMatches: []TextMatch{{Value: "nomatch"}}}}}
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	c := cardWithEmails(t, 1)
	if f.Match(c) {
		t.Fatal("matched")
	}
	f.Props[0].TextMatches[0].Value = "x"
	if !f.Match(c) {
		t.Error("a changed Value after Validate is ignored")
	}
	f.Props[0].TextMatches[0].Collation = "i;bogus"
	if f.Match(c) {
		t.Error("an unknown collation matched")
	}
}
