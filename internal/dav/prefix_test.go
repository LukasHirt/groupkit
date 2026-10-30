// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"strings"
	"testing"
)

func TestValidPrefix(t *testing.T) {
	valid := []string{"", "/dav", "/a/b", "/dav/sub", "/my-dav_1.0~x", "/a.b"}
	for _, p := range valid {
		if err := ValidPrefix(p); err != nil {
			t.Errorf("ValidPrefix(%q) = %v, want nil", p, err)
		}
	}
	invalid := []string{
		"dav", "/", "/dav/", "//evil.com", "//dav", "/a\r\nb", "/a\x00b", "/a\x7fb", "/my dav",
		"/./dav", "/dav/../x", "/dav/.", "/a//b", "/d%61v", "/dav?x", "/dav#x", `/da\v`,
		"/dáv", "/a\xffb", "/a;b", "/a:b", "/a@b",
		"/a$b", "/a&b", "/a+b", "/a=b", "/a!b", "/a'b", "/a(b", "/a)b", "/a*b", "/a,b",
	}
	for _, p := range invalid {
		err := ValidPrefix(p)
		if err == nil {
			t.Errorf("ValidPrefix(%q) = nil, want an error", p)
			continue
		}
		if strings.ContainsAny(err.Error(), "\r\n\x00") {
			t.Errorf("error for %q echoes control characters: %q", p, err)
		}
	}
}
