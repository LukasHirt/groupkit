# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

"""Runs the Python caldav client against a running example server.

The server address and credentials come from the environment:
GROUPKIT_URL (default http://127.0.0.1:8080/dav/), GROUPKIT_USER (default
demo) and GROUPKIT_PASSWORD (required). The exit status is 1 if any step fails.
"""

import datetime as dt
import os
import sys
import traceback

import caldav

URL = os.environ.get("GROUPKIT_URL", "http://127.0.0.1:8080/dav/")
USER = os.environ.get("GROUPKIT_USER", "demo")
PASSWORD = os.environ["GROUPKIT_PASSWORD"]

EVENT = """BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//groupkit//python client check//EN
BEGIN:VEVENT
UID:{uid}
DTSTAMP:20261101T100000Z
DTSTART:20261110T090000Z
DTEND:20261110T100000Z
SUMMARY:{summary}
END:VEVENT
END:VCALENDAR
"""

WEEKLY = """BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//groupkit//python client check//EN
BEGIN:VEVENT
UID:weekly@example
DTSTAMP:20261101T100000Z
DTSTART:20261201T090000Z
DTEND:20261201T100000Z
RRULE:FREQ=WEEKLY;COUNT=4
SUMMARY:Weekly
END:VEVENT
END:VCALENDAR
"""

failures = []


def step(name, fn, expect=None):
    """Runs fn. If expect is given, the result must equal it."""
    try:
        result = fn()
        if expect is not None and result != expect:
            raise AssertionError(f"expected {expect!r}, got {result!r}")
        print("PASS", name)
        return result
    except Exception:
        failures.append(name)
        print("FAIL", name)
        traceback.print_exc()
        return None


def utc(*args):
    return dt.datetime(*args, tzinfo=dt.timezone.utc)


def summary(event):
    return str(event.icalendar_component["SUMMARY"])


client = caldav.DAVClient(url=URL, username=USER, password=PASSWORD)
principal = step("discover the principal", client.principal)
if principal is None:
    sys.exit(1)

names = step("list the default calendar", lambda: [c.get_display_name() for c in principal.calendars()])
if names is not None and "Personal" not in names:
    failures.append("default calendar missing")
    print("FAIL default calendar missing:", names)

cal = step(
    "create a calendar",
    lambda: principal.make_calendar(name="Check", supported_calendar_component_set=["VEVENT"]),
)
if cal is None:
    sys.exit(1)

step("created calendar is listed",
     lambda: sorted(c.get_display_name() for c in principal.calendars()),
     expect=["Check", "Personal"])
step("save an event", lambda: cal.save_event(EVENT.format(uid="one@example", summary="One")))
step("save a recurring event", lambda: cal.save_event(WEEKLY))
step("list events", lambda: len(cal.events()), expect=2)
step("time-range search finds the single event",
     lambda: [summary(e) for e in cal.search(event=True, start=utc(2026, 11, 10), end=utc(2026, 11, 11))],
     expect=["One"])
step("expanding the recurring event gives four occurrences",
     lambda: len(cal.search(event=True, start=utc(2026, 12, 1), end=utc(2026, 12, 31), expand=True)),
     expect=4)
step("fetch an event by UID", lambda: summary(cal.event_by_uid("one@example")), expect="One")


def edit():
    event = cal.event_by_uid("one@example")
    event.icalendar_component["SUMMARY"] = "Edited"
    event.save()
    return summary(cal.event_by_uid("one@example"))


step("edit an event", edit, expect="Edited")

token = step("initial sync", lambda: cal.objects_by_sync_token(sync_token=None))
if token is not None:
    step("add an event after the sync token",
         lambda: cal.save_event(EVENT.format(uid="three@example", summary="Three")))
    step("delta sync returns the one new event",
         lambda: len(list(cal.objects_by_sync_token(sync_token=token.sync_token))),
         expect=1)

step("delete an event", lambda: cal.event_by_uid("weekly@example").delete() or True)
step("list events after the delete", lambda: len(cal.events()), expect=2)
step("delete the calendar", lambda: cal.delete() or True)
step("deleted calendar is gone",
     lambda: [c.get_display_name() for c in principal.calendars()],
     expect=["Personal"])

if failures:
    print("FAILED:", ", ".join(failures))
    sys.exit(1)
print("all steps passed")
