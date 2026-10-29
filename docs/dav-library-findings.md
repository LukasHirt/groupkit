<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# go-webdav findings

Results of a spike that wired `github.com/emersion/go-webdav` v0.7.0 (with
`go-ical` and `go-vcard`) to an in-memory backend and probed it with scripted
clients. Its goal was to find out how much of a CalDAV and CardDAV protocol layer the
library can carry.

## Scope and limits

The server under test used this URL layout
(`/dav/principals/{user}/`, `/dav/calendars/{user}/{calendar}/`,
`/dav/addressbooks/{user}/{book}/`) with HTTP Basic auth. It was probed with
curl, a Python script and the Python `caldav` client (3.5.0). A second pass
read the library's server code and checked each behavior against what Thunderbird,
Apple Calendar and Contacts, DAVx⁵ and Evolution are known to request.

Not done: no GUI client was connected. The behavior of Thunderbird, Apple
Calendar, DAVx⁵ and Evolution is inferred from the protocol and from what those
clients are known to ask for. Connecting them is still open.

## Summary

The library handles object-level requests well enough: `PUT`, `DELETE`,
`calendar-multiget`, `addressbook-multiget`, `addressbook-query`, and the 207
encoding for those. Almost everything around collections and discovery is missing
or wrong, and the calendar time-range matcher has correctness bugs. A server built
on it needs its own handlers for principals, collections, `PROPPATCH`,
`MKCALENDAR`, `sync-collection` and `.well-known`, its own matcher for
`calendar-query`, and middleware for limits.

`lib/internal` is a sealed Go package, so code outside the module can't reuse the
library's multistatus types or property rendering. Anything added in front of the
library has to produce its own 207 XML.

## What works

- Discovery: `current-user-principal` on every resource, calendar and address
  book home sets on the principal, and `OPTIONS` advertising `calendar-access` and
  `addressbook`. The library's principal helper can serve both home sets from one
  principal.
- The planned URL layout works with the handler prefix set to `/dav`: the home set
  is `/dav/calendars/{user}/`, a calendar is `/dav/calendars/{user}/{calendar}/`,
  and objects sit below it. Only the principal at `/dav/principals/{user}/` needs a
  separate handler, because the library treats the second path segment as the home
  set.
- Conditional `PUT`: `If-None-Match: *` and a stale `If-Match` return 412, provided
  the backend returns `webdav.NewHTTPError(412, ...)`. The library passes the
  headers through and doesn't evaluate them.
- `calendar-multiget` (including a 404 for a missing href), `addressbook-query` with
  `starts-with` and `equals`, and vCard 3.0 and 4.0 `PUT`.
- The Python client completed discovery, listing calendars, saving an event,
  finding it by UID and searching it by date range.
- Recurrence in time-range queries mostly works: weekly rules, `EXDATE`, `COUNT`,
  `UNTIL`, monthly rules that skip short months, floating times, `DURATION`, events
  without `DTEND`, and daylight saving changes with a proper `VTIMEZONE`. A suite
  of 19 edge cases passed 17; the two failures and the extra probes are listed
  below.
- Throughput is not a concern for the library layer. With 3000 events in memory,
  `calendar-query` returned in under 10 ms, a 100-object `multiget` in 2 ms, and a
  `PROPFIND` of etags in 8 ms.

## Gaps

Severity reflects likely impact on real clients. "Where to fix" says whether the
fix fits in a wrapper or backend around the library, or needs a fork.

| # | Finding | Evidence | Severity | Where to fix |
|---|---|---|---|---|
| 1 | No `sync-collection` REPORT: the library answers 400 `unsupported REPORT root`. Collections also lack `sync-token`, `getctag` and `supported-report-set`. | `REPORT` returns 400 on both calendars and address books; the three properties come back as 404 propstats. | High | Wrapper |
| 2 | No `MKCALENDAR`: returns 405. Plain `MKCOL` creates a calendar but ignores the properties in the request. | `MKCALENDAR` 405; `MKCOL` 201 with an empty display name. Python client `make_calendar` fails. | High | Wrapper (the backend's `CreateCalendar` is public) |
| 3 | `PROPPATCH` is not implemented for calendars (501) and malformed for address books. Display name, colour, order, description and timezone can't be changed. | `PROPPATCH` 501 on a calendar. | High | Wrapper |
| 4 | Property set is closed. Missing: `calendar-color`, `calendar-order`, `calendar-timezone`, `owner`, `principal-URL`, `calendar-user-address-set`, `schedule-inbox-URL`, `schedule-outbox-URL`, quota properties. Properties can't be supplied by the backend. | 404 propstats for each of them. | High (Apple expects the schedule properties on the principal) | Wrapper answering collection and principal `PROPFIND` itself, or fork |
| 5 | Time-range matching ignores event duration for recurring events. An occurrence is found only if it starts inside the window, so a window in the middle of a one-hour occurrence, or on the second day of a recurring three-day all-day event, finds nothing. | Window 08:30 to 08:45Z inside a weekly 08:00 to 09:00Z event: no match. Recurring three-day all-day event, window on day two: no match. | High | Backend (own matcher) |
| 6 | Non-recurring events that start exactly at the window start are missed, because the comparison is strict. A UTC-midnight window finds no all-day event starting that midnight. | Window 10:00 to 10:30Z, event 10:00 to 11:00Z: no match. All-day event on its own day with a day-aligned UTC window: no match. | High | Backend (own matcher) |
| 7 | `RECURRENCE-ID` overrides are not applied. A moved occurrence still matches at its original time. | Occurrence moved from Monday to Thursday still matches on the Monday. | Medium | Backend (own matcher) |
| 8 | An event with a `TZID` that has no loadable timezone makes the whole `calendar-query` return 500, for every event in the calendar. The library resolves TZIDs through the system zone database and ignores `VTIMEZONE`, so non-IANA names such as Outlook's or Evolution's fail. | One event with `TZID=Custom/Unknown` and no `VTIMEZONE` breaks every query against that calendar. | High (one bad event hides the calendar) | Backend (own matcher and timezone handling) |
| 9 | Stored bytes are not returned. `GET` re-encodes the parsed object, and go-ical sorts properties alphabetically and re-folds lines. The same happens for vCards. Content-Length comes from the backend, so reporting the stored length corrupts HTTP framing. | A round trip of an event and a vCard differs from the upload in property order. Unknown properties such as `X-CUSTOM-PROP;X-PARAM=foo` survive. | Medium | Wrapper: capture the raw body on `PUT`, serve `GET` ourselves |
| 10 | No request limits. A 60 MB event `PUT` was accepted and server memory went from 17 MB to 259 MB. `Depth: infinity` is accepted and recursed. Deeply nested XML (200,000 levels) is rejected with 400. | Measured on the spike server. | Medium | Middleware |
| 11 | Calendar collection `DELETE` returns 204 and does nothing: the backend has no `DeleteCalendar`, and the request is treated as an object delete. | Calendar still listed after a 204. | Medium | Backend special case or wrapper |
| 12 | Text matching ignores collations and is case-sensitive. `i;unicode-casemap`, the default, should be case-insensitive, so searching `ADA` doesn't find `ada@example.org`. Only the first property value is checked. `param-filter` is missing for contacts. | `contains` with different case returns nothing; the same query in matching case returns the contact. | Medium | Backend (own matcher) |
| 13 | Validation errors are plain-text 400s, and an event without a `UID` produces a 500. No `valid-calendar-data` or `no-uid-conflict` precondition elements. UID uniqueness isn't enforced. | `PUT` of an event with no `UID` returns 500. | Medium | Backend: validate before handing to the library |
| 14 | `.well-known` redirects with 308 and only for an authenticated user, because it needs the current principal. Unauthenticated discovery returns an error. | Wrapper had to answer `.well-known` itself to make discovery work without credentials. | Low | Wrapper |
| 15 | `MOVE` and `COPY` return 501. | `MOVE` 501. | Low | Wrapper |
| 16 | `current-user-privilege-set` puts read and write inside one `<privilege>` element; the RFC wants one element per privilege. `supported-address-data` ignores the backend's value. `calendar-description` is always emitted. Address book `PROPPATCH` responses mix `status` and `propstat`. | Seen in responses and in the library code. | Low | Fork |
| 17 | `calendar-data` partial retrieval and `expand` requests are parsed but ignored. `free-busy-query` and `expand-property` are not implemented. VTODO never matches a time-range filter. | Library code and tests. | Low for now | Backend, later |
| 18 | `OPTIONS` omits `extended-mkcol` and doesn't list `PROPPATCH` or `MKCALENDAR` in `Allow`. | `OPTIONS` response. | Low | Wrapper |

According to the code audit, the library has about 60 `TODO`s in its server code
and five server test functions, none covering `PUT`, `DELETE`, `PROPPATCH`,
queries or error paths. It is still
maintained (v0.7.0 was tagged on 2025-10-18).

## Recurrence edge cases tried

Against the library's `Filter` helper, with a `VTIMEZONE` for `Europe/Prague`:

| Case | Result |
|---|---|
| Weekly rule with `EXDATE` | passes |
| `COUNT=3`: third occurrence matches, fourth doesn't | passes |
| `UNTIL` boundary | passes |
| Daylight saving change: occurrence after the switch at the correct UTC time, and not at the stale offset | passes |
| `DURATION` instead of `DTEND`, and no `DTEND` at all | passes |
| Monthly rule on day 31 skips February | passes |
| Floating time | passes |
| All-day event does not leak into the next day | passes |
| Moved occurrence (`RECURRENCE-ID`) no longer matches its original slot | fails (gap 7) |
| All-day event found by a day-aligned UTC window | fails (gap 6) |
| Unknown `TZID` | crashes the query (gap 8) |
| Window inside a recurring occurrence | fails (gap 5) |

## Consequences

Whatever the library covers, a server built on it would still have to write most of
the protocol itself:

- Its own `calendar-query` evaluation instead of `caldav.Filter` (gaps 5, 6, 7, 8
  and 12), and its own contact text matching.
- Storing and serving the bytes the client sent (gap 9). The obstacle is the
  library's backend interface, which hands the backend an already-parsed object, not
  only the `GET` handler. Fixing it means changing the interface every handler is
  built around.
- Principals, collection and home set `PROPFIND`, `PROPPATCH`, `MKCALENDAR`, `MOVE`,
  `sync-collection` and `.well-known`. These need their own multistatus XML, because
  the library's is sealed.
- Middleware that caps request bodies, clamps `Depth`, and maps errors to the right
  status codes.

## Other libraries

No Go library does better. Checked in October 2026:

| Library | Licence | State | Notes |
|---|---|---|---|
| [emersion/go-webdav](https://github.com/emersion/go-webdav) | MIT | Since 2017, 501 stars, last push June 2026 | Subject of this spike |
| [samedi/caldav-go](https://github.com/samedi/caldav-go) and forks | MIT | Last push 2019, CalDAV only | Unmaintained |
| [mniehe/davkit](https://github.com/mniehe/davkit) | AGPL-3.0 | Created August 2026, no stars, requires Go 1.27 | Has an optional `sync-collection` interface, but AGPL makes the shipped binary a combined work under AGPL terms, which an Apache-2.0 project can't accept. No sign of testing against real clients. |
| [chriss-de/go-ccdav](https://github.com/chriss-de/go-ccdav) | none stated | Created October 2026, no stars | A standalone server, not a library; its code is under `internal/`. Evaluated below. |

Another project [hit the same gaps](https://github.com/C0piIot/stratus-backend/issues/75)
with go-webdav v0.7.0 and put its own `REPORT` and `PROPFIND` handlers in front of
it, with a per-collection change counter and a tombstone table for
`sync-collection`.

Non-Go servers (sabre/dav, Radicale, Xandikos) were not evaluated. They would be a
second runtime next to the embedding service and rule out a single-binary
deployment.

### go-ccdav evaluation

`go-ccdav` was built and run at commit `31c75b8` (27 October 2026) in Docker, with no
network, a read-only filesystem and dropped capabilities. The cases from this
document were re-implemented as probes against it.

It passed every probe where go-webdav failed, with one exception: `MOVE` and `COPY`
return 405. Working: `sync-collection` (including deletions and invalid tokens),
`getctag`, `sync-token`, `supported-report-set`, `MKCALENDAR` and `PROPPATCH` with
properties, calendar `DELETE`, unauthenticated `.well-known`, byte-identical `GET`
for events and vCards, all 19 recurrence cases plus the overlap, boundary and
override cases, Windows and unknown `TZID`s without failing the query, case-insensitive
contact search with collations, `403` with precondition XML for invalid data, a `413`
for a 60 MB `PUT` with memory staying near 24 MB, and clamped `Depth: infinity`.
3272 requests produced no panics and no 5xx responses. With 3000 events, a month
query took 25 ms.

Facts that limit what can be done with it:

- No licence anywhere, so nothing can be copied or depended on.
- One author, 37 commits between 23 and 27 October 2026, about 26,000 lines of
  non-test Go. Its design document says it was exported from a Claude Code session,
  which leaves the copyright position unclear.
- Everything is under `internal/`. The protocol packages (`dav`, `ical`, `rrule`,
  `caldav`, `carddav`, about 5,100 lines) import only each other and could be moved
  out. The layer that serves `PROPFIND`, `REPORT` and sync reads users straight from
  its own `config.yaml`.
- The README claims testing against Apple, Thunderbird, DAVx⁵ and Evolution. The
  fixtures are hand-written and its own design document mentions only DAVx⁵, so this
  claim is unverified.
- It requires Go 1.27.

It is good enough to learn from and, until it has a licence, to use as a black-box
reference server run in Docker. Depending on it would need a permissive licence, the
protocol packages moved out of `internal/`, tagged releases, and a way to replace its
config-based users.

## Options for the protocol layer

1. Wrapper. Use the library as the object-level engine and write everything
   above. Quickest start. Two code paths have to agree on paths, properties and
   errors, and the part written on top ends up covering most of the protocol.
2. Fork. Maintain a fork of go-webdav and fix the closed property map, the REPORT
   roots and the depth-based routing at the source, and offer the fixes upstream.
   This removes the duplicate code path. It adds a fork to maintain, and the
   library's own test coverage is thin.
3. Own protocol layer. Use only `go-ical`, `go-vcard` and `rrule-go`, and write the
   DAV handling ourselves. Most control and the most code.

## Decision

groupkit writes its own protocol layer (option 3). The gaps are in the library's
core model, not at its edges: a closed property map, fixed REPORT roots, path-depth
routing, and a backend interface built around parsed objects. A wrapper would
duplicate most of the protocol, and a fork would diverge from upstream immediately,
because upstream's interface conflicts with storing the bytes the client sent.

- `go-ical`, `go-vcard` and `rrule-go` are used for parsing, validation and
  recurrence. Stored bytes are what gets served.
- Where go-webdav code is useful, such as XML element definitions and REPORT request
  decoders, it is copied in under its MIT licence with the copyright notice kept, and
  the commit says where it came from.
- The `calendar-query` matcher, text collations and time zone mapping are written
  here. The spike's edge-case suite is the starting point for the tests.
- Request limits, `Depth` handling and error responses belong to the engine.
- go-ccdav is run locally in Docker as a black-box reference until it has a licence.
  If its author adds a permissive licence and importable packages, depending on it
  could replace part of this work.

## Still to verify

- Real clients: Thunderbird, Apple Calendar and Contacts on macOS and iOS, DAVx⁵,
  Evolution. In particular what each does when `getctag`, `sync-token` and the
  schedule properties are absent, and how each creates and renames a calendar.
- Whether the gaps above change once the missing properties and reports exist (which
  of them clients actually need to function).
