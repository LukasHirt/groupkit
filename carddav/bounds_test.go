// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/internal/dav"
)

const dupData = `<CR:address-data/>`

func countAddressData(r reply, i int) int {
	n := 0
	for _, p := range r.props[i][200] {
		if p.Name == dav.AddressData {
			n++
		}
	}
	return n
}

// reportBodies returns report bodies that ask for the properties given.
func reportBodies(props string) map[string]string {
	return map[string]string{
		"query": `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:prop>` + props + `</D:prop></CR:addressbook-query>`,
		"multiget": `<?xml version="1.0"?><CR:addressbook-multiget ` + nsDecl + `><D:prop>` + props + `</D:prop>` +
			hrefEl("/dav/addressbooks/alice/contacts/ada.vcf") + hrefEl("/dav/addressbooks/alice/contacts/bob.vcf") +
			hrefEl("/dav/addressbooks/alice/contacts/carl.vcf") + `</CR:addressbook-multiget>`,
		"sync": syncBody("", "1", props, ""),
	}
}

func TestReportDuplicatePropertiesAnsweredOnce(t *testing.T) {
	props := `<D:getetag/>` + strings.Repeat(dupData, 20) + `<D:getetag/>`
	for name, body := range reportBodies(props) {
		t.Run(name, func(t *testing.T) {
			f := newReportFixture(t)
			f.st.gets.Store(0)
			rec := f.report(bookURL, body)
			r := parseReply(t, rec)
			if len(r.hrefs) != 3 {
				t.Fatalf("hrefs = %v", r.hrefs)
			}
			for i := range r.hrefs {
				if n := countAddressData(r, i); n != 1 {
					t.Errorf("response %d has %d address-data, want 1", i, n)
				}
			}
			if n := strings.Count(rec.Body.String(), "getetag"); n != 3*2 {
				t.Errorf("%d getetag tags, want one element per response", n)
			}
			if n := f.st.gets.Load(); n > 3 {
				t.Errorf("GetObject called %d times for 3 objects", n)
			}
		})
	}
}

func TestReportDuplicateIncludeAnsweredOnce(t *testing.T) {
	inc := `<D:allprop/><D:include>` + strings.Repeat(dupData, 20) + `</D:include>`
	body := `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `>` + inc + `</CR:addressbook-query>`
	f := newReportFixture(t)
	r := parseReply(t, f.report(bookURL, body))
	for i := range r.hrefs {
		if n := countAddressData(r, i); n != 1 {
			t.Errorf("response %d has %d address-data, want 1", i, n)
		}
	}
}

func TestReportTooManyDistinctProperties(t *testing.T) {
	names := func(n int) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, `<X:p%d xmlns:X="urn:x"/>`, i)
		}
		return b.String()
	}
	f := newReportFixture(t)
	for name, body := range reportBodies(names(1001)) {
		if rec := f.report(bookURL, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: 1001 names: status %d, want 400", name, rec.Code)
		}
	}
	for name, body := range reportBodies(names(1000)) {
		if rec := f.report(bookURL, body); rec.Code != http.StatusMultiStatus {
			t.Errorf("%s: 1000 names: status %d", name, rec.Code)
		}
	}
	// Duplicates do not count towards the cap.
	dups := strings.Repeat(`<X:p xmlns:X="urn:x"/>`, 5000)
	for name, body := range reportBodies(dups) {
		if rec := f.report(bookURL, body); rec.Code != http.StatusMultiStatus {
			t.Errorf("%s: duplicates: status %d", name, rec.Code)
		}
	}
	// The include list of an allprop request is capped the same way.
	inc := `<?xml version="1.0"?><CR:addressbook-query ` + nsDecl + `><D:allprop/><D:include>` + names(1001) + `</D:include></CR:addressbook-query>`
	if rec := f.report(bookURL, inc); rec.Code != http.StatusBadRequest {
		t.Errorf("include: status %d, want 400", rec.Code)
	}
}

func TestTextMatchSizeLimits(t *testing.T) {
	tm := func(n int) string {
		return `<CR:text-match>` + strings.Repeat("a", n) + `</CR:text-match>`
	}
	pf := func(inner string) string {
		return query(filter("", `<CR:prop-filter name="FN">`+inner+`</CR:prop-filter>`))
	}
	f := newReportFixture(t)
	check := func(name, body string, want int) {
		t.Helper()
		rec := f.report(bookURL, body)
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", name, rec.Code, want)
		}
		if want == http.StatusForbidden && !strings.Contains(rec.Body.String(), "valid-filter") {
			t.Errorf("%s: body %s", name, rec.Body)
		}
	}
	check("8 KiB value", pf(tm(8192)), http.StatusMultiStatus)
	check("8 KiB + 1 value", pf(tm(8193)), http.StatusForbidden)
	check("64 KiB total", pf(strings.Repeat(tm(8192), 8)), http.StatusMultiStatus)
	check("64 KiB + 1 total", pf(strings.Repeat(tm(8192), 8)+tm(1)), http.StatusForbidden)
	// The total counts across prop-filters and parameter filters.
	body := func(extra string) string {
		return query(filter("", `<CR:prop-filter name="FN">`+strings.Repeat(tm(8192), 4)+`</CR:prop-filter>`+
			`<CR:prop-filter name="N">`+strings.Repeat(tm(8192), 3)+`<CR:param-filter name="X">`+tm(8192)+`</CR:param-filter></CR:prop-filter>`+extra))
	}
	check("total across filters", body(""), http.StatusMultiStatus)
	check("total across filters + 1", body(`<CR:prop-filter name="X">`+tm(1)+`</CR:prop-filter>`), http.StatusForbidden)
}

type countingReader struct{ n int }

func (c *countingReader) Read(p []byte) (int, error) { c.n++; return 0, fmt.Errorf("read") }

func TestPutWrongMediaTypeIsAnsweredWithoutReadingTheBody(t *testing.T) {
	f := memFixture(t)
	body := &countingReader{}
	req := httptest.NewRequest("PUT", objBase+"x.vcf", body)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType || body.n != 0 {
		t.Errorf("status %d, body read %d times", rec.Code, body.n)
	}
}
