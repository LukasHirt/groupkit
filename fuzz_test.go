// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit"
)

// FuzzServer sends arbitrary methods, paths and bodies to a server holding a
// few contacts. It must never panic and never answer 5xx.
func FuzzServer(f *testing.F) {
	srv, err := groupkit.NewServer(testConfig("/dav"))
	if err != nil {
		f.Fatal(err)
	}
	h := srv.Handler()
	for _, tc := range [][2]string{
		{"MKCOL", "/dav/addressbooks/alice/b/"},
		{"PUT", "/dav/addressbooks/alice/b/a.vcf"},
	} {
		body := ""
		if tc[0] == "PUT" {
			body = contactA
		}
		r := httptest.NewRequest(tc[0], tc[1], strings.NewReader(body))
		r.Header.Set(userHeader, "alice")
		h.ServeHTTP(httptest.NewRecorder(), r)
	}

	f.Add("PROPFIND", "/dav/", propfindBody)
	f.Add("PROPFIND", "/dav/principals/alice/", "")
	f.Add("PROPFIND", "/dav/addressbooks/alice/b/", propfindBody)
	f.Add("REPORT", "/dav/addressbooks/alice/b/", `<D:sync-collection xmlns:D="DAV:"><D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop></D:sync-collection>`)
	f.Add("OPTIONS", "*", "")
	f.Add("GET", "/dav/addressbooks/alice/b/a.vcf", "")
	f.Add("PUT", "/dav/addressbooks/alice/b/%e4.vcf", "BEGIN:VCARD")
	f.Add("DELETE", "/dav/principals/%2e%2e/", "")
	f.Add("PROPFIND", "/dav/principals/bob/", propfindBody)

	f.Fuzz(func(t *testing.T, method, path, body string) {
		if !validMethod(method) || !strings.HasPrefix(path, "/") && path != "*" {
			t.Skip()
		}
		r, err := http.NewRequest(method, "http://example.test/", strings.NewReader(body))
		if err != nil {
			t.Skip()
		}
		u, err := r.URL.Parse(path)
		if err != nil {
			t.Skip()
		}
		r.URL = u
		r.Header.Set(userHeader, "alice")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code >= 500 {
			t.Fatalf("%s %q = %d: %s", method, path, w.Code, w.Body)
		}
	})
}

// validMethod reports whether m is an HTTP token.
func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for _, c := range m {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune(`()<>@,;:\"/[]?={}`, c) {
			return false
		}
	}
	return true
}
