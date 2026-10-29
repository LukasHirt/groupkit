<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# groupkit

A Go toolkit for calendars, contacts and tasks: CalDAV and CardDAV servers,
iCalendar and vCard handling, and storage you can embed in your own service.

Status: early development. Nothing is released and the API will change.

## Packages

| Package | Purpose |
|---|---|
| `store` | The storage interface (collections, objects, change journal), an in-memory store, a file-backed store, and `storetest`, a conformance suite every store passes |
| `ical`, `vcard` | Parsing, validation and query matching, including recurrence and time zones |
| `caldav`, `carddav` | Protocol engines that serve a store over HTTP |

[docs/filestore.md](docs/filestore.md) describes the file store's layout, crash recovery and limits.
[docs/ical.md](docs/ical.md) describes what the iCalendar package accepts, how it reads times and
time zones, and its limits.

Objects are stored and served as the bytes the client sent. The engines know
nothing about identity providers or user accounts: they work with owners,
collections and objects, and the program embedding them decides who may see
what.

## Development

Requires Go 1.26.10 and [go-task](https://taskfile.dev).

```sh
task build
task test
task check   # fmt, vet, lint and test, what CI runs
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Please also read the
[Code of Conduct](CODE_OF_CONDUCT.md).

## Security

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities.

## License

[Apache License 2.0](LICENSE).
