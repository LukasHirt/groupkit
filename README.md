<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# groupkit

groupkit is a Go library that serves calendars and contacts over CalDAV and
CardDAV. You give it a store, a URL prefix and two functions that
say who is asking, and it returns HTTP handlers for your own program to mount.
Objects are kept as the bytes the client sent. The library knows nothing about
accounts or identity systems: it works with owners, collections and objects,
and your program decides who may see what.

Status: pre-1.0. The API will change.

## What works

- CalDAV and CardDAV discovery, including the well-known URLs.
- Creating and deleting calendars and address books from a client, and
  changing their display name, colour and description.
- Events and contacts: create, read, replace and delete, with `ETag` and
  conditional requests.
- Recurring events, overrides and time zones, including Windows zone names, in
  time-range queries.
- Queries (`calendar-query`, `addressbook-query`), `calendar-multiget` and
  `addressbook-multiget`.
- Incremental sync with `sync-collection`, and `getctag` for clients that poll.
- A file-backed store that survives restarts and crashes, and an in-memory
  store for tests.

## What does not work

- Tasks. Only events (`VEVENT`) are stored; a task (`VTODO`) is refused, and a
  task list cannot be created.
- Scheduling and invitations. Attendees are kept as data and nothing is sent.
- Sharing and access control lists.
- `MOVE`, `COPY`, `LOCK` and `UNLOCK`.
- JMAP.
- Authentication. The library asks two functions you supply who is calling
  and what they may reach.

## Mounting it

```go
package main

import (
	"log"
	"net/http"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store/filestore"
)

func main() {
	st, err := filestore.Open("./data")
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	// Replace these two functions with your own authentication.
	currentUser := func(r *http.Request) (string, error) {
		if user, _, ok := r.BasicAuth(); ok {
			return user, nil
		}
		return "", groupkit.ErrUnauthenticated
	}
	authorize := func(r *http.Request, urlUser string) (string, error) {
		user, err := currentUser(r)
		if err != nil {
			return "", err
		}
		if user != urlUser {
			return "", groupkit.ErrForbidden
		}
		return user, nil
	}

	srv, err := groupkit.NewServer(groupkit.Config{
		Store: st, Prefix: "/dav", CurrentUser: currentUser, Authorize: authorize,
		Challenge: `Basic realm="example"`,
	})
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/dav", srv.Handler())
	mux.Handle("/.well-known/caldav", srv.WellKnown())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", mux))
}
```

This snippet accepts any password. It only shows the wiring. `CurrentUser`
and `Authorize` are where your program checks credentials and maps the user
in the URL to the owner of the data. [docs/server.md](docs/server.md)
describes both, the URL layout, the limits and how to run the handlers
behind a reverse proxy. Call `groupkit.EnsureDefaultCollections` when you
create a user so that clients find a calendar and an address book.

## Try it

`examples/server` is a small program that serves one account from a data
directory, with HTTP Basic authentication. The password comes from the
environment and must have at least 12 characters and at most 256 bytes. The server refuses to start
without one.

```sh
go build -o bin/groupkit-example ./examples/server
GROUPKIT_PASSWORD='choose a long password' bin/groupkit-example -user demo -data /tmp/groupkit-data
```

It listens on `127.0.0.1:8080` and speaks plain HTTP. Run `bin/groupkit-example
-help` for the flags. In a second terminal:

```sh
curl -u demo:'choose a long password' -X PROPFIND -H 'Depth: 1' \
  http://127.0.0.1:8080/dav/calendars/demo/
```

The answer lists the default calendar `personal`. The address book is at
`/dav/addressbooks/demo/contacts/`. [docs/clients.md](docs/clients.md) shows
how to point desktop and phone clients at it and has more `curl` examples.

## Packages

| Package | Purpose |
|---|---|
| `groupkit` | `NewServer`, `Config` and `EnsureDefaultCollections`: the entry point that serves both protocols |
| `access` | The errors that `CurrentUser` and `Authorize` return to refuse a request |
| `store` | The storage interface: collections, objects and a change journal |
| `store/memory` | An in-memory store |
| `store/filestore` | A store that keeps plain files in one directory |
| `store/storetest` | A conformance suite that any `store.Store` can run |
| `vcard` | vCard validation and `addressbook-query` matching |
| `ical` | iCalendar validation, recurrence expansion, time zones and `calendar-query` matching |
| `caldav`, `carddav` | The protocol handlers, for programs that want only one of them or their own discovery |

The packages under `internal/` are not importable.

## Documentation

- [docs/server.md](docs/server.md): configuration, the URL layout, mounting,
  limits and what is not supported.
- [docs/filestore.md](docs/filestore.md): the file store's layout, crash
  recovery and operations.
- [docs/clients.md](docs/clients.md): connecting clients, and `curl` examples.
- [docs/ical.md](docs/ical.md): what the iCalendar package accepts, how it
  reads times and time zones, and its limits.

## Client compatibility

The go-webdav CardDAV and CalDAV clients have been run against the server by
tests in this repository (`interop_test.go` and `interop_calendar_test.go`).
They cover discovery, reading, writing, deleting and sync. The other clients
in the table send the same standard requests and should work, but nobody has
run them against it yet.

| Client | Verified | Expected, not yet verified |
|---|---|---|
| go-webdav CardDAV client | yes | |
| go-webdav CalDAV client | yes | |
| Python `caldav` client | | yes |
| Thunderbird (calendars and contacts) | | yes |
| Apple Calendar and Apple Contacts | | yes |
| DAVx5 | | yes |
| Evolution | | yes |

<!-- claim: TestInteropDiscovery -->
<!-- claim: TestInteropReadWrite -->
<!-- claim: TestInteropSync -->
<!-- claim: TestInteropCalendarReadWrite -->
<!-- claim: TestInteropCalendarSync -->

## Security notes

- The library does not authenticate. Put your own authentication in
  `CurrentUser` and `Authorize`.
- `Authorize` is the access check: return the owner whose data the URL user
  may reach. The owner is an opaque store key and can differ from the name in
  the URL ([docs/server.md](docs/server.md)).
- Logs can contain collection and object names, which clients choose.
- Put TLS in front. The handlers speak plain HTTP, and Basic authentication
  sends the password with every request.
- Set the timeouts on your `http.Server` (`ReadHeaderTimeout`, `ReadTimeout`,
  `WriteTimeout`, `IdleTimeout`) and `MaxHeaderBytes`. The library sets none.
  The `net/http` default has no timeouts at all, and the snippet above does
  not set them either.
- The file store creates its directories with mode `0700` and its files with
  `0600`. Run the program as a user that owns the data directory.
- Use one process per data directory. A second store on the same directory
  fails with `ErrLocked`.

## Stability

groupkit is pre-1.0 and the API will change. Nothing is released yet.

## Development

Requires Go 1.26.10 and [go-task](https://taskfile.dev).

```sh
task build
task test
task check   # fmt, vet, lint and test
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Please also read the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Security

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities.

## License

[Apache License 2.0](LICENSE).
