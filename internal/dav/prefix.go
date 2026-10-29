// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ValidPrefix checks a URL path prefix such as "/dav". The empty prefix is
// valid. Any other must start with a single slash, must not end with one, and
// must be a clean path of plain ASCII segments: what the operator writes is
// exactly what appears in a request's escaped path, and it can never be read
// as a host ("//host"), a query, a fragment or an escape. Each segment must
// be left unchanged by url.PathEscape, which rejects spaces, "%", "?", "#",
// ";", ":", "@", control characters and non-ASCII text.
func ValidPrefix(p string) error {
	if p == "" {
		return nil
	}
	switch {
	case !strings.HasPrefix(p, "/"):
		return fmt.Errorf("prefix %q must be empty or start with a slash", p)
	case strings.HasPrefix(p, "//"):
		return fmt.Errorf("prefix %q must not start with two slashes", p)
	case strings.HasSuffix(p, "/"):
		return fmt.Errorf("prefix %q must not end with a slash", p)
	case path.Clean(p) != p:
		return fmt.Errorf("prefix %q must be a clean path", p)
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if url.PathEscape(seg) != seg || strings.ContainsAny(seg, ";:@") {
			return fmt.Errorf("prefix %q must contain only unreserved ASCII path characters", p)
		}
	}
	return nil
}
