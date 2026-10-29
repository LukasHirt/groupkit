// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package contentline

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		"FN:Ada Lovelace\r\n",
		"A:1\r\nB:2\nC:3\r\n",
		"\xef\xbb\xbfFN:Ada\r\n",
		"\r\n\nA:1\r\n\r\n",
		"FN:Ada\r\n  Lovelace\r\n",
		"FN:\xc5\r\n \xa1\r\n",
		"item1.EMAIL;TYPE=work,home;PREF=1:ada@example.org\r\n",
		`X-FOO;X-BAR="a:b;c,d":v` + "\r\n",
		"URL:https://example.org:8080/x",
		"FN\r\n",
		":value",
		`B;P="x:v`,
		" FN:x",
		"a.b.c:x",
		"\u017fUMMARY:x",
		"A;P Q=x:v",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		limits := Limits{MaxLines: 1000, MaxLineBytes: 4096}
		lines, err := Parse(data, limits)
		if err != nil {
			return
		}
		for _, l := range lines {
			names := []string{l.Name}
			if l.Group != "" {
				names = append(names, l.Group)
			}
			for _, p := range l.Params {
				names = append(names, p.Name)
			}
			for _, n := range names {
				if !isToken(n) || n != strings.ToUpper(n) {
					t.Fatalf("name %q is not an upper-case token in %#v", n, l)
				}
			}
			if len(l.Value) > limits.MaxLineBytes {
				t.Fatalf("value of %d bytes exceeds the limit", len(l.Value))
			}
		}
	})
}
