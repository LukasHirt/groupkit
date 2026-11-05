# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

"""Runs DAVx5 in an Android emulator against a running example server.

The script drives a running emulator (or device) through adb. It installs the
DAVx5 APK, opens DAVx5's own login screen through a caldav:// link, taps
through account creation and then checks, with the content providers that
Android's Calendar and Contacts apps use, that items move in both directions.

Environment:
  GROUPKIT_PASSWORD   password of the server's account (required)
  GROUPKIT_USER       account name (default demo)
  GROUPKIT_URL        the server's DAV root as the host sees it
                      (default http://127.0.0.1:8080/dav/)
  GROUPKIT_DEVICE_URL the same address as the emulator sees it
                      (default: the host part replaced by 10.0.2.2)
  DAVX5_APK           path of the DAVx5 APK (required)
  ANDROID_SERIAL      which device adb talks to, if more than one is attached

The exit status is 1 if any step fails. A failure also prints the visible text
of the current screen, which is the first thing to read when a tap went wrong.
"""

import base64
import os
import re
import subprocess
import sys
import time
import traceback
import urllib.parse
import urllib.request

PASSWORD = os.environ["GROUPKIT_PASSWORD"]
USER = os.environ.get("GROUPKIT_USER", "demo")
URL = os.environ.get("GROUPKIT_URL", "http://127.0.0.1:8080/dav/")
BASE = URL if URL.endswith("/") else URL + "/"
_parts = urllib.parse.urlsplit(BASE)
DEVICE_URL = os.environ.get(
    "GROUPKIT_DEVICE_URL", f"{_parts.scheme}://10.0.2.2:{_parts.port or 80}{_parts.path}"
)
APK = os.environ["DAVX5_APK"]
PACKAGE = "at.bitfire.davdroid"
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
        print("screen:", screen_text(), flush=True)
        return None


def adb(*args, check=True):
    proc = subprocess.run(["adb", *args], capture_output=True, text=True, timeout=120)
    if check and proc.returncode != 0:
        raise RuntimeError(f"adb {' '.join(args)}: {proc.stdout}{proc.stderr}")
    return proc.stdout


def shell(command):
    return adb("shell", command)


def query(uri, projection):
    """Returns the rows of a content query as dicts."""
    out = shell(f"content query --uri {uri} --projection {projection}")
    rows = []
    for line in out.splitlines():
        if not line.startswith("Row:"):
            continue
        fields = re.split(r", (?=\w+=)", line.split(" ", 2)[2])
        rows.append(dict(f.split("=", 1) for f in fields))
    return rows


def ui_nodes():
    dump = adb("exec-out", "uiautomator", "dump", "/dev/tty", check=False)
    return [dict(re.findall(r'(\S+?)="([^"]*)"', m)) for m in re.findall(r"<node([^>]*)>", dump)]


def screen_text():
    try:
        return " | ".join(n["text"] for n in ui_nodes() if n.get("text") and "systemui" not in n.get("resource-id", ""))
    except Exception as exc:  # the screen text is a debugging aid only
        return f"(unavailable: {exc})"


def tap(label, seconds=30):
    """Taps the node whose text or content description is label, once it shows."""
    deadline = time.monotonic() + seconds
    while True:
        nodes = ui_nodes()
        if any(n.get("text", "").endswith("isn't responding") for n in nodes):
            for node in nodes:
                if node.get("text") == "Wait":
                    x1, y1, x2, y2 = map(int, re.findall(r"\d+", node["bounds"]))
                    shell(f"input tap {(x1 + x2) // 2} {(y1 + y2) // 2}")
        for node in nodes:
            if label in (node.get("text"), node.get("content-desc")):
                x1, y1, x2, y2 = map(int, re.findall(r"\d+", node["bounds"]))
                shell(f"input tap {(x1 + x2) // 2} {(y1 + y2) // 2}")
                return True
        if time.monotonic() > deadline:
            raise AssertionError(f"no {label!r} on the screen")
        time.sleep(1)


def http(method, url, body=None, headers=None):
    req = urllib.request.Request(url, data=body, method=method)
    req.add_header("Authorization", AUTH)
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    with urllib.request.urlopen(req, timeout=20) as resp:
        return resp.status, resp.read().decode()


def server_texts(collection_url, suffix):
    _, listing = http("PROPFIND", collection_url, b"", {"Depth": "1"})
    root = urllib.parse.urlsplit(collection_url).path
    texts = []
    for href in re.findall(r"<(?:\w+:)?href>([^<]+)</(?:\w+:)?href>", listing):
        if href.endswith(suffix) and href != root:
            texts.append(http("GET", urllib.parse.urljoin(collection_url, href))[1])
    return texts


def sync_now():
    try:
        tap("Synchronize now", seconds=5)
    except AssertionError:
        pass  # not on the account screen: the periodic sync will still run


def wait_for(fn, what, seconds=120):
    """Polls fn, asking DAVx5 to synchronize every few polls, until it is truthy."""
    deadline = time.monotonic() + seconds
    tries = 0
    while True:
        value = fn()
        if value:
            return value
        if time.monotonic() > deadline:
            raise AssertionError(f"timed out waiting for {what}")
        if tries % 4 == 0:
            sync_now()
        tries += 1
        time.sleep(3)


EVENT = (
    "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//groupkit//davx5 check//EN\r\n"
    "BEGIN:VEVENT\r\nUID:from-server@example\r\nDTSTAMP:20261101T100000Z\r\n"
    "DTSTART:20261112T090000Z\r\nDTEND:20261112T100000Z\r\nSUMMARY:Server event\r\n"
    "END:VEVENT\r\nEND:VCALENDAR\r\n"
)
CARD = (
    "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:from-server-card@example\r\nFN:Server Contact\r\n"
    "N:Contact;Server;;;\r\nEND:VCARD\r\n"
)
BOOK_ACCOUNT = "at.bitfire.davdroid.address_book"


def events():
    return query("content://com.android.calendar/events", "_id:title:dirty:deleted:_sync_id")


def raw_contacts():
    return query(
        "content://com.android.contacts/raw_contacts",
        "_id:dirty:deleted:sourceid:account_type",
    )


def contact_names():
    return [r["display_name"] for r in query("content://com.android.contacts/contacts", "display_name")]


def setup_account():
    # A slow emulator makes system apps show "isn't responding" dialogs that cover
    # DAVx5. Hide them, and wait for the home screen to settle.
    shell("settings put global hide_error_dialogs 1")
    shell("input keyevent KEYCODE_HOME")
    time.sleep(20)
    adb("install", "-r", APK)
    for perm in ("READ_CALENDAR", "WRITE_CALENDAR", "READ_CONTACTS", "WRITE_CONTACTS", "POST_NOTIFICATIONS"):
        adb("shell", "pm", "grant", PACKAGE, f"android.permission.{perm}", check=False)
    shell("input keyevent 82")  # wake up and dismiss the lock screen
    adb(
        "shell", "am", "start", "-a", "android.intent.action.VIEW",
        "-c", "android.intent.category.BROWSABLE",
        "-d", DEVICE_URL.replace(
            "http://", f"caldav://{urllib.parse.quote(USER, safe='')}:{urllib.parse.quote(PASSWORD, safe='')}@", 1),
    )
    tap("Login with URL and user name", seconds=60)
    tap("Continue")
    # The form opens with the soft keyboard up, which moves the Login button. Close
    # the keyboard, tap Login and, if the form is still there, try again.
    for attempt in range(4):
        time.sleep(3)
        if "mInputShown=true" in shell("dumpsys input_method"):
            shell("input keyevent KEYCODE_BACK")
            time.sleep(2)
        tap("Login")
        try:
            tap("Finish", seconds=45)  # waits for service discovery
            break
        except AssertionError:
            if attempt == 3:
                raise
    tap("synchronize this collection", seconds=60)  # the calendar
    tap("CardDAV")
    tap("synchronize this collection")  # the address book
    sync_now()
    return True


# The account screen starts with a calendar and an address book from the server.
step("add the account through the login screen", setup_account)
if failures:  # nothing below can work without the account
    print("FAILED:", ", ".join(failures))
    sys.exit(1)
step(
    "DAVx5 created the calendar and address book accounts",
    lambda: wait_for(
        lambda: BOOK_ACCOUNT in shell("dumpsys account") and "Personal" in str(
            query("content://com.android.calendar/calendars", "calendar_displayName")),
        "the calendar and the address book",
    ),
)

# Server to device.
step("write an event on the server",
     lambda: http("PUT", CAL_URL + "from-server.ics", EVENT.encode(),
                  {"Content-Type": "text/calendar; charset=utf-8"})[0] in (201, 204), expect=True)
step("write a contact on the server",
     lambda: http("PUT", BOOK_URL + "from-server-card.vcf", CARD.encode(),
                  {"Content-Type": "text/vcard; charset=utf-8"})[0] in (201, 204), expect=True)
step("the event reaches the phone",
     lambda: wait_for(lambda: any(e["title"] == "Server event" for e in events()), "the event"))
step("the contact reaches the phone",
     lambda: wait_for(lambda: "Server Contact" in contact_names(), "the contact"))

# Device to server. Rows written without the sync adapter flag are marked dirty and
# DAVx5 uploads them, the way an edit made in the Calendar or Contacts app is.
step("create an event on the phone",
     lambda: shell(
         "content insert --uri content://com.android.calendar/events --bind calendar_id:i:"
         + query("content://com.android.calendar/calendars", "_id")[0]["_id"]
         + " --bind title:s:Phone_event --bind dtstart:l:1794301200000"
           " --bind dtend:l:1794304800000 --bind eventTimezone:s:UTC") is not None)
step("the server holds the event from the phone",
     lambda: wait_for(lambda: any("SUMMARY:Phone_event" in t for t in server_texts(CAL_URL, ".ics")),
                      "the event on the server"))


def create_phone_contact():
    # DAVx5 names the address book's account after the server account and numbers it.
    match = re.search(rf"Account \{{name=(.+?), type={re.escape(BOOK_ACCOUNT)}\}}", shell("dumpsys account"))
    if match is None:
        raise AssertionError("no address book account on the phone")
    shell(
        f"content insert --uri content://com.android.contacts/raw_contacts"
        f" --bind account_name:s:'{match.group(1)}' --bind account_type:s:{BOOK_ACCOUNT}")
    rows = [r for r in raw_contacts() if r["account_type"] == BOOK_ACCOUNT and r["sourceid"] == "NULL"]
    raw_id = rows[-1]["_id"]
    shell(
        f"content insert --uri content://com.android.contacts/data --bind raw_contact_id:i:{raw_id}"
        " --bind mimetype:s:vnd.android.cursor.item/name --bind data1:s:'Phone Contact'"
        " --bind data2:s:Phone --bind data3:s:Contact")
    return True


step("create a contact on the phone", create_phone_contact)
step("the server holds the contact from the phone",
     lambda: wait_for(lambda: any("FN:Phone Contact" in t for t in server_texts(BOOK_URL, ".vcf")),
                      "the contact on the server"))

# Deletes in both directions.
step("delete the server's event on the server",
     lambda: http("DELETE", CAL_URL + "from-server.ics")[0] in (200, 204), expect=True)
step("the phone drops the event",
     lambda: wait_for(lambda: not any(e["title"] == "Server event" for e in events()), "the delete"))


def delete_phone_event():
    row = next(e for e in events() if e["title"] == "Phone_event")
    shell(f"content delete --uri content://com.android.calendar/events/{row['_id']}")
    return True


step("delete the phone's event on the phone", delete_phone_event)
step("the server drops the event from the phone",
     lambda: wait_for(lambda: not any("Phone_event" in t for t in server_texts(CAL_URL, ".ics")),
                      "the delete on the server"))

if failures:
    print("FAILED:", ", ".join(failures))
    sys.exit(1)
print("all steps passed")
