<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# Evolution check

`check.py` runs Evolution's data server, the code Evolution itself uses for calendars
and contacts, without a desktop. It adds a CalDAV calendar and a CardDAV address book to
the source registry and then:

- creates, reads, edits and deletes an event and a contact through the client libraries
  and checks what the server stored after each step;
- writes an event (a weekly recurrence) and a contact on the server with plain HTTP,
  refreshes the client and checks that both arrive, and that the recurrence expands to
  three occurrences;
- deletes the event on the server and checks that a refresh removes it.

The `evolution` job in CI runs it. It takes about two minutes.

## Run it

The script writes to the demo user's default calendar and address book and removes
what it creates. Use a throwaway example server.

```sh
go build -o /tmp/groupkit-example ./examples/server
GROUPKIT_PASSWORD='choose a long password' /tmp/groupkit-example \
  -addr 127.0.0.1:8080 -data "$(mktemp -d)" &

docker build -t groupkit-evolution test/evolution
docker run --rm --network host -e GROUPKIT_PASSWORD='choose a long password' \
  -v "$PWD/test/evolution/check.py:/check.py:ro" \
  groupkit-evolution dbus-run-session -- python3 -I /check.py
```

`--network host` needs Linux. On Docker Desktop, run the example server in a container
and join its network with `--network container:<name>` instead.
