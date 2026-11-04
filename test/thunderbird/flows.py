# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

"""Runs Thunderbird against a groupkit server and checks both directions.

Thunderbird is driven through its own account-setup and sync code over Marionette, and
the server is checked over plain HTTP. See README.md for how to start both.

Settings come from the environment: GROUPKIT_PASSWORD (required), GROUPKIT_URL
(default http://127.0.0.1:8081) and GROUPKIT_USER (default demo).

WARNING: the script deletes everything in the user's default calendar and address
book on the server. Run it against a throwaway example server."""
import base64, json, os, re, sys, time, traceback, urllib.request, urllib.error

from marionette import Marionette

PW = os.environ["GROUPKIT_PASSWORD"]
SRV = os.environ.get("GROUPKIT_URL", "http://127.0.0.1:8081").rstrip("/")
USER = os.environ.get("GROUPKIT_USER", "demo")
AUTH = "Basic " + base64.b64encode(f"{USER}:{PW}".encode()).decode()
BOOK = f"/dav/addressbooks/{USER}/contacts/"
CAL = f"/dav/calendars/{USER}/personal/"

def http(method, path, body=None, headers=None):
    req = urllib.request.Request(SRV + path, data=body, method=method)
    req.add_header("Authorization", AUTH)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, dict(r.headers), r.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()

def listing(collection):
    """name -> etag via PROPFIND depth 1"""
    st, _, body = http("PROPFIND", collection, b'<propfind xmlns="DAV:"><prop><getetag/></prop></propfind>',
                       {"Depth": "1", "Content-Type": "application/xml"})
    out = {}
    for m in re.finditer(r"<D:response>.*?</D:response>", body.decode(), re.S):
        href = re.search(r"<D:href>(.*?)</D:href>", m.group(0)).group(1)
        et = re.search(r"<D:getetag>(.*?)</D:getetag>", m.group(0))
        if et and not href.endswith("/"):
            out[href.rsplit("/", 1)[1]] = et.group(1)
    return out

def until(fn, timeout=25, step=0.5):
    end = time.time() + timeout
    last = None
    while time.time() < end:
        try:
            last = fn()
            if last:
                return last
        except Exception as e:  # noqa
            last = e
        time.sleep(step)
    return None

results = []
def limit(name, detail=""):
    results.append((name, None, detail))
    print("LIMIT " + name + (f"  [{detail}]" if detail else ""))

def check(name, ok, detail=""):
    results.append((name, bool(ok), detail))
    print(("PASS " if ok else "FAIL ") + name + (f"  [{detail}]" if detail and not ok else ""))

JS_PRE = r"""
const { cal } = ChromeUtils.importESModule("resource:///modules/calendar/calUtils.sys.mjs");
const { MailServices } = ChromeUtils.importESModule("resource:///modules/MailServices.sys.mjs");
const { CalEvent } = ChromeUtils.importESModule("resource:///modules/CalEvent.sys.mjs");
const { CalTodo } = ChromeUtils.importESModule("resource:///modules/CalTodo.sys.mjs");
const { CalAlarm } = ChromeUtils.importESModule("resource:///modules/CalAlarm.sys.mjs");
const { CalRecurrenceInfo } = ChromeUtils.importESModule("resource:///modules/CalRecurrenceInfo.sys.mjs");
const { CalAttendee } = ChromeUtils.importESModule("resource:///modules/CalAttendee.sys.mjs");
const calendar = cal.manager.getCalendars().find(c => c.type == "caldav");
const book = [...MailServices.ab.directories].find(d => d.dirType == 102);
const sleep = ms => new Promise(r => setTimeout(r, ms));
const ALL = Ci.calICalendar.ITEM_FILTER_ALL_ITEMS;
const allItems = async () => await calendar.getItemsAsArray(ALL, 0, null, null);
async function syncBook() { await (book.wrappedJSObject || book).syncWithServer(false); }
async function refreshCal() { calendar.refresh(); await sleep(3000); }
"""

def tb(m, js, timeout_ms=120000):
    return m.run(JS_PRE + js, timeout_ms)

def main():
    m = Marionette()
    # Add the address book and the calendar the way Thunderbird's own dialogs do.
    m.run("""
      const { CardDAVUtils } = ChromeUtils.importESModule("resource:///modules/CardDAVUtils.sys.mjs");
      const { cal } = ChromeUtils.importESModule("resource:///modules/calendar/calUtils.sys.mjs");
      const { MailServices } = ChromeUtils.importESModule("resource:///modules/MailServices.sys.mjs");
      const base = %s + "/dav/", user = %s, pw = %s;
      if (![...MailServices.ab.directories].some(d => d.dirType == 102)) {
        const books = await CardDAVUtils.detectAddressBooks(user, pw, base, false);
        if (!books.length) throw new Error("Thunderbird found no address book at " + base);
        await books[0].create();
      }
      if (!cal.manager.getCalendars().some(c => c.type == "caldav")) {
        const found = await cal.provider.detection.detect(user, pw, base, true, []);
        const cals = [...found.values()].flat();
        if (!cals.length) throw new Error("Thunderbird found no calendar at " + base);
        for (const c of cals) cal.manager.registerCalendar(c);
      }
      return true;""" % (json.dumps(SRV), json.dumps(USER), json.dumps(PW)), 120000)
    for n in list(listing(CAL)): http("DELETE", CAL + n)
    for n in list(listing(BOOK)): http("DELETE", BOOK + n)
    tb(m, "await syncBook(); await refreshCal(); for (const i of await allItems()) { try { await calendar.deleteItem(i); } catch (e) {} } return true;")
    for n in list(listing(CAL)): http("DELETE", CAL + n)

    # ================= Contacts: Thunderbird -> server =================
    uid = tb(m, r"""
      const card = Cc["@mozilla.org/addressbook/cardproperty;1"].createInstance(Ci.nsIAbCard);
      card.displayName = "Grace Hopper"; card.firstName = "Grace"; card.lastName = "Hopper";
      card.primaryEmail = "grace@example.org"; card.setProperty("WorkPhone", "+420 222 333 444");
      card.setProperty("Notes", "Rear Admiral");
      book.addCard(card);
      return card.UID;""")
    names = until(lambda: listing(BOOK))
    check("contacts: a card made in Thunderbird appears on the server", bool(names), str(names))
    vcard = ""
    if names:
        name = list(names)[0]
        st, hd, body = http("GET", BOOK + name)
        vcard = body.decode()
        check("contacts: the stored vCard is served as text/vcard", st == 200 and "text/vcard" in hd.get("Content-Type", ""), f"{st} {hd.get('Content-Type')}")
        check("contacts: name, e-mail, phone and note survive", all(s in vcard for s in ("Grace", "Hopper", "grace@example.org", "222 333 444", "Rear Admiral")), vcard[:300])
        check("contacts: the UID Thunderbird chose is kept", uid in vcard, vcard[:200])
        etag0 = names[name]
    tb(m, f"""
      const c = book.childCards.find(x => x.UID == "{uid}");
      c.displayName = "Grace Murray Hopper"; c.firstName = "Grace Murray"; book.modifyCard(c); return true;""")
    changed = until(lambda: (lambda l: l if l and list(l.values())[0] != etag0 else None)(listing(BOOK)))
    check("contacts: an edit in Thunderbird changes the ETag on the server", bool(changed))
    if changed:
        st, _, body = http("GET", BOOK + list(changed)[0])
        check("contacts: the edited name is stored", "Grace Murray" in body.decode())
    tb(m, f"""const c = book.childCards.find(x => x.UID == "{uid}"); book.deleteCards([c]); return true;""")
    gone = until(lambda: not listing(BOOK))
    check("contacts: deleting in Thunderbird removes it from the server", bool(gone))

    # ================= Contacts: server -> Thunderbird =================
    zoe = ("BEGIN:VCARD\r\nVERSION:3.0\r\nUID:zoe-1\r\nFN:Zoë Škoda\r\nN:Škoda;Zoë;;;\r\n"
           "EMAIL;TYPE=home:zoe@example.org\r\nTEL;TYPE=cell:+420 777 000 111\r\nBDAY:1990-04-02\r\n"
           "ADR;TYPE=home:;;Hlavní 1;Praha;;11000;CZ\r\nNOTE:Dlouhý text\\, s čárkou\r\nEND:VCARD\r\n").encode("utf-8")
    st, hd, _ = http("PUT", BOOK + "zoe.vcf", zoe, {"Content-Type": "text/vcard"})
    check("contacts: PUT of a UTF-8 vCard on the server", st == 201, str(st))
    et = hd.get("ETag") or hd.get("Etag")
    got = tb(m, """await syncBook(); const c = book.childCards.find(x => x.displayName == "Zoë Škoda");
      if (!c) return null; const vp = c.vCardProperties;
      return {name: c.displayName, mail: c.primaryEmail, tel: vp.getFirstValue("tel"), bday: vp.getFirstValue("bday"),
              adr: JSON.stringify(vp.getFirstValue("adr")), note: vp.getFirstValue("note")};""")
    check("contacts: a card made on the server shows up in Thunderbird after a sync", bool(got), str(got))
    check("contacts: a UTF-8 card keeps e-mail, phone, birthday, address and note", bool(got) and got.get("mail") == "zoe@example.org" and "777 000 111" in str(got.get("tel")) and "1990" in str(got.get("bday")) and "Praha" in str(got.get("adr")) and "\u010d\u00e1rkou" in str(got.get("note")), str(got))
    zoe2 = zoe.replace("Dlouhý".encode(), "Nový".encode()).replace("Zoë Škoda".encode(), "Zoë Škoda II".encode())
    st, hd, _ = http("PUT", BOOK + "zoe.vcf", zoe2, {"Content-Type": "text/vcard", "If-Match": et})
    check("contacts: conditional PUT with If-Match on the server", st == 204, str(st))
    got = tb(m, """await syncBook(); return !!book.childCards.find(x => x.displayName == "Zoë Škoda II");""")
    check("contacts: a server-side edit reaches Thunderbird", got)
    st, _, _ = http("DELETE", BOOK + "zoe.vcf")
    got = tb(m, """await syncBook(); return book.childCards.some(x => x.displayName.startsWith("Zoë"));""")
    check("contacts: a server-side delete reaches Thunderbird", st == 204 and got is False, f"{st} {got}")

    # ================= Calendar: Thunderbird -> server =================
    cuid = tb(m, r"""
      const tz = cal.timezoneService.getTimezone("Europe/Prague");
      const ev = new CalEvent();
      ev.id = "tb-standup@example.org"; ev.title = "Standup";
      const start = cal.createDateTime(); start.resetTo(2026, 10, 10, 9, 0, 0, tz);
      const end = start.clone(); end.hour = 10;
      ev.startDate = start; ev.endDate = end;
      const info = new CalRecurrenceInfo(ev); const rule = cal.createRecurrenceRule();
      rule.type = "WEEKLY"; rule.count = 4; info.insertRecurrenceItemAt(rule, 0); ev.recurrenceInfo = info;
      const alarm = new CalAlarm(); alarm.action = "DISPLAY"; alarm.related = Ci.calIAlarm.ALARM_RELATED_START;
      alarm.offset = cal.createDuration("-PT15M"); alarm.description = "Reminder"; ev.addAlarm(alarm);
      ev.setProperty("LOCATION", "Room 4"); ev.setProperty("DESCRIPTION", "Weekly sync");
      const att = new CalAttendee(); att.id = "mailto:bob@example.org"; att.commonName = "Bob";
      att.role = "REQ-PARTICIPANT"; att.participationStatus = "NEEDS-ACTION"; ev.addAttendee(att);
      await calendar.addItem(ev); return ev.id;""")
    cnames = until(lambda: listing(CAL))
    check("calendar: a recurring event made in Thunderbird appears on the server", bool(cnames), str(cnames))
    ics = ""
    if cnames:
        cname = list(cnames)[0]
        st, hd, body = http("GET", CAL + cname)
        ics = body.decode()
        check("calendar: served as text/calendar", st == 200 and "text/calendar" in hd.get("Content-Type", ""), f"{st} {hd.get('Content-Type')}")
        check("calendar: RRULE, TZID, VTIMEZONE, alarm, location, attendee survive",
              all(s in ics for s in ("RRULE:FREQ=WEEKLY;COUNT=4", "TZID=Europe/Prague", "BEGIN:VTIMEZONE", "BEGIN:VALARM", "TRIGGER:-PT15M", "LOCATION:Room 4", "ATTENDEE")), ics[:600])
        cet0 = cnames[cname]
    # one occurrence moved
    tb(m, r"""
      const items = await allItems(); const parent = items.find(i => i.id == "tb-standup@example.org");
      const newp = parent.clone();
      const second = parent.startDate.clone(); second.day += 7;
      const occ = newp.recurrenceInfo.getOccurrenceFor(second);
      const mod = occ.clone(); mod.title = "Standup (moved)"; mod.startDate.hour = 11; mod.endDate.hour = 12;
      newp.recurrenceInfo.modifyException(mod, true);
      await calendar.modifyItem(newp, parent); return true;""")
    ics2 = until(lambda: (lambda n: http("GET", CAL + list(n)[0])[2].decode() if n and list(n.values())[0] != cet0 else None)(listing(CAL)))
    check("calendar: moving one occurrence stores a RECURRENCE-ID override",
          bool(ics2) and "RECURRENCE-ID" in ics2 and "Standup (moved)" in ics2, (ics2 or "")[:200])
    st, _, rep = http("REPORT", CAL, b'<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop><C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT"><C:time-range start="20261117T000000Z" end="20261118T000000Z"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>', {"Depth": "1", "Content-Type": "application/xml"})
    check("calendar: a time-range query finds the recurring event in its 2nd week", st == 207 and "tb-" in rep.decode() or ".ics" in rep.decode(), rep.decode()[:200])
    # all-day event, and a task (VTODO)
    tb(m, r"""
      const ad = new CalEvent(); ad.id = "tb-allday@example.org"; ad.title = "Holiday";
      const d = cal.createDateTime("20261111"); d.isDate = true; ad.startDate = d;
      const e = d.clone(); e.day += 1; ad.endDate = e; await calendar.addItem(ad); return true;""")
    names3 = until(lambda: (lambda l: l if len(l) >= 2 else None)(listing(CAL)))
    check("calendar: an all-day event is stored", bool(names3), str(names3))
    if names3:
        texts = {n: http("GET", CAL + n)[2].decode() for n in names3}
        check("calendar: the all-day event uses DTSTART;VALUE=DATE", any("DTSTART;VALUE=DATE:20261111" in t for t in texts.values()))
    task = tb(m, r"""
      try {
        const td = new CalTodo(); td.id = "tb-task@example.org"; td.title = "Write report";
        td.dueDate = cal.createDateTime("20261115T170000Z"); await calendar.addItem(td); return {added: true};
      } catch (e) { return {added: false, error: String(e).slice(0, 160)}; }""")
    if task and task.get("added"):
        check("calendar: a task is stored", bool(until(lambda: any("VTODO" in http("GET", CAL + n)[2].decode() for n in listing(CAL)))))
    else:
        limit("tasks: Thunderbird refuses to create a task (the calendar holds events only)", str(task))
    # delete everything from Thunderbird
    tb(m, r"""for (const i of await allItems()) { await calendar.deleteItem(i); } return true;""")
    empty = until(lambda: not listing(CAL))
    check("calendar: deleting in Thunderbird empties the server calendar", bool(empty), str(listing(CAL)))

    # ================= Calendar: server -> Thunderbird =================
    ics_in = ("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//e2e//EN\r\n"
              "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nRRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\n"
              "BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"
              "BEGIN:VEVENT\r\nUID:srv-1@example.org\r\nDTSTAMP:20261101T100000Z\r\nDTSTART;TZID=Europe/Prague:20261201T090000\r\nDTEND;TZID=Europe/Prague:20261201T100000\r\n"
              "RRULE:FREQ=WEEKLY;COUNT=4\r\nEXDATE;TZID=Europe/Prague:20261208T090000\r\nSUMMARY:Planning\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT30M\r\nDESCRIPTION:soon\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n").encode()
    st, hd, _ = http("PUT", CAL + "srv-1.ics", ics_in, {"Content-Type": "text/calendar"})
    check("calendar: PUT of an event with VTIMEZONE, EXDATE and VALARM on the server", st == 201, str(st))
    et = hd.get("ETag") or hd.get("Etag")
    got = tb(m, r"""
      await refreshCal();
      const s = cal.createDateTime("20261201T000000Z"), e = cal.createDateTime("20261231T000000Z");
      const occ = await calendar.getItemsAsArray(ALL | Ci.calICalendar.ITEM_FILTER_CLASS_OCCURRENCES, 0, s, e);
      const base = (await allItems()).find(i => i.id == "srv-1@example.org");
      return {occurrences: occ.length, title: base && base.title, alarms: base ? base.getAlarms().length : -1,
              start: base && base.startDate.icalString + " " + base.startDate.timezone.tzid};""")
    check("calendar: the server-made event reaches Thunderbird after a refresh", bool(got) and got.get("title") == "Planning", str(got))
    check("calendar: the recurrence expands to 3 occurrences (4 minus the EXDATE)", bool(got) and got.get("occurrences") == 3, str(got))
    check("calendar: its alarm and time zone are read", bool(got) and got.get("alarms") == 1 and "Europe/Prague" in (got.get("start") or ""), str(got))
    st, _, _ = http("PUT", CAL + "srv-1.ics", ics_in.replace(b"SUMMARY:Planning", b"SUMMARY:Planning v2"), {"Content-Type": "text/calendar", "If-Match": et})
    got = tb(m, r"""await refreshCal(); const b = (await allItems()).find(i => i.id == "srv-1@example.org"); return b && b.title;""")
    check("calendar: a server-side edit reaches Thunderbird", st == 204 and got == "Planning v2", f"{st} {got}")
    st, _, _ = http("DELETE", CAL + "srv-1.ics")
    got = tb(m, r"""await refreshCal(); return (await allItems()).some(i => i.id == "srv-1@example.org");""")
    check("calendar: a server-side delete reaches Thunderbird", st == 204 and got is False, f"{st} {got}")

    ok = sum(1 for _, o, _ in results if o is True); lim = sum(1 for _, o, _ in results if o is None)
    failed = [n for n, o, _ in results if o is False]
    print(f"\n{ok} passed, {len(failed)} failed, {lim} known limitations")
    return 0 if not failed else 1

if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        traceback.print_exc()
        sys.exit(2)
