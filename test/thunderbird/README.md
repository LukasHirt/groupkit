<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# Thunderbird check

`flows.py` runs Thunderbird against the example server and checks both directions. It
uses Thunderbird's own code to add the address book and the calendar, then:

- creates, edits and deletes contacts and events in Thunderbird and reads what the
  server stored (vCard and iCalendar text, ETags, status codes), including a recurring
  event with a time zone, an alarm and an attendee, one moved occurrence, and an
  all-day event;
- creates, edits and deletes contacts and events on the server, runs a Thunderbird sync
  and reads what Thunderbird now shows, including a UTF-8 contact and an event with
  `VTIMEZONE`, `EXDATE` and `VALARM`.

It needs the Thunderbird application, so it is not part of CI. Last run: Thunderbird
159.0 on macOS, 27 checks passed, none failed. One known limitation is reported, not
failed: Thunderbird refuses to create a task, because the server stores events only.

## Run it

The script **deletes everything in the demo user's default calendar and address book**.
Use a throwaway example server and a throwaway Thunderbird profile.

```sh
go build -o /tmp/groupkit-example ./examples/server
GROUPKIT_PASSWORD='choose a long password' /tmp/groupkit-example \
  -addr 127.0.0.1:8081 -data "$(mktemp -d)" -user demo &

profile=$(mktemp -d)
MOZ_REMOTE_ALLOW_SYSTEM_ACCESS=1 /Applications/Thunderbird.app/Contents/MacOS/thunderbird \
  --headless --marionette --no-remote --profile "$profile" &

GROUPKIT_PASSWORD='choose a long password' python3 test/thunderbird/flows.py
```

`GROUPKIT_URL` (default `http://127.0.0.1:8081`) and `GROUPKIT_USER` (default `demo`)
change the target. The script prints `PASS`, `FAIL` or `LIMIT` for each check and exits
with 1 if any check failed.

`MOZ_REMOTE_ALLOW_SYSTEM_ACCESS=1` lets the remote-control port run privileged code in
Thunderbird. The port listens on the local machine only, but set it for a throwaway
profile and quit that Thunderbird when the run is over. `marionette.py` is a small
client for that port and uses only the Python standard library.
