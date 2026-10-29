// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package groupkit is a toolkit for calendars, contacts and tasks: CalDAV and
// CardDAV servers, iCalendar and vCard handling, and storage you can embed in
// your own service.
//
// The module is split into packages so a program imports only what it needs:
//
//   - store: the storage interface, with an in-memory store, a file-backed
//     store, and a conformance suite every store must pass
//   - ical and vcard: parsing, validation and query matching
//   - caldav and carddav: protocol engines that serve a store over HTTP
//
// The API is not stable yet.
package groupkit
