// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import "strings"

// ComplianceHeader builds the value of the DAV response header: the base
// classes "1" and "3" followed by the extra classes a protocol adds.
func ComplianceHeader(extra ...string) string {
	return strings.Join(append([]string{"1", "3"}, extra...), ", ")
}

// CardDAVClasses returns the classes the CardDAV handler adds to the DAV
// header.
func CardDAVClasses() []string { return []string{"addressbook", "extended-mkcol"} }

// CalDAVClasses returns the classes the CalDAV handler adds to the DAV
// header.
func CalDAVClasses() []string { return []string{"calendar-access", "extended-mkcol"} }
