// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package collation

import (
	"errors"
	"sync"
	"testing"
)

func mustGet(t *testing.T, name string) Collation {
	t.Helper()
	c, err := Get(name)
	if err != nil {
		t.Fatalf("Get(%q): %v", name, err)
	}
	return c
}

func TestCollations(t *testing.T) {
	type op struct {
		method string
		a, b   string
		want   bool
	}
	tests := []struct {
		name string
		ops  []op
	}{
		{"i;octet", []op{
			{"equal", "Ada", "Ada", true},
			{"equal", "Ada", "ada", false},
			{"contains", "Ada", "ada", false},
			{"contains", "Ada Lovelace", "Love", true},
			{"prefix", "Ada", "Ad", true},
			{"prefix", "Ada", "ad", false},
			{"suffix", "Ada", "da", true},
			{"suffix", "Ada", "DA", false},
			{"contains", "Ada", "", true},
			{"prefix", "Ada", "", true},
			{"suffix", "Ada", "", true},
		}},
		{"i;ascii-casemap", []op{
			{"equal", "ADA", "ada", true},
			{"equal", "ÄDA", "äda", false},
			{"equal", "ÄDA", "ÄdA", true},
			{"contains", "Ada Lovelace", "LOVE", true},
			{"contains", "ÄDA", "äda", false},
			{"prefix", "ADA", "ad", true},
			{"prefix", "ÄDA", "äd", false},
			{"suffix", "ADA", "dA", true},
			{"suffix", "ADÄ", "dä", false},
			{"contains", "Ada", "", true},
			{"prefix", "Ada", "", true},
			{"suffix", "Ada", "", true},
		}},
		{"i;unicode-casemap", []op{
			{"equal", "ÄDA", "äda", true},
			{"equal", "Straße", "STRASSE", true},
			{"equal", "Ada", "Bob", false},
			{"contains", "Ada Lovelace", "LOVE", true},
			{"contains", "ÄDA Lovelace", "äda", true},
			{"contains", "Ada", "Bob", false},
			{"prefix", "ÄDA", "äd", true},
			{"prefix", "ADA", "da", false},
			{"suffix", "Straße", "SSE", true},
			{"suffix", "ADA", "ad", false},
			{"contains", "Ada", "", true},
			{"prefix", "Ada", "", true},
			{"suffix", "Ada", "", true},
		}},
	}
	for _, tt := range tests {
		c := mustGet(t, tt.name)
		if c.Name() != tt.name {
			t.Errorf("Name() = %q, want %q", c.Name(), tt.name)
		}
		for _, o := range tt.ops {
			var got bool
			switch o.method {
			case "equal":
				got = c.Equal(o.a, o.b)
			case "contains":
				got = c.Contains(o.a, o.b)
			case "prefix":
				got = c.HasPrefix(o.a, o.b)
			case "suffix":
				got = c.HasSuffix(o.a, o.b)
			}
			if got != o.want {
				t.Errorf("%s %s(%q, %q) = %v, want %v", tt.name, o.method, o.a, o.b, got, o.want)
			}
		}
	}
}

func TestGet(t *testing.T) {
	def := mustGet(t, "")
	if def.Name() != "i;unicode-casemap" {
		t.Errorf("default = %q, want i;unicode-casemap", def.Name())
	}
	if !def.Equal("Straße", "STRASSE") {
		t.Error("default collation does not fold Unicode")
	}
	_, err := Get("i;bogus")
	if !errors.Is(err, ErrUnknown) {
		t.Errorf("Get(i;bogus) error = %v, want ErrUnknown", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	for _, name := range []string{"i;octet", "i;ascii-casemap", "i;unicode-casemap"} {
		c := mustGet(t, name)
		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				want := name == "i;unicode-casemap"
				for range 50 {
					if c.Equal("Straße", "STRASSE") != want || c.HasPrefix("ÄDA", "äd") != want ||
						c.HasSuffix("Straße", "SSE") != want {
						t.Errorf("%s: wrong result under concurrent use", name)
						return
					}
					if c.Contains("Ada Lovelace", "LOVE") != (name != "i;octet") {
						t.Errorf("%s: wrong Contains result under concurrent use", name)
						return
					}
				}
			}()
		}
		wg.Wait()
	}
}

func TestGetCaseInsensitiveName(t *testing.T) {
	tests := map[string]string{
		"I;OCTET":           "i;octet",
		"i;Octet":           "i;octet",
		"I;ASCII-CASEMAP":   "i;ascii-casemap",
		"i;Unicode-Casemap": "i;unicode-casemap",
		"I;UNICODE-CASEMAP": "i;unicode-casemap",
	}
	for in, want := range tests {
		c, err := Get(in)
		if err != nil {
			t.Errorf("Get(%q): %v", in, err)
			continue
		}
		if c.Name() != want {
			t.Errorf("Get(%q).Name() = %q, want %q", in, c.Name(), want)
		}
	}
	if c := mustGet(t, "I;OCTET"); c.Equal("Ada", "ada") {
		t.Error("I;OCTET folds case")
	}
	for _, in := range []string{" i;octet", "i;octet ", "\ti;octet", "i;octet\n", " ", "i;KCTET"} {
		if _, err := Get(in); !errors.Is(err, ErrUnknown) {
			t.Errorf("Get(%q) error = %v, want ErrUnknown", in, err)
		}
	}
}

func TestInvalidUTF8(t *testing.T) {
	// Invalid bytes are compared as they are, in every collation, and never
	// collapse into one replacement character.
	for _, name := range []string{"i;octet", "i;ascii-casemap", "i;unicode-casemap"} {
		c := mustGet(t, name)
		checks := []struct {
			got, want bool
			what      string
		}{
			{c.Equal("\xff", "\xff"), true, `Equal("\xff", "\xff")`},
			{c.Equal("\xff", "\xfe"), false, `Equal("\xff", "\xfe")`},
			{c.Contains("a\xffb", "\xff"), true, `Contains("a\xffb", "\xff")`},
			{c.Contains("a\xffb", "\xfe"), false, `Contains("a\xffb", "\xfe")`},
			{c.HasPrefix("\xc5ada", "\xc5"), true, `HasPrefix("\xc5ada", "\xc5")`},
			{c.HasSuffix("ada\xc5", "a\xc5"), true, `HasSuffix("ada\xc5", "a\xc5")`},
			{c.HasSuffix("ada\xc5", "\xa1"), false, `HasSuffix("ada\xc5", "\xa1")`},
		}
		for _, ch := range checks {
			if ch.got != ch.want {
				t.Errorf("%s %s = %v, want %v", name, ch.what, ch.got, ch.want)
			}
		}
	}
	if !mustGet(t, "i;ascii-casemap").Equal("\xffADA", "\xffada") {
		t.Error("ascii-casemap does not fold ASCII next to invalid bytes")
	}
	if !mustGet(t, "i;unicode-casemap").Equal("\xffADA", "\xffada") {
		t.Error("unicode-casemap does not fold ASCII next to invalid bytes")
	}
}

func TestLengthChangingFold(t *testing.T) {
	c := mustGet(t, "i;unicode-casemap")
	checks := []struct {
		got, want bool
		what      string
	}{
		{c.Contains("İstanbul", "i̇s"), true, `Contains("İstanbul", "i̇s")`},
		{c.HasPrefix("İstanbul", "i̇"), true, `HasPrefix("İstanbul", "i̇")`},
		{c.HasSuffix("Straße", "SSE"), true, `HasSuffix("Straße", "SSE")`},
		{c.Contains("STRASSE", "ß"), true, `Contains("STRASSE", "ß")`},
		{c.Contains("Straße", "aSs"), true, `Contains("Straße", "aSs")`},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %v, want %v", ch.what, ch.got, ch.want)
		}
	}
	a := mustGet(t, "i;ascii-casemap")
	if a.HasSuffix("Straße", "SSE") || a.Contains("İstanbul", "i̇s") {
		t.Error("ascii-casemap matched across a non-ASCII fold")
	}
}
