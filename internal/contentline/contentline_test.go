// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package contentline

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []Line
	}{
		{"simple", "FN:Ada Lovelace\r\n", []Line{{Name: "FN", Value: "Ada Lovelace"}}},
		{"no trailing newline", "FN:Ada", []Line{{Name: "FN", Value: "Ada"}}},
		{"lowercase name", "fn:Ada\n", []Line{{Name: "FN", Value: "Ada"}}},
		{"CRLF", "A:1\r\nB:2\r\n", []Line{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}},
		{"LF", "A:1\nB:2\n", []Line{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}},
		{"mixed", "A:1\r\nB:2\nC:3\r\n", []Line{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}, {Name: "C", Value: "3"}}},
		{"BOM", "\xef\xbb\xbfFN:Ada\r\n", []Line{{Name: "FN", Value: "Ada"}}},
		{"blank lines", "\r\n\nA:1\r\n\r\n\nB:2\r\n\r\n", []Line{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}},
		{"space fold", "FN:Ada\r\n  Lovelace\r\n", []Line{{Name: "FN", Value: "Ada Lovelace"}}},
		{"tab fold", "FN:Ada\r\n\tLovelace\r\n", []Line{{Name: "FN", Value: "AdaLovelace"}}},
		{"LF fold", "FN:Ada\n Lovelace\n", []Line{{Name: "FN", Value: "AdaLovelace"}}},
		{"fold in UTF-8 character", "FN:\xc5\r\n \xa1\r\n", []Line{{Name: "FN", Value: "š"}}},
		{"fold inside name", "F\r\n N:x\r\n", []Line{{Name: "FN", Value: "x"}}},
		{
			"group and params",
			"item1.EMAIL;TYPE=work,home;PREF=1:ada@example.org\r\n",
			[]Line{{
				Group: "ITEM1", Name: "EMAIL",
				Params: []Param{{Name: "TYPE", Values: []string{"work", "home"}}, {Name: "PREF", Values: []string{"1"}}},
				Value:  "ada@example.org",
			}},
		},
		{
			"quoted parameter value",
			`X-FOO;X-BAR="a:b;c,d":v` + "\r\n",
			[]Line{{Name: "X-FOO", Params: []Param{{Name: "X-BAR", Values: []string{"a:b;c,d"}}}, Value: "v"}},
		},
		{
			"quoted and unquoted values",
			`X;P="a,b",c,"d":v`,
			[]Line{{Name: "X", Params: []Param{{Name: "P", Values: []string{"a,b", "c", "d"}}}, Value: "v"}},
		},
		{
			"empty parameter value",
			"X;P=:v",
			[]Line{{Name: "X", Params: []Param{{Name: "P", Values: []string{""}}}, Value: "v"}},
		},
		{"extra colons", "URL:https://example.org:8080/x", []Line{{Name: "URL", Value: "https://example.org:8080/x"}}},
		{"empty value", "NOTE:", []Line{{Name: "NOTE", Value: ""}}},
		{"raw value", `NOTE:a\,b\nc;d`, []Line{{Name: "NOTE", Value: `a\,b\nc;d`}}},
		{"empty input", "", nil},
		{
			"every quote removed",
			`X;CN=John "J" Doe:v`,
			[]Line{{Name: "X", Params: []Param{{Name: "CN", Values: []string{"John J Doe"}}}, Value: "v"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.in), DefaultLimits)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line string
	}{
		{"no colon", "A:1\r\nFN\r\n", "line 2"},
		{"empty name", "A:1\r\n:value\r\n", "line 2"},
		{"empty name with group", "A:1\r\nx.:value\r\n", "line 2"},
		{"unterminated quote", "A:1\r\nB;P=\"x:v\r\n", "line 2"},
		{"leading continuation", " FN:x\r\n", "line 1"},
		{"continuation after blank", "A:1\r\n\r\n x\r\n", "line 3"},
		{"parameter without equals", "A;P:v\r\n", "line 1"},
		{"empty parameter name", "A;=x:v\r\n", "line 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in), DefaultLimits)
			if !errors.Is(err, ErrSyntax) {
				t.Fatalf("err = %v, want ErrSyntax", err)
			}
			if !strings.Contains(err.Error(), tt.line+":") {
				t.Fatalf("err = %q, want it to contain %q", err, tt.line)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	t.Run("too many lines", func(t *testing.T) {
		_, err := Parse([]byte("A:1\r\nB:2\r\nC:3\r\n"), Limits{MaxLines: 2, MaxLineBytes: 100})
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("err = %v, want ErrSyntax", err)
		}
		if _, err := Parse([]byte("A:1\r\nB:2\r\n"), Limits{MaxLines: 2, MaxLineBytes: 100}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})
	t.Run("line too long", func(t *testing.T) {
		_, err := Parse([]byte("A:"+strings.Repeat("x", 20)+"\r\n"), Limits{MaxLines: 10, MaxLineBytes: 10})
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("err = %v, want ErrSyntax", err)
		}
	})
	t.Run("folded line too long", func(t *testing.T) {
		in := "A:xxxx\r\n xxxx\r\n xxxx\r\n"
		_, err := Parse([]byte(in), Limits{MaxLines: 10, MaxLineBytes: 10})
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("err = %v, want ErrSyntax", err)
		}
	})
	t.Run("line at the limit", func(t *testing.T) {
		if _, err := Parse([]byte("A:12345678"), Limits{MaxLines: 10, MaxLineBytes: 10}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})
}

func TestLineParam(t *testing.T) {
	l := Line{Name: "EMAIL", Params: []Param{{Name: "TYPE", Values: []string{"work", "home"}}}}
	if got := l.Param("type"); !reflect.DeepEqual(got, []string{"work", "home"}) {
		t.Fatalf("Param(type) = %v", got)
	}
	if got := l.Param("PREF"); got != nil {
		t.Fatalf("Param(PREF) = %v, want nil", got)
	}
}

func TestUpperASCII(t *testing.T) {
	tests := []struct{ in, want string }{
		{"summary", "SUMMARY"},
		{"x-Foo-1", "X-FOO-1"},
		{"ſummary", "ſUMMARY"}, // U+017F would become S under strings.ToUpper
		{"ıd", "ıD"},           // U+0131 would become I under strings.ToUpper
		{"ß", "ß"},
		{"K", "K"}, // Kelvin sign
	}
	for _, tt := range tests {
		if got := upperASCII(tt.in); got != tt.want {
			t.Errorf("upperASCII(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLineParamASCIIOnly(t *testing.T) {
	l := Line{Name: "X", Params: []Param{
		{Name: "K", Values: []string{"k"}},
		{Name: "S", Values: []string{"s"}},
		{Name: "I", Values: []string{"i"}},
		{Name: "SS", Values: []string{"ss"}},
	}}
	for _, name := range []string{"K", "ſ", "ı", "ß"} {
		if got := l.Param(name); got != nil {
			t.Errorf("Param(%q) = %v, want nil", name, got)
		}
	}
	for name, want := range map[string]string{"k": "k", "s": "s", "i": "i", "sS": "ss"} {
		if got := l.Param(name); len(got) != 1 || got[0] != want {
			t.Errorf("Param(%q) = %v, want [%s]", name, got, want)
		}
	}
}

func TestParseRejectsNonTokenNames(t *testing.T) {
	tests := []struct{ name, in string }{
		{"dotless i in name", "ıD:x"},
		{"long s in name", "ſUMMARY:x"},
		{"sharp s in name", "ßX:x"},
		{"Kelvin sign in name", "K:x"},
		{"space in name", "F N:x"},
		{"trailing space in name", "FN :x"},
		{"quote in name", `"FN":x`},
		{"NUL in name", "F\x00N:x"},
		{"control in name", "F\x01N:x"},
		{"tab in name", "F\tN:x"},
		{"underscore in name", "X_A:x"},
		{"second dot", "a.b.c:x"},
		{"space in group", "it em.FN:x"},
		{"non-ASCII group", "ité.FN:x"},
		{"NUL in group", "a\x00.FN:x"},
		{"space in parameter name", "A;P Q=x:v"},
		{"quote in parameter name", `A;"P"=x:v`},
		{"non-ASCII parameter name", "A;K=x:v"},
		{"dot in parameter name", "A;P.Q=x:v"},
		{"NUL in parameter name", "A;P\x00=x:v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in), DefaultLimits)
			if !errors.Is(err, ErrSyntax) {
				t.Fatalf("err = %v, want ErrSyntax", err)
			}
		})
	}
	if _, err := Parse([]byte("item-1.X-Foo-2;X-p-3=v:x"), DefaultLimits); err != nil {
		t.Fatalf("valid tokens: %v", err)
	}
}

func TestPhysicalLines(t *testing.T) {
	t.Run("continuations count toward MaxLines", func(t *testing.T) {
		in := "A:1\r\n x\r\n y\r\n"
		if _, err := Parse([]byte(in), Limits{MaxLines: 2, MaxLineBytes: 100}); !errors.Is(err, ErrSyntax) {
			t.Fatalf("err = %v, want ErrSyntax", err)
		}
		if _, err := Parse([]byte(in), Limits{MaxLines: 3, MaxLineBytes: 100}); err != nil {
			t.Fatalf("at the limit: %v", err)
		}
	})
	t.Run("a million empty continuations", func(t *testing.T) {
		in := "A:1\n" + strings.Repeat(" \n", 1000000)
		_, err := Parse([]byte(in), DefaultLimits)
		if !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "more than") {
			t.Fatalf("err = %v, want a MaxLines error", err)
		}
	})
	loose := Limits{MaxLines: 100, MaxLineBytes: 10}
	tests := []struct {
		name, in, line string
		limits         Limits
	}{
		{"after a folded property", "A:x\r\n y\r\n z\r\nBROKEN\r\n", "line 4:", loose},
		{"after a folded property, LF", "A:x\n y\nB:1\nBROKEN\n", "line 4:", loose},
		{"in a folded property", "A:1\r\nB\r\n C\r\n", "line 2:", loose},
		{"after blank lines", "A:1\r\n\r\n\r\nBROKEN\r\n", "line 4:", loose},
		{"too long continuation", "A:1\r\nB:xxxx\r\n xxxx\r\n xxxx\r\n", "line 4:", loose},
		{"MaxLines on a continuation", "A:1\r\n x\r\n y\r\n", "line 3:", Limits{MaxLines: 2, MaxLineBytes: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in), tt.limits)
			if !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), tt.line) {
				t.Fatalf("err = %v, want ErrSyntax at %s", err, tt.line)
			}
		})
	}
}

func TestZeroLimitsMeanDefaults(t *testing.T) {
	in := "A:1\r\n" + "B:" + strings.Repeat("x", 2000) + "\r\n"
	got, err := Parse([]byte(in), Limits{})
	if err != nil || len(got) != 2 {
		t.Fatalf("Parse with zero limits = %d lines, %v", len(got), err)
	}
	if _, err := Parse([]byte(in), Limits{MaxLines: 1}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("MaxLines 1: err = %v, want ErrSyntax", err)
	}
	if _, err := Parse([]byte(in), Limits{MaxLineBytes: 100}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("MaxLineBytes 100: err = %v, want ErrSyntax", err)
	}
	if DefaultLimits.MaxLineBytes != 16<<20 || DefaultLimits.MaxLines != 100000 {
		t.Fatalf("DefaultLimits = %+v", DefaultLimits)
	}
}

func TestErrorsTruncateFragments(t *testing.T) {
	long := strings.Repeat("\u00e9", 450000) // 900 KB
	for _, in := range []string{
		long + ":x",
		long + ".FN:x",
		"FN;" + long + "=v:x",
	} {
		_, err := Parse([]byte(in), DefaultLimits)
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("err = %v, want ErrSyntax", err)
		}
		if n := len(err.Error()); n >= 300 {
			t.Fatalf("error is %d bytes long", n)
		}
		if !strings.Contains(err.Error(), "...") {
			t.Fatalf("error %q has no truncation marker", err)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("short"); got != "short" {
		t.Fatalf("Clip(short) = %q", got)
	}
	exact := strings.Repeat("x", 40)
	if got := Clip(exact); got != exact {
		t.Fatalf("Clip of 40 bytes = %q", got)
	}
	if got := Clip(strings.Repeat("x", 41)); got != exact+"..." {
		t.Fatalf("Clip of 41 bytes = %q", got)
	}
	// Never cut inside a multi-byte character.
	got := Clip("x" + strings.Repeat("\u00e9", 30))
	if got != "x"+strings.Repeat("\u00e9", 19)+"..." {
		t.Fatalf("Clip cut a character: %q", got)
	}
}
