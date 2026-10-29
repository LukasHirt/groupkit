// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"errors"
	"strings"
	"testing"
)

func mustCard(t *testing.T, lines ...string) *Card {
	t.Helper()
	data := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:X\r\n"
	for _, l := range lines {
		data += l + "\r\n"
	}
	data += "END:VCARD\r\n"
	c, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func emailFilter(tm TextMatch) *Filter {
	return &Filter{Props: []PropFilter{{Name: "EMAIL", TextMatches: []TextMatch{tm}}}}
}

func TestFilterTextMatch(t *testing.T) {
	c := mustCard(t, "EMAIL:ada@example.org", "NOTE:a\\, b", "N:Lovelace;äda;;;")
	tests := []struct {
		name string
		f    *Filter
		want bool
	}{
		{"contains", emailFilter(TextMatch{Value: "example"}), true},
		{"contains miss", emailFilter(TextMatch{Value: "nope"}), false},
		{"equals", emailFilter(TextMatch{Value: "ada@example.org", MatchType: "equals"}), true},
		{"equals miss", emailFilter(TextMatch{Value: "ada", MatchType: "equals"}), false},
		{"starts-with", emailFilter(TextMatch{Value: "ada@", MatchType: "starts-with"}), true},
		{"starts-with miss", emailFilter(TextMatch{Value: "example", MatchType: "starts-with"}), false},
		{"ends-with", emailFilter(TextMatch{Value: ".org", MatchType: "ends-with"}), true},
		{"ends-with miss", emailFilter(TextMatch{Value: "ada", MatchType: "ends-with"}), false},
		{"default collation folds case", emailFilter(TextMatch{Value: "ADA"}), true},
		{"octet exact", emailFilter(TextMatch{Value: "ADA", Collation: "i;octet"}), false},
		{"octet exact hit", emailFilter(TextMatch{Value: "ada", Collation: "i;octet"}), true},
		{"negate", emailFilter(TextMatch{Value: "ada", Negate: true}), false},
		{"negate miss", emailFilter(TextMatch{Value: "zzz", Negate: true}), true},
		{"escaped text", &Filter{Props: []PropFilter{{Name: "NOTE", TextMatches: []TextMatch{{Value: "a, b", MatchType: "equals"}}}}}, true},
		{"ascii casemap leaves non-ASCII", &Filter{Props: []PropFilter{{Name: "N", TextMatches: []TextMatch{{Value: "ÄDA", Collation: "i;ascii-casemap"}}}}}, false},
		{"unicode casemap folds non-ASCII", &Filter{Props: []PropFilter{{Name: "N", TextMatches: []TextMatch{{Value: "ÄDA", Collation: "i;unicode-casemap"}}}}}, true},
		{"bad collation", emailFilter(TextMatch{Value: "ada", Collation: "x"}), false},
		{"bad match type", emailFilter(TextMatch{Value: "ada", MatchType: "x"}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Match(c); got != tt.want {
				t.Fatalf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterProperties(t *testing.T) {
	c := mustCard(t, "EMAIL:first@example.org", "EMAIL;TYPE=work:second@example.net", "item1.TEL;TYPE=cell:+1 555")
	has := func(name string, tms ...TextMatch) *Filter {
		return &Filter{Props: []PropFilter{{Name: name, TextMatches: tms}}}
	}
	tests := []struct {
		name string
		f    *Filter
		want bool
	}{
		{"empty filter", &Filter{}, true},
		{"any property of the name", has("EMAIL", TextMatch{Value: "second"}), true},
		{"lower-case name", has("email"), true},
		{"missing property", has("ADR"), false},
		{"group property by name", has("TEL", TextMatch{Value: "555"}), true},
		{"not defined hit", &Filter{Props: []PropFilter{{Name: "ADR", IsNotDefined: true}}}, true},
		{"not defined miss", &Filter{Props: []PropFilter{{Name: "EMAIL", IsNotDefined: true}}}, false},
		{"param equals", &Filter{Props: []PropFilter{{Name: "EMAIL", Params: []ParamFilter{
			{Name: "TYPE", TextMatch: &TextMatch{Value: "work", MatchType: "equals"}}}}}}, true},
		{"param on other property only", &Filter{Props: []PropFilter{{Name: "TEL", Params: []ParamFilter{
			{Name: "TYPE", TextMatch: &TextMatch{Value: "work"}}}}}}, false},
		{"param lower-case name", &Filter{Props: []PropFilter{{Name: "EMAIL", Params: []ParamFilter{{Name: "type"}}}}}, true},
		{"param not defined hit", &Filter{Props: []PropFilter{{Name: "TEL", Params: []ParamFilter{{Name: "PREF", IsNotDefined: true}}}}}, true},
		{"param not defined miss", &Filter{Props: []PropFilter{{Name: "TEL", Params: []ParamFilter{{Name: "TYPE", IsNotDefined: true}}}}}, false},
		{"prop anyof", &Filter{Props: []PropFilter{{Name: "EMAIL", TextMatches: []TextMatch{{Value: "zzz"}, {Value: "first"}}}}}, true},
		{"prop allof miss", &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "allof", TextMatches: []TextMatch{{Value: "first"}, {Value: "zzz"}}}}}, false},
		{"prop allof text and param", &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "allof",
			TextMatches: []TextMatch{{Value: "second"}}, Params: []ParamFilter{{Name: "TYPE"}}}}}, true},
		{"prop allof text and param miss", &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "allof",
			TextMatches: []TextMatch{{Value: "first"}}, Params: []ParamFilter{{Name: "TYPE"}}}}}, false},
		{"filter anyof", &Filter{Props: []PropFilter{{Name: "ADR"}, {Name: "EMAIL"}}}, true},
		{"filter allof hit", &Filter{Test: "allof", Props: []PropFilter{{Name: "TEL"}, {Name: "EMAIL"}}}, true},
		{"filter allof miss", &Filter{Test: "allof", Props: []PropFilter{{Name: "ADR"}, {Name: "EMAIL"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Match(c); got != tt.want {
				t.Fatalf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterValidate(t *testing.T) {
	tests := []struct {
		name string
		f    *Filter
		ok   bool
	}{
		{"empty", &Filter{}, true},
		{"valid", &Filter{Test: "allof", Props: []PropFilter{{Name: "EMAIL", Test: "anyof",
			TextMatches: []TextMatch{{Value: "a", Collation: "i;octet", MatchType: "equals"}},
			Params:      []ParamFilter{{Name: "TYPE", TextMatch: &TextMatch{Value: "w"}}}}}}, true},
		{"bad collation", emailFilter(TextMatch{Collation: "x"}), false},
		{"bad match type", emailFilter(TextMatch{MatchType: "x"}), false},
		{"bad filter test", &Filter{Test: "xor"}, false},
		{"bad prop test", &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "xor"}}}, false},
		{"bad param collation", &Filter{Props: []PropFilter{{Name: "EMAIL",
			Params: []ParamFilter{{Name: "TYPE", TextMatch: &TextMatch{Collation: "x"}}}}}}, false},
		{"bad param match type", &Filter{Props: []PropFilter{{Name: "EMAIL",
			Params: []ParamFilter{{Name: "TYPE", TextMatch: &TextMatch{MatchType: "x"}}}}}}, false},
		{"collation name in upper case", emailFilter(TextMatch{Collation: "I;OCTET"}), true},
		{"empty prop name", &Filter{Props: []PropFilter{{}}}, false},
		{"empty param name", &Filter{Props: []PropFilter{{Name: "EMAIL", Params: []ParamFilter{{}}}}}, false},
		{"empty param name with not defined", &Filter{Props: []PropFilter{{Name: "EMAIL",
			Params: []ParamFilter{{IsNotDefined: true}}}}}, false},
		{"prop not defined", &Filter{Props: []PropFilter{{Name: "EMAIL", IsNotDefined: true}}}, true},
		{"prop not defined with text match", &Filter{Props: []PropFilter{{Name: "EMAIL", IsNotDefined: true,
			TextMatches: []TextMatch{{Value: "a"}}}}}, false},
		{"prop not defined with param", &Filter{Props: []PropFilter{{Name: "EMAIL", IsNotDefined: true,
			Params: []ParamFilter{{Name: "TYPE"}}}}}, false},
		{"param not defined", &Filter{Props: []PropFilter{{Name: "EMAIL",
			Params: []ParamFilter{{Name: "TYPE", IsNotDefined: true}}}}}, true},
		{"param not defined with text match", &Filter{Props: []PropFilter{{Name: "EMAIL",
			Params: []ParamFilter{{Name: "TYPE", IsNotDefined: true, TextMatch: &TextMatch{Value: "w"}}}}}}, false},
		{"nil filter", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.f.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !tt.ok && !errors.Is(err, ErrFilter) {
				t.Fatalf("err = %v, want ErrFilter", err)
			}
		})
	}
}

func TestFilterNil(t *testing.T) {
	c := mustCard(t, "EMAIL:ada@example.org")
	var nilFilter *Filter
	if !nilFilter.Match(c) {
		t.Error("nil filter does not match a card")
	}
	if !nilFilter.Match(nil) {
		t.Error("nil filter does not match a nil card")
	}
	if !(&Filter{}).Match(nil) {
		t.Error("empty filter does not match a nil card")
	}
	for _, f := range []*Filter{
		{Props: []PropFilter{{Name: "EMAIL"}}},
		{Props: []PropFilter{{Name: "ADR", IsNotDefined: true}}},
		{Test: "allof", Props: []PropFilter{{Name: "ADR", IsNotDefined: true}}},
	} {
		if f.Match(nil) {
			t.Errorf("filter %#v matches a nil card", f)
		}
	}
}

// TestFilterSameInstance pins RFC 6352 section 10.5.1: every condition of a
// prop-filter is tested against one property instance. Some servers aggregate
// text matches across instances and would match here.
func TestFilterSameInstance(t *testing.T) {
	c := mustCard(t, "EMAIL:first@x", "EMAIL:second@x")
	allof := &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "allof",
		TextMatches: []TextMatch{{Value: "first"}, {Value: "second"}}}}}
	if allof.Match(c) {
		t.Error("allof matched across two EMAIL lines")
	}
	anyof := &Filter{Props: []PropFilter{{Name: "EMAIL",
		TextMatches: []TextMatch{{Value: "first"}, {Value: "second"}}}}}
	if !anyof.Match(c) {
		t.Error("anyof did not match")
	}
	mixed := mustCard(t, "EMAIL;TYPE=home:first@x", "EMAIL;TYPE=work:second@x")
	crossed := &Filter{Props: []PropFilter{{Name: "EMAIL", Test: "allof",
		TextMatches: []TextMatch{{Value: "first"}},
		Params:      []ParamFilter{{Name: "TYPE", TextMatch: &TextMatch{Value: "work", MatchType: "equals"}}}}}}
	if crossed.Match(mixed) {
		t.Error("allof combined a text match and a param filter from different lines")
	}
}

func TestFilterNegate(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		tm    TextMatch
		want  bool
	}{
		{"matches through the other line", []string{"EMAIL:ada@example.org", "EMAIL:bob@example.org"},
			TextMatch{Value: "ada", Negate: true}, true},
		{"every line matches", []string{"EMAIL:ada@example.org", "EMAIL:ada@example.net"},
			TextMatch{Value: "ada", Negate: true}, false},
		{"empty value", []string{"EMAIL:"}, TextMatch{Value: "ada", Negate: true}, true},
		{"empty value, empty needle", []string{"EMAIL:"}, TextMatch{Negate: true}, false},
		{"no such property", []string{"FN:Y"}, TextMatch{Value: "ada", Negate: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emailFilter(tt.tm).Match(mustCard(t, tt.lines...)); got != tt.want {
				t.Fatalf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterClientSearches(t *testing.T) {
	c := mustCard(t, "FN:Ada Lovelace", "EMAIL;TYPE=INTERNET:Ada.Lovelace@Example.ORG", "NICKNAME:The Enchantress")
	search := func(fragment string) *Filter {
		var props []PropFilter
		for _, name := range []string{"FN", "EMAIL", "NICKNAME"} {
			props = append(props, PropFilter{Name: name,
				TextMatches: []TextMatch{{Value: fragment, Collation: "i;unicode-casemap", MatchType: "contains"}}})
		}
		return &Filter{Test: "anyof", Props: props}
	}
	tests := []struct {
		name string
		f    *Filter
		want bool
	}{
		{"fragment in FN", search("lovel"), true},
		{"fragment only in EMAIL", search("example.org"), true},
		{"fragment only in NICKNAME", search("enchant"), true},
		{"no match", search("babbage"), false},
		{"starts-with on FN", &Filter{Props: []PropFilter{{Name: "FN",
			TextMatches: []TextMatch{{Value: "ada", MatchType: "starts-with"}}}}}, true},
		{"starts-with on FN miss", &Filter{Props: []PropFilter{{Name: "FN",
			TextMatches: []TextMatch{{Value: "love", MatchType: "starts-with"}}}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.f.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := tt.f.Match(c); got != tt.want {
				t.Fatalf("Match = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNamesFoldASCIIOnly(t *testing.T) {
	c := mustCard(t, "NICKNAME;X-K=1:Ada", "SOURCE:x")
	if got := c.Find("nic\u212aname"); got != nil {
		t.Errorf("Find with a Kelvin sign = %#v, want nil", got)
	}
	if got := c.Find("ſource"); got != nil {
		t.Errorf("Find with a long s = %#v, want nil", got)
	}
	if got := c.Find("nickName"); len(got) != 1 {
		t.Errorf("Find(nickName) = %#v", got)
	}
	tests := []struct {
		name string
		f    *Filter
		want bool
	}{
		{"prop name with a Kelvin sign", &Filter{Props: []PropFilter{{Name: "NIC\u212aNAME"}}}, false},
		{"param name", &Filter{Props: []PropFilter{{Name: "NICKNAME", Params: []ParamFilter{{Name: "x-k"}}}}}, true},
		{"param name with a Kelvin sign", &Filter{Props: []PropFilter{{Name: "NICKNAME",
			Params: []ParamFilter{{Name: "X-\u212a"}}}}}, false},
	}
	for _, tt := range tests {
		if got := tt.f.Match(c); got != tt.want {
			t.Errorf("%s: Match = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFilterConstants(t *testing.T) {
	got := []string{TestAnyOf, TestAllOf, MatchEquals, MatchContains, MatchStartsWith, MatchEndsWith}
	want := []string{"anyof", "allof", "equals", "contains", "starts-with", "ends-with"}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("constant %d = %q, want %q", i, got[i], want[i])
		}
	}
	f := &Filter{Test: TestAllOf, Props: []PropFilter{{Name: "EMAIL", Test: TestAnyOf,
		TextMatches: []TextMatch{{Value: "ada@", MatchType: MatchStartsWith}}}}}
	if err := f.Validate(); err != nil || !f.Match(mustCard(t, "EMAIL:ada@example.org")) {
		t.Fatalf("Validate = %v, or no match", err)
	}
}

func TestFilterErrorsTruncate(t *testing.T) {
	long := strings.Repeat("x", 900000)
	for _, f := range []*Filter{
		{Test: long},
		{Props: []PropFilter{{Name: "EMAIL", Test: long}}},
		{Props: []PropFilter{{Name: long, IsNotDefined: true, TextMatches: []TextMatch{{}}}}},
		{Props: []PropFilter{{Name: "EMAIL", Params: []ParamFilter{{Name: long, IsNotDefined: true, TextMatch: &TextMatch{}}}}}},
		emailFilter(TextMatch{MatchType: long}),
		emailFilter(TextMatch{Collation: long}),
	} {
		err := f.Validate()
		if !errors.Is(err, ErrFilter) {
			t.Fatalf("err = %v, want ErrFilter", err)
		}
		if n := len(err.Error()); n >= 300 {
			t.Fatalf("error is %d bytes long", n)
		}
	}
}
