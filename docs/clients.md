<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# Connecting clients

This guide shows how to point calendar and contact clients at the example
server in `examples/server`, and how to talk to it by hand with `curl`. For a
server of your own, replace the host name, the user and the prefix.

## What has been tested

The go-webdav CardDAV and CalDAV clients have been run against the server by
tests in this repository. They cover discovery, listing, reading, writing,
deleting and incremental sync.

<!-- claim: TestInteropDiscovery -->
<!-- claim: TestInteropWellKnown -->
<!-- claim: TestInteropReadWrite -->
<!-- claim: TestInteropSync -->
<!-- claim: TestInteropCalendarWellKnown -->
<!-- claim: TestInteropCalendarReadWrite -->
<!-- claim: TestInteropCalendarSync -->
Verified: the go-webdav CardDAV client (`interop_test.go`) and the go-webdav
CalDAV client (`interop_calendar_test.go`).

None of the following has been run against the server. Their requests are
standard and the server is expected to work with them, but that is not
verified: the Python `caldav` client, Thunderbird, Apple Calendar and Apple
Contacts, DAVx5 and Evolution. Everything below about those clients describes
what to enter and what the server is built to answer. It is not a record of a
tested setup.

## Start the example server

```sh
go build -o bin/groupkit-example ./examples/server
GROUPKIT_PASSWORD='choose a long password' bin/groupkit-example -user demo -data /tmp/groupkit-data
```

It listens on `127.0.0.1:8080` and serves the user `demo` with the default
calendar `personal` and the default address book `contacts`. It speaks plain
HTTP, and Basic authentication sends the password with every request. For a
phone or another computer, put a reverse proxy with TLS in front and use the
public `https` address.

## Addresses to enter

With the example server behind `https://dav.example.com`, these are the
addresses a client needs. The server answers the well-known URLs
(`/.well-known/caldav` and `/.well-known/carddav`) with a redirect to
`/dav/`, which is where discovery starts, so a client that supports it needs
only the host name.

| Purpose | Address |
|---|---|
| Server (discovery) | `https://dav.example.com/` or `https://dav.example.com/dav/` |
| Principal | `https://dav.example.com/dav/principals/demo/` |
| Calendar home | `https://dav.example.com/dav/calendars/demo/` |
| The default calendar | `https://dav.example.com/dav/calendars/demo/personal/` |
| Address book home | `https://dav.example.com/dav/addressbooks/demo/` |
| The default address book | `https://dav.example.com/dav/addressbooks/demo/contacts/` |

The user name is `demo` and the password is the value of `GROUPKIT_PASSWORD`.

### Thunderbird

Not yet verified. For calendars, choose New Calendar, On the Network, and
enter the user name and the calendar address
`https://dav.example.com/dav/calendars/demo/personal/`. For contacts, choose New
CardDAV Address Book and enter the user name and the server address
`https://dav.example.com/dav/`. The server stores events only, so there are no task
lists to subscribe to.

### Apple Calendar and Apple Contacts

Not yet verified. Add an account of the type "Other", then "CalDAV account" or
"CardDAV account". Choose manual setup, and enter the user name, the password,
and the server address `https://dav.example.com/dav/principals/demo/`. If
automatic setup is offered, the host name alone should work through the
well-known URLs. The server does not implement scheduling, so invitations are
not sent and Calendar shows attendees as plain data. Reminders (task lists)
cannot be created.

### DAVx5

Not yet verified. Add an account with "Login with URL and user name". Enter the
base URL `https://dav.example.com/dav/`, the user name and the password. DAVx5
then lists the address books and calendars it finds, and you choose which to
sync. A calendar that was created with other component types besides events
advertises events only.

### Evolution

Not yet verified. For a calendar, add a new calendar of the type CalDAV, enter
the URL `https://dav.example.com/dav/calendars/demo/personal/` and the user name.
For contacts, add a new address book of the type CardDAV and enter
`https://dav.example.com/dav/addressbooks/demo/contacts/`.

### Programs that use Go's `net/http`

The well-known URL answers 301, and `net/http` follows a 301 with a `GET`
on `/dav/`, which the server answers with 405 because the root only takes
`PROPFIND` and `OPTIONS`. Turn off redirect following and send `PROPFIND`
again to the `Location`, or configure `https://dav.example.com/dav/` as the
address directly. The go-webdav clients work against that address.

## curl examples

These run against the example server above. They use a shell variable for the
credentials:

```sh
export B=http://127.0.0.1:8080
export U='demo:choose a long password'
```

Each XML body is sent with `--data`; `curl` sets the content type itself,
and the server does not require one for these requests.

### Discovery

The well-known URL needs no credentials:

```sh
curl -i -X PROPFIND $B/.well-known/caldav
```

```
HTTP/1.1 301 Moved Permanently
Location: /dav/
```

Find the principal, and from there the home sets:

```sh
curl -u "$U" -X PROPFIND -H 'Depth: 0' $B/dav/ \
  --data '<propfind xmlns="DAV:"><prop><current-user-principal/></prop></propfind>'

curl -u "$U" -X PROPFIND -H 'Depth: 0' $B/dav/principals/demo/ \
  --data '<propfind xmlns="DAV:"
    xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:card="urn:ietf:params:xml:ns:carddav">
    <prop><c:calendar-home-set/><card:addressbook-home-set/></prop></propfind>'
```

The first answer holds `<D:href>/dav/principals/demo/</D:href>`. The second
holds `/dav/calendars/demo/` and `/dav/addressbooks/demo/`.

### Create a calendar

```sh
curl -i -u "$U" -X MKCALENDAR $B/dav/calendars/demo/work/ \
  --data '<c:mkcalendar xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
    <set><prop><displayname>Work</displayname></prop></set></c:mkcalendar>'
```

```
HTTP/1.1 201 Created
Location: /dav/calendars/demo/work/
```

### Put an event

Write a weekly event in the Prague time zone to a file. iCalendar requires
lines to end with CR LF, so use `printf`:

```sh
printf 'BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//example//EN\r\nBEGIN:VEVENT\r\n'\
'UID:standup-1@example.com\r\nDTSTAMP:20261001T000000Z\r\n'\
'DTSTART;TZID=Europe/Prague:20261102T090000\r\n'\
'DTEND;TZID=Europe/Prague:20261102T091500\r\nRRULE:FREQ=WEEKLY;COUNT=10\r\n'\
'SUMMARY:Standup\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n' > /tmp/standup.ics
```

```sh
curl -i -u "$U" -X PUT -H 'Content-Type: text/calendar' \
  --data-binary @/tmp/standup.ics $B/dav/calendars/demo/work/standup.ics
```

```
HTTP/1.1 201 Created
Etag: "e34038647bd21fe1d3fc928656e54fd7"
```

A second `PUT` of a changed event answers 204. Use `-H 'If-None-Match: *'` to
refuse overwriting an existing event, or `-H 'If-Match: "<etag>"'` to replace
exactly the version you read.

### Query a time range

Which events fall on Monday 9 November 2026 (UTC)? The weekly event matches
because its recurrences are expanded:

```sh
curl -u "$U" -X REPORT -H 'Depth: 1' $B/dav/calendars/demo/work/ \
  --data '<c:calendar-query xmlns="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
    <prop><getetag/></prop>
    <c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">
      <c:time-range start="20261109T000000Z" end="20261110T000000Z"/>
    </c:comp-filter></c:comp-filter></c:filter></c:calendar-query>'
```

The answer has one response for `/dav/calendars/demo/work/standup.ics`. A
window on Tuesday 10 November returns an empty multistatus. Add
`<c:calendar-data/>` to the `prop` to get the event itself.

### Sync

An empty `sync-token` asks for everything, and the answer ends with a token:

```sh
curl -u "$U" -X REPORT $B/dav/calendars/demo/work/ \
  --data '<sync-collection xmlns="DAV:"><sync-token/><sync-level>1</sync-level>
    <prop><getetag/></prop></sync-collection>'
```

The answer lists each event and ends with, for example,
`<D:sync-token>urn:groupkit:sync:477f6f97c89fd3971d39db59ba5ee921:1</D:sync-token>`.
Send that token in the next request to get only what changed since. Deleted
events come back as responses with status 404. A token the server cannot use
gets 403 `valid-sync-token`, and the client starts again with an empty token.

For a client that only polls, `getctag` changes whenever anything in the
collection changes:

```sh
curl -u "$U" -X PROPFIND -H 'Depth: 0' $B/dav/calendars/demo/work/ \
  --data '<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/">
    <prop><cs:getctag/><sync-token/></prop></propfind>'
```

### Things that are refused

Moving an event is not supported:

```sh
curl -i -u "$U" -X MOVE $B/dav/calendars/demo/work/standup.ics
```

```
HTTP/1.1 405 Method Not Allowed
Allow: OPTIONS, HEAD, GET, PUT, DELETE, PROPFIND
```

A task is refused with 403 and `supported-calendar-component`. To move or
copy an event, read it and put it at the new address.

<!-- claim: TestUnknownMethodIs405WithAllow -->
<!-- claim: TestPutAccepted -->
<!-- claim: TestSyncInitialAndDeltas -->
<!-- claim: TestQueryClientBodies -->
The behaviour above is covered by the tests of the `caldav` package
(`calendar-query`, `sync-collection`, conditional writes and refusals).

## Contacts

<!-- claim: TestPutCreateReplace -->
<!-- claim: TestQueryFilters -->
Address books work the same way with `addressbook-query` and vCard objects
(`text/vcard`, versions 3.0 and 4.0) under
`/dav/addressbooks/demo/contacts/`. A vCard is stored as it was sent.

```sh
curl -i -u "$U" -X PUT -H 'Content-Type: text/vcard' \
  --data-binary $'BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada-1\r\nFN:Ada Lovelace\r\nEND:VCARD\r\n' \
  $B/dav/addressbooks/demo/contacts/ada.vcf
```

## Polling versus sync

A client that polls should read `getctag` or the `sync-token` property of a
collection with `Depth: 0`. That request reads no objects. Prefer
`sync-collection` where the client supports it: it returns only the changes.
