# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

"""Runs Evolution's data server against a running example server.

Evolution's calendar and address book code runs headless: the script adds a
CalDAV calendar and a CardDAV address book to the data server's source
registry, then creates, reads, changes and deletes items through the client
libraries and checks what the server holds. It also writes items on the server
with plain HTTP and checks that a refresh brings them in.

The server address and credentials come from the environment: GROUPKIT_URL
(default http://127.0.0.1:8080/dav/), GROUPKIT_USER (default demo) and
GROUPKIT_PASSWORD (required). Run it under dbus-run-session. The exit status is
1 if any step fails.
"""

import base64
import calendar
import os
import re
import sys
import time
import traceback
import urllib.request

import gi

gi.require_version("EDataServer", "1.2")
gi.require_version("ECal", "2.0")
gi.require_version("EBook", "1.2")
gi.require_version("EBookContacts", "1.2")
gi.require_version("ICalGLib", "3.0")
from gi.repository import EBook, EBookContacts, ECal, EDataServer, GLib, ICalGLib  # noqa: E402

URL = os.environ.get("GROUPKIT_URL", "http://127.0.0.1:8080/dav/")
USER = os.environ.get("GROUPKIT_USER", "demo")
PASSWORD = os.environ["GROUPKIT_PASSWORD"]
BASE = URL if URL.endswith("/") else URL + "/"
CAL_URL = f"{BASE}calendars/{USER}/personal/"
BOOK_URL = f"{BASE}addressbooks/{USER}/contacts/"
AUTH = "Basic " + base64.b64encode(f"{USER}:{PASSWORD}".encode()).decode()

failures = []


def step(name, fn, expect=None):
    """Runs fn. If expect is given, the result must equal it."""
    try:
        result = fn()
        if expect is not None and result != expect:
            raise AssertionError(f"expected {expect!r}, got {result!r}")
        print("PASS", name, flush=True)
        return result
    except Exception:
        failures.append(name)
        print("FAIL", name, flush=True)
        traceback.print_exc()
        return None


def http(method, url, body=None, headers=None):
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("Authorization", AUTH)
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    with urllib.request.urlopen(req, timeout=20) as resp:
        return resp.status, resp.read().decode()


def server_items(collection_url, suffix):
    """Returns the text of every resource in a collection on the server."""
    _, listing = http("PROPFIND", collection_url, b"", {"Depth": "1"})
    hrefs = re.findall(r"<(?:\w+:)?href>([^<]+)</(?:\w+:)?href>", listing)
    root = GLib.Uri.parse(collection_url, GLib.UriFlags.NONE).get_path()
    texts = []
    for href in hrefs:
        if href.endswith(suffix) and href != root:
            texts.append(http("GET", GLib.Uri.resolve_relative(collection_url, href, GLib.UriFlags.NONE))[1])
    return texts


def wait_for(fn, what, seconds=40):
    """Polls fn until it returns something truthy."""
    deadline = time.monotonic() + seconds
    while True:
        value = fn()
        if value:
            return value
        if time.monotonic() > deadline:
            raise AssertionError(f"timed out waiting for {what}")
        time.sleep(1)


def add_source(registry, uid, parent, extension, backend, path):
    source = EDataServer.Source.new_with_uid(uid, None)
    source.set_display_name(uid)
    source.set_parent(parent)
    source.get_extension(extension).set_backend_name(backend)
    uri = GLib.Uri.parse(BASE.rstrip("/").rsplit("/dav", 1)[0] + path, GLib.UriFlags.NONE)
    auth = source.get_extension("Authentication")
    auth.set_host(uri.get_host())
    auth.set_port(uri.get_port())
    auth.set_user(USER)
    source.get_extension("WebDAV Backend").set_uri(uri)
    source.get_extension("WebDAV Backend").set_resource_path(uri.get_path())
    registry.commit_source_sync(source, None)
    source.store_password_sync(PASSWORD, False, None)
    return source


EVENT = """BEGIN:VEVENT
UID:{uid}
DTSTAMP:20261101T100000Z
DTSTART:20261110T090000Z
DTEND:20261110T100000Z
SUMMARY:{summary}
END:VEVENT
"""

SERVER_EVENT = """BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//groupkit//evolution check//EN
BEGIN:VEVENT
UID:from-server@example
DTSTAMP:20261101T100000Z
DTSTART:20261112T090000Z
DTEND:20261112T100000Z
RRULE:FREQ=WEEKLY;COUNT=3
SUMMARY:Server event
END:VEVENT
END:VCALENDAR
"""

SERVER_CARD = """BEGIN:VCARD
VERSION:3.0
UID:from-server-card@example
FN:Server Contact
N:Contact;Server;;;
EMAIL:server@example.com
END:VCARD
"""

registry = step("open the source registry", lambda: EDataServer.SourceRegistry.new_sync(None))
if registry is None:
    sys.exit(1)

# Calendar.

cal_source = step(
    "add a CalDAV calendar",
    lambda: add_source(registry, "gk-calendar", "caldav-stub", "Calendar", "caldav", f"/dav/calendars/{USER}/personal/"),
)
cal = None
if cal_source is not None:
    cal = step(
        "connect to the calendar",
        lambda: ECal.Client.connect_sync(cal_source, ECal.ClientSourceType.EVENTS, 60, None),
    )

if cal is not None:
    def create_event():
        comp = ICalGLib.Component.new_from_string(EVENT.format(uid="made-in-evolution@example", summary="Made in Evolution"))
        return cal.create_object_sync(comp, ECal.OperationFlags.NONE, None)

    uid = step("create an event", create_event)
    step(
        "the server holds the created event",
        lambda: wait_for(
            lambda: [t for t in server_items(CAL_URL, ".ics") if "Made in Evolution" in t and "UID:made-in-evolution@example" in t],
            "the event on the server",
        ) and True,
    )

    def read_event():
        _, comp = cal.get_object_sync("made-in-evolution@example", None, None)
        return comp.get_summary()

    step("read the event back", read_event, expect="Made in Evolution")

    def edit_event():
        _, comp = cal.get_object_sync("made-in-evolution@example", None, None)
        comp.set_summary("Edited in Evolution")
        cal.modify_objects_sync([comp], ECal.ObjModType.ALL, ECal.OperationFlags.NONE, None)
        return True

    step("edit the event", edit_event)
    step(
        "the server holds the edit",
        lambda: wait_for(
            lambda: any("Edited in Evolution" in t for t in server_items(CAL_URL, ".ics")),
            "the edit on the server",
        ),
    )

    def delete_event():
        cal.remove_object_sync("made-in-evolution@example", None, ECal.ObjModType.ALL, ECal.OperationFlags.NONE, None)
        return True

    step("delete the event", delete_event)
    step(
        "the server dropped the event",
        lambda: wait_for(
            lambda: not any("made-in-evolution@example" in t for t in server_items(CAL_URL, ".ics")),
            "the delete on the server",
        ),
    )

    def put_server_event():
        status, _ = http("PUT", CAL_URL + "from-server.ics", SERVER_EVENT.encode(),
                         {"Content-Type": "text/calendar; charset=utf-8"})
        return status in (201, 204)

    step("write an event on the server", put_server_event, expect=True)

    def refresh_and_find_event():
        def attempt():
            cal.refresh_sync(None)
            ok, comps = cal.get_object_list_sync("(contains? \"summary\" \"Server event\")", None)
            return ok and len(comps) > 0
        return wait_for(attempt, "the server event in Evolution")

    step("a refresh brings in the server event", refresh_and_find_event)

    def occurrences():
        found = []
        start = int(calendar.timegm((2026, 11, 1, 0, 0, 0)))
        end = int(calendar.timegm((2026, 12, 31, 0, 0, 0)))
        _, comps = cal.get_object_list_as_comps_sync("#t", None)
        for comp in comps:
            if comp.get_icalcomponent().get_uid() == "from-server@example":
                cal.generate_instances_sync(start, end, None, lambda *args: found.append(args) or True, None)
        return len(found)

    step("the server's recurring event expands to three occurrences", occurrences, expect=3)

    def delete_on_server():
        return http("DELETE", CAL_URL + "from-server.ics")[0] in (200, 204)

    step("delete the event on the server", delete_on_server, expect=True)

    def refresh_and_miss_event():
        def attempt():
            cal.refresh_sync(None)
            ok, comps = cal.get_object_list_sync("(contains? \"summary\" \"Server event\")", None)
            return ok and len(comps) == 0
        return wait_for(attempt, "the delete in Evolution")

    step("a refresh drops the deleted event", refresh_and_miss_event)

# Address book.

book_source = step(
    "add a CardDAV address book",
    lambda: add_source(registry, "gk-contacts", "webdav-stub", "Address Book", "carddav", f"/dav/addressbooks/{USER}/contacts/"),
)
book = None
if book_source is not None:
    book = step("connect to the address book", lambda: EBook.BookClient.connect_sync(book_source, 60, None))

if book is not None:
    def add_contact():
        contact = EBookContacts.Contact.new_from_vcard(
            "BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\nN:Lovelace;Ada;;;\nEMAIL:ada@example.com\nEND:VCARD\n"
        )
        ok, added_uid = book.add_contact_sync(contact, EBookContacts.BookOperationFlags.NONE, None)
        return added_uid

    contact_uid = step("add a contact", add_contact)
    step(
        "the server holds the contact",
        lambda: wait_for(
            lambda: any("Ada Lovelace" in t and "ada@example.com" in t for t in server_items(BOOK_URL, ".vcf")),
            "the contact on the server",
        ),
    )

    def edit_contact():
        ok, contact = book.get_contact_sync(contact_uid, None)
        contact.set_property("full-name", "Ada King")
        book.modify_contact_sync(contact, EBookContacts.BookOperationFlags.NONE, None)
        return True

    if contact_uid:
        step("edit the contact", edit_contact)
        step(
            "the server holds the edit",
            lambda: wait_for(lambda: any("Ada King" in t for t in server_items(BOOK_URL, ".vcf")), "the edit on the server"),
        )
        step(
            "delete the contact",
            lambda: book.remove_contact_by_uid_sync(contact_uid, EBookContacts.BookOperationFlags.NONE, None) or True,
        )
        step(
            "the server dropped the contact",
            lambda: wait_for(
                lambda: not any("Ada" in t for t in server_items(BOOK_URL, ".vcf")),
                "the delete on the server",
            ),
        )

    def put_server_card():
        status, _ = http("PUT", BOOK_URL + "from-server-card.vcf", SERVER_CARD.encode(),
                         {"Content-Type": "text/vcard; charset=utf-8"})
        return status in (201, 204)

    step("write a contact on the server", put_server_card, expect=True)

    def refresh_and_find_contact():
        def attempt():
            book.refresh_sync(None)
            ok, contacts = book.get_contacts_sync("(contains \"full_name\" \"Server Contact\")", None)
            return ok and len(contacts) > 0
        return wait_for(attempt, "the server contact in Evolution")

    step("a refresh brings in the server contact", refresh_and_find_contact)

if failures:
    print("FAILED:", ", ".join(failures))
    sys.exit(1)
print("all steps passed")
