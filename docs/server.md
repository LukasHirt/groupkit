<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# The server

`groupkit.NewServer` builds an HTTP handler that serves CalDAV and CardDAV
from a `store.Store`. This document is for the program that embeds it. It
covers the configuration, the two functions that tie requests to your users,
the URL layout, how to mount the handlers, the limits, and what the server
does not do.

## Configuration

`NewServer(Config)` checks the configuration and returns a `*Server`, or an
error. It copies the `Config`.

| Field | Meaning |
|---|---|
| `Store` | The `store.Store` that holds the calendars and address books. Required |
| `Prefix` | The URL path the server is mounted at, such as `/dav`. Empty, or segments of letters, digits and `-._~` that start with `/`, with no trailing slash |
| `CurrentUser` | Names the user making a request to the root or a principal. Required |
| `Authorize` | Maps the user in a calendar or address book URL to the store owner, or refuses the request. Required |
| `Challenge` | The `WWW-Authenticate` value sent with a 401, such as `Basic realm="example"`. Empty sends no header. Control characters are an error |
| `MaxObjectSize` | The largest object a client may store, in bytes. Zero means the default given under Limits |
| `DisableAddressBooks` | Turns CardDAV off |
| `DisableCalendars` | Turns CalDAV off. Turning both off is an error |
| `Logger` | An `*slog.Logger` for server faults. Nil discards them |

<!-- claim: TestNewServerErrors -->
<!-- claim: TestMountsFollowDisableFlags -->
<!-- claim: TestBothDisabledIsAnError -->
`DisableCalendars` makes everything under `{prefix}/calendars/` answer 404 and
removes `calendar-home-set` and the `calendar-access` class from the
responses. `DisableAddressBooks` does the same for `{prefix}/addressbooks/`.

## Who is asking: CurrentUser and Authorize

The library does not authenticate anyone. It asks your two functions.
Authenticate the request first, in your own middleware or inside these
functions, and answer from what you know about the caller.

`CurrentUser(r)` is called for requests to the root (`{prefix}/`) and to a
principal (`{prefix}/principals/{user}/`). It returns the name of the signed-in
user. That name must be the name that appears in the user's own URLs, because
a principal can only be read by the user it belongs to: a request for
another user's principal gets 403, whether or not that user exists.

`Authorize(r, urlUser)` is called for every calendar and address book
request, including requests for the user's own data. `urlUser` is the user
segment of the URL, already percent-decoded. The function returns the store
owner whose data that URL refers to, or refuses the request. This is where
you decide that `alice` may read her own calendars and nobody else's, or that
a colleague may read hers.

The owner and the URL user can differ. The owner is the opaque string the
store files the data under: it can contain slashes and dots, can be up to 1024
bytes long, and can be any UTF-8 text without control characters, for example
the subject of an identity token. Links in responses always use the URL user,
never the owner. The file store encodes the owner, so an owner such as
`../x` cannot reach outside the data directory.

Both functions return an error to refuse. The server maps it like this:

| Error (or an error wrapping it) | Answer |
|---|---|
| `groupkit.ErrUnauthenticated` | 401, with `WWW-Authenticate: <Challenge>` when `Challenge` is set |
| `groupkit.ErrForbidden` | 403 |
| `groupkit.ErrUserNotFound` | 404 |
| any other error | 500. The error is logged and no detail is sent to the client |

<!-- claim: TestCurrentUserErrorMapping -->
<!-- claim: TestAuthorizeErrorsReachAddressBooks -->
<!-- claim: TestEmptyCurrentUserIs500 -->
An empty user name from `CurrentUser`, or an empty owner from `Authorize`, is
also a 500. The `Challenge` is added to every 401 the server sends, including
the ones from the CalDAV and CardDAV handlers.

The same errors exist in package `access`, and `groupkit` re-exports them, so
a function can return either.

## URL layout

With the prefix `/dav`:

| URL | What it is |
|---|---|
| `/dav/` | The root. `PROPFIND` returns `current-user-principal` |
| `/dav/principals/{user}/` | The principal of a user. It carries `calendar-home-set` and `addressbook-home-set` |
| `/dav/calendars/{user}/` | The user's calendar home. `PROPFIND` only |
| `/dav/calendars/{user}/{calendar}/` | A calendar |
| `/dav/calendars/{user}/{calendar}/{name}` | An event |
| `/dav/addressbooks/{user}/` | The user's address book home. `PROPFIND` only |
| `/dav/addressbooks/{user}/{book}/` | An address book |
| `/dav/addressbooks/{user}/{book}/{name}` | A contact |

A request for a collection works with or without the trailing slash. Links in
responses always have it. `REPORT` on a home or a principal answers 405.

Calendars are created with `MKCALENDAR`, or with an extended `MKCOL` whose
`resourcetype` names `calendar`. Address books are created with an extended
`MKCOL` that names `addressbook`. Both answer 201 with a `Location` header.
Creating a name that exists answers 405 with an `Allow` header.

Client-visible properties of a collection: the display name (up to 255
bytes) and the description (up to 4096 bytes) for both kinds, and for
calendars also the Apple calendar colour, the order, a time zone and the set of
components. The component set can be given when the calendar is created and
cannot be changed afterwards.

## Mounting the handlers

`Server.Handler()` serves everything under the prefix. It does not strip the
prefix: mount it so the full request path reaches it. With a `net/http`
`ServeMux`, mount it twice, because the mux would otherwise answer `/dav`
with a redirect to `/dav/`, and the handler can answer both itself:

```go
mux.Handle("/dav/", srv.Handler())
mux.Handle("/dav", srv.Handler())
```

`Server.WellKnown()` serves the discovery URLs. Mount it at
`/.well-known/caldav` and `/.well-known/carddav`:

```go
mux.Handle("/.well-known/caldav", srv.WellKnown())
mux.Handle("/.well-known/carddav", srv.WellKnown())
```

<!-- claim: TestWellKnown -->
It ignores the request path, needs no authentication, and answers `GET`,
`HEAD` and `PROPFIND` with 301 and `Location: {prefix}/`. `OPTIONS` gets 200
with an `Allow` header, and any other method gets 405. Clients that look
for the service this way then continue at the prefix and authenticate there.

An empty prefix is allowed. Then the handler serves from `/`.

The CalDAV and CardDAV handlers are also available on their own, as
`caldav.New` and `carddav.New`, for a program that wants only one protocol
or its own discovery. `groupkit.Server` is the way to get both with
discovery, and is what this document describes.

## Behind a reverse proxy

A proxy in front of the program usually terminates TLS. For this server it
has to:

- Send the prefix and the two well-known paths to the program. With the prefix
  `/dav`, that is `/dav`, everything below `/dav/`, `/.well-known/caldav` and
  `/.well-known/carddav`.
- Pass the path on unchanged. The server builds links from its prefix and
  does not rewrite them, so a proxy that maps a public path to another path
  inside gives clients links that do not exist.
- Pass the methods and headers of WebDAV unchanged. That includes `PROPFIND`,
  `PROPPATCH`, `REPORT`, `MKCALENDAR` and `MKCOL`, and the `Depth`,
  `If-Match` and `If-None-Match` headers.
- Allow request bodies as large as `MaxObjectSize`. Many proxies refuse bodies
  above a small default size unless told otherwise.

Forward the `Authorization` header if the program does the authentication.
If the proxy does the authentication, have it pass the user in a header that
the program's own `CurrentUser` reads, and make sure clients cannot set that
header themselves.

## Timeouts and TLS

The library sets no read, write or idle timeouts, no header size limit, no TLS
and no rate limits. Those belong to the `http.Server` of the embedding
program. Set `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`,
`IdleTimeout` and `MaxHeaderBytes`, and terminate TLS in the program or in a
proxy in front of it. The store receives `r.Context()` on each call, so a client
that disconnects cancels its request.

## Setting up users

`groupkit.EnsureDefaultCollections(ctx, store, owner)` creates an address
book named `contacts` (display name "Contacts") and a calendar named
`personal` (display name "Personal", for events), each only if the owner has no
collection of that kind. The check does not look at names: an owner who has
only an address book called `family` gets no `contacts`. It is safe to call
repeatedly and from several goroutines. The `owner` argument is the store
owner, not the name in the URL.

`Server.EnsureDefaultCollections(ctx, owner)` does the same for the protocols
the server has enabled, using the store from the `Config`. It skips the address
book when `DisableAddressBooks` is set and the calendar when
`DisableCalendars` is set.

<!-- claim: TestEnsureDefaultCollectionsBothKinds -->
<!-- claim: TestServerEnsureDefaultCollectionsRespectsFlags -->
The server never calls either function itself. Call one when you create a
user, so that clients find a place to save their first event or contact. A
user who deletes every calendar gets `personal` back the next time the program
calls it.

## Limits

These limits bound what one client can make the server do.

<!-- claim: TestNewDefaults -->
<!-- claim: TestPutTwelveMiB -->
- Objects: at most `MaxObjectSize`, 10 MiB by default. A larger `PUT` gets 413
  with the `max-resource-size` condition.
<!-- claim: TestParseXMLSizeLimit -->
- XML request bodies: at most 1 MiB.
<!-- claim: TestParseXMLDepth -->
- XML nesting: at most 64 levels.
<!-- claim: TestParseXMLElementCount -->
<!-- claim: TestParseXMLAttributeCount -->
- XML elements and attributes: at most 100,000 of each in one body.
<!-- claim: TestReportTooManyDistinctProperties -->
- Property names: at most 1000 in one `PROPFIND`, `PROPPATCH` or `REPORT`.
<!-- claim: TestPropfindDepth -->
- `Depth: infinity`, and a missing `Depth` header, are served as `Depth: 1`.
  `REPORT` ignores `Depth`.
<!-- claim: TestCreateBoundaryLengths -->
<!-- claim: TestCreateOneByteOverTheLengthLimitsIsRefused -->
- Display name: at most 255 bytes. Description: at most 4096 bytes.
<!-- claim: TestMultigetTooManyHrefs -->
- `calendar-multiget` and `addressbook-multiget`: at most 1000 hrefs, else 400.
<!-- claim: TestQueryObjectCap -->
- A query looks at most 100,000 objects of a collection. If there are more, the
  answer ends with a 507 `number-of-matches-within-limits` response for the
  collection.
<!-- claim: TestSyncCap -->
- A sync delta is at most 100,000 changes. A larger one gets 403
  `valid-sync-token` and the client syncs from scratch. An initial sync is never
  cut short.
<!-- claim: TestTextMatchSizeLimits -->
- Filters: at most 100 `prop-filter`, 100 `text-match` and 100 `param-filter`
  elements. A `text-match` is at most 8 KiB, and all of them together at most
  64 KiB. A calendar query also allows 16 levels of `comp-filter`, 1000 filter
  elements and 16 time ranges. Filters over these limits get 403
  `valid-filter`.
<!-- claim: TestWideWindowExpandsHundredYears -->
- Recurrence: a time-range query expands at most 100 years from the later of
  the window start and the event start, and at most 10,000 occurrences per
  object. See [ical.md](ical.md).
<!-- claim: TestValidateName -->
- Names: collection and object names up to 255 bytes, owners up to 1024 bytes.

An object that the server cannot read or parse is skipped in listings, queries
and sync, and logged as a warning. In a multiget it is a 500 response for that
one href.

## What is not supported

| Request | Answer |
|---|---|
| `PUT` of a task or journal (`VTODO`, `VJOURNAL`), or creating a calendar that holds only tasks | 403 `supported-calendar-component` |
| `PUT` of an iCalendar object with a `METHOD` property, with several UIDs, or that is not iCalendar | 403 `valid-calendar-data` |
| `PUT` of a vCard 2.1 card | 403 `supported-address-data` |
| `PUT` of an object whose UID another object in the collection already uses | 409 `no-uid-conflict`, with the other object's href |
| `PUT` with a `Content-Type` that is not a calendar or contact type | 415 |
| `free-busy-query` and any other unknown `REPORT` | 403 `supported-report` |
| A text match with an unknown collation | 403 `supported-collation` |
| A `sync-collection` with `sync-level` other than 1 | 400 |
| Scheduling: `PROPPATCH` of `schedule-calendar-transp` and other scheduling properties | 207, with status 403 for those properties; nothing is stored. Reading them gives a 404 property status |
| `MOVE`, `COPY`, `LOCK`, `UNLOCK`, `ACL` | 405 with an `Allow` header |
| `GET` of a collection | 405 |
| `REPORT` on a home, a principal or an object | 405 |

<!-- claim: TestMoveCopyLockAndACLAre405 -->
<!-- claim: TestFreeBusyQueryIsUnsupported -->
<!-- claim: TestCreateWithoutEventsIsRefused -->
<!-- claim: TestPutUIDConflict -->
<!-- claim: TestReportDispatch -->
Sharing, invitations, free-busy lookups, quotas and push notifications are not
implemented, and their properties are reported as absent. Stored objects are
served byte for byte; the server does not convert between vCard versions or
between iCalendar dialects, and ignores requests for part of an object
(`expand`, `limit-recurrence-set` and property subsets). It returns the whole
object.

## Sync tokens and change tags

A `sync-collection` token looks like `urn:groupkit:sync:{collection-id}:{revision}`.
The revision is the collection's revision when the answer was made. The
collection ID is a random value that a collection gets when it is created,
so a token from a deleted and re-created collection of the same name does
not match.

The `getctag` property is the collection's revision as a number. It changes
whenever the collection's contents or properties change, and it never goes
back, so a client that polls can compare it to the value it saw last.
Reading `getctag` or `sync-token` with `Depth: 0` reads no objects.

<!-- claim: TestSyncInvalidTokens -->
<!-- claim: TestSyncCTagEqualsTokenRevision -->
A token that is empty means "give me everything". A token from another
collection, from a collection that was re-created, from a revision the
collection has not reached, that is garbage, or that is older than the store's
change journal gets 403 `valid-sync-token`. Clients answer that by syncing
from scratch. The file store's journal size is set with `WithMaxJournal`; see
[filestore.md](filestore.md).

Deleted objects appear in a delta as responses with status 404 and no
properties.

## Conditional requests

<!-- claim: TestPutConditions -->
<!-- claim: TestGetIfNoneMatch -->
Every object has a strong `ETag`, derived from its content. `PUT` and `DELETE`
on an object honour `If-Match` and `If-None-Match`, including `*`. The store
checks the condition and writes in one step, so two clients that race get one
success and one 412. A malformed header also gets 412. `GET` and `HEAD`
honour `If-None-Match` with 304. A `PUT` answers 201 for a new object and 204
for a replaced one. `DELETE` on a collection ignores conditional headers.

## Logging

When `Config.Logger` is set, the server logs faults and warnings: an error from
`CurrentUser` or `Authorize` that is not one of the three above, a store
failure, an object that could not be read, a response that was cut short. A
log entry has the request method or the collection name, the resource path or
the object name where there is one, and the error. Collection and object names
are chosen by clients and can contain personal data. It never has bodies,
credentials, headers or query strings.

## The example server

`examples/server` is a complete program built from these pieces. Its source shows a
working `CurrentUser`, `Authorize` and mux setup.

- It opens a file store in the directory given by `-data` (default `./data`).
- It authenticates one account with HTTP Basic. The user name is the `-user`
  flag (default `demo`). The password is read from the environment variable
  `GROUPKIT_PASSWORD`, never from a flag, and must have at least 12
  characters and at most 256 bytes. A missing, short or overlong password stops the program before it listens, and so
  does an empty user name, one longer than 64 bytes, one containing `/`, `:`
  or control characters, one with surrounding spaces, or one equal to `.` or
  `..`.
- A middleware in front of the DAV handler checks the credentials in constant
  time. A request without the right credentials gets 401 with a challenge and
  never reaches the server. `CurrentUser` then reads the user from the request
  context, and `Authorize` allows that user to reach their own URLs and nobody
  else's.
- It creates the default address book and calendar for the account with
  `groupkit.EnsureDefaultCollections`.
- It mounts the handler at `/dav` (with and without the trailing slash), the
  well-known URLs, and a plain text answer at `/`.
- It listens on `127.0.0.1:8080` by default (`-addr`), with read-header, read,
  write and idle timeouts and a header size limit set on its `http.Server`.
- It speaks plain HTTP. Put a reverse proxy with TLS in front of it before
  exposing it beyond the local machine.
- On `SIGINT` or `SIGTERM` it stops accepting requests, lets running ones
  finish, and then closes the store.

<!-- claim: TestConfigValidation -->
<!-- claim: TestRejectedRequestsNeverReachTheServer -->
<!-- claim: TestOtherUsersAreForbidden -->
<!-- claim: TestRestartKeepsData -->
<!-- claim: TestOneProcessPerDirectory -->
The tests in `examples/server` cover the refusals above, the isolation between
users, restarts and the single-process lock.
