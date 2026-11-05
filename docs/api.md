<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# Compatibility, versioning and sizes

This page says what a program that imports groupkit can rely on, how versions
are numbered, and how large a deployment the library has been measured with.

## What the compatibility promise covers

From version 1.0.0, a program that builds against a 1.x release builds against
every later 1.x release without changes, and keeps working the same way. The
promise covers:

- Every exported identifier of these packages: `groupkit`, `access`, `store`,
  `store/filestore`, `store/memory`, `store/storetest`, `ical`, `vcard`,
  `caldav` and `carddav`. Their documentation on pkg.go.dev describes what
  each one does, and that behaviour is part of the promise.
- The on-disk layout of the file store. A 1.x release opens a directory written
  by an earlier 1.x release. A layout that earlier releases cannot read would
  need a new major version. See [filestore.md](filestore.md).
- The sentinel errors. `errors.Is` keeps matching the same errors, and an
  error that wraps one of them keeps wrapping it.
- Which HTTP status and WebDAV error a refused request gets, for the cases the
  documentation lists.

Some things are outside it:

- Everything under `internal/`, `examples/` and `test/`, and the scripts.
- The text of error messages and log lines. Match errors with `errors.Is`
  or `errors.As`.
- The order of properties in a `PROPFIND` answer, whitespace in XML, and other
  details that the protocols leave open.
- The minimum Go version. A minor release can raise it.
- Behaviour that contradicts a standard or the documentation. Fixing it is a
  bug fix, even when a program depended on it.

### Interfaces and structs

`store.Store` is an interface that other programs implement. Adding a method
to it would break them, so a 1.x release does not. New capabilities a store may
offer arrive as separate interfaces that the server finds with a type
assertion, and a store that lacks them keeps working.

The option and result structs (`groupkit.Config`, `store.Props`,
`store.Collection` and the like) can gain fields in a minor release. Build them
with field names, never by position, and a new field then takes its zero
value, which keeps the old behaviour.

## Versioning

Versions follow [semantic versioning](https://semver.org). The module path
stays `github.com/LukasHirt/groupkit` for every 1.x version, and a 2.0 would
add `/v2`.

| Change | Release |
|---|---|
| A removed or changed exported identifier, or a layout change that old versions cannot read | major |
| A new exported identifier, a new option, a new protocol feature | minor |
| A fix, a performance improvement, a dependency update | patch |

A release is a signed tag with release notes. [CONTRIBUTING.md](../CONTRIBUTING.md)
describes how one is made.

Deprecated identifiers stay for the whole major version. Their documentation
starts a paragraph with `Deprecated:` and names the replacement.

Security fixes go into the latest release, as [SECURITY.md](../SECURITY.md)
says.

## Supported sizes

The sizes below were measured, not estimated. A program built on groupkit
served one data directory from the file store, and a load generator talked to
it over loopback on one 14-core machine (a development laptop, not a dedicated
host). The figures include authentication in that program, so treat them as
an order of magnitude for the library.

With 30,000 events in one calendar:

- A range query had a p95 of about 0.6 s.
- A cold start took under 1 s to answer its first request, and a calendar is
  loaded when its first request arrives.
- Memory stayed between 55 and 78 MB for the whole run, which held 55,000
  events in three large calendars and 28 MB on disk.

Calendars of 1,000 and 10,000 events were quicker: a range query had a p95 of
about 15 ms on 1,000 events and about 0.18 s on 10,000. Delta syncs and the
writing of one event stayed in the range of milliseconds at every size.

The largest calendar is where the margin is thinnest. Clients that ask for
large ranges on calendars of that size, or many of them at once, were not
covered, and sizes past these figures have not been tried. One process owns a
data directory, which is how the file store works (`ErrLocked`).
