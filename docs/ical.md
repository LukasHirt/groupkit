<!--
SPDX-FileCopyrightText: 2026 groupkit contributors

SPDX-License-Identifier: Apache-2.0
-->

# The iCalendar package

`ical` parses and validates calendar object resources and answers calendar
queries against them. A query can ask for time ranges, which means expanding
recurrences, applying overrides and mapping time zones, and it can ask for
text matches. This document lists what the package accepts, how it reads
times, and the limits it puts on untrusted input.

Every limit below comes from a constant in the package. When input goes over
a limit, the package either returns an error or ignores the part that broke
it, and each section says which.

## Validation

`Validate(data, allowed)` checks the rules for a stored calendar object
resource (RFC 4791, section 4.1) and returns its UID and component type.

| Rule | Error it wraps |
|---|---|
| The data is one `BEGIN:VCALENDAR` ... `END:VCALENDAR` block whose BEGIN and END lines nest, with no empty component name and no VCALENDAR inside it | `ErrInvalid` |
| `VERSION:2.0` (surrounding white space ignored) and a `PRODID`, which may be empty | `ErrInvalid` |
| No `METHOD` | `ErrInvalid` |
| At least one component besides VTIMEZONE, and all of those of one type | `ErrInvalid` |
| That type is in `allowed`; an empty or nil list means VEVENT only | `ErrUnsupportedComponent` |
| Every one of those components has the same UID, and none is empty | `ErrUID` |

Lines are parsed with a limit of 100,000 physical lines and 16 MiB per
unfolded line. The caller has to bound the size of the whole object.

Names of components, properties and parameters may be in any case. Only
ASCII letters are folded. A non-ASCII lookalike such as `ı` (U+0131) or `ſ`
(U+017F) never stands for `I` or `S`, so `BEGIN:VTıMEZONE` is not a
VTIMEZONE. The same rule applies to VALUE parameters and RRULE text.

`Parse` checks the structure only. It does not check the rules in the
table, apart from the first row.

## Time zones

A `Resolver` turns DATE and DATE-TIME values into instants. It looks up a
TZID in this order:

1. The VTIMEZONEs of the calendar object (see the next section).
2. The Windows zone names from CLDR release 46.1, in any ASCII case, such
   as `W. Europe Standard Time`.
3. The system zone database, with the name as given.
4. The zone database again, with leading path elements removed, trying at
   most the last 8 elements. `/example.org/Tzfile/Europe/Berlin` finds
   `Europe/Berlin`.
5. UTC.

White space around a TZID is ignored. An unknown TZID is read as UTC and is
never an error. Some names never reach the database: those longer than 256
bytes, those that contain `..`, a backslash or NUL, `Local`, and any path
element with a character other than ASCII letters, digits and `_+-`.

Zones loaded from the database are cached for the whole process, up to 1,024
of them. A Resolver stops reading the database after 64 lookups that found
nothing. Once the process cache is full, a lookup that does find a zone also
counts toward those 64, since the next request for it reads the file again. After that, a name resolves only if it is embedded in the object,
is a Windows name, or is already in the process-wide cache; any other name
is read as UTC. A calendar with thousands of made-up TZIDs therefore costs
at most 64 file reads. `Matches` and `Occurrences` without a Resolver create
a new one for each call, so each call gets its own budget.

When clocks go forward, a skipped wall time takes the offset from before the
change. When clocks go back, a repeated wall time means the earlier of its
two instants. Floating times and DATE values are read in the location the
caller passes, or in UTC when it passes nil.

The Windows table is generated. To regenerate it, run `go generate ./ical`.
That needs network access to download `windowsZones.xml` from CLDR.

## Embedded VTIMEZONEs

`NewResolver` reads at most the first 50 VTIMEZONEs. Across all of them, at
most 50 STANDARD and DAYLIGHT blocks may carry an RRULE, because each rule is
expanded again for every stretch of years a query looks at. A VTIMEZONE that
would go over that budget is skipped. When several VTIMEZONEs share a TZID,
the first one that parses wins.

Each VTIMEZONE has to follow these rules:

- At most 100 observances, and at most 1,000 RDATE values in total.
- Each observance has TZOFFSETFROM, TZOFFSETTO and a DATE-TIME DTSTART.
  Offsets are `+hhmm` or `+hhmmss` with either sign. `+hh:mm` is rejected.
- RDATE values are DATE-TIMEs. A PERIOD or a DATE is rejected.
- At most one RRULE per observance, with FREQ=YEARLY or FREQ=MONTHLY.
- At most one BYHOUR, BYMINUTE and BYSECOND value, at most 31 values in
  each other BYxxx list, and at most 25 onsets a year, checked over the
  rule's first two years.
- COUNT is at most 400. The rule is expanded once and COUNT becomes an
  UNTIL at the last onset.
- A rule with no instances at all is dropped, and its observance keeps its
  DTSTART and RDATE onsets.

A VTIMEZONE that breaks any of these rules is ignored as a whole. Its TZID
then goes to the zone database and, failing that, to UTC.

A Resolver also limits the work of evaluating a VTIMEZONE. The rules are
expanded for eight years at a time, and the results for 64 such blocks are
kept. Each block that has to be expanded costs the rule instances it
produces plus the periods it walks. A VTIMEZONE with n rules may spend
20,000 times n. A VTIMEZONE whose rules need more than that in one Resolver
is dropped, and from then on its TZID resolves as if the VTIMEZONE had failed
to parse. A real zone evaluated over two centuries uses a few percent of its
budget.

## Values

A DATE is midnight of its day. DTEND is exclusive, so an all-day event from
`DTSTART;VALUE=DATE:20260410` to `DTEND;VALUE=DATE:20260411` lasts one day.
All-day lengths are counted in calendar days of the floating location. An
all-day event without DTEND or DURATION lasts one day, and so does one whose
DTEND is on or before its DTSTART.

A DURATION has weeks alone or days and time parts in order (`P1W`,
`P1DT2H`, `-PT15M`). A day in a DURATION is exactly 24 hours. A value that
would overflow is an error.

TZID is ignored on UTC and DATE values. VALUE=DATE forces a DATE and
VALUE=DATE-TIME rejects one. When a parameter has several values, the first
counts.

## Recurrence

`Occurrences` returns the occurrences of an object's VEVENTs that overlap a
window, sorted by start.

- The VEVENT without RECURRENCE-ID is the master. Only its first RRULE is
  used. Its DTSTART, RDATEs and rule instances make up the recurrence set,
  minus the EXDATEs. A DTSTART that the rule does not generate is still an
  occurrence, and it comes on top of COUNT. COUNT counts from DTSTART,
  before EXDATE removes anything.
- An object may have at most 1,000 RDATE values and 10,000 EXDATE values.
  More is an error. An RDATE PERIOD counts as its start.
- At most 10,000 occurrences are examined. Past that, the result is cut
  short without an error.
- A window is at most 100 years long, counted from its start, back from its
  end when the start is open, and from DTSTART when both sides are open.
- An occurrence overlaps when it starts before the window's end and ends
  after its start. An occurrence without duration overlaps when it starts
  at or after the window's start and before its end.
- The rule runs on wall-clock time in DTSTART's zone, so a weekly 09:00
  meeting stays at 09:00 across a change of offset. A UTC UNTIL is converted
  to that wall time first. A DATE UNTIL covers its whole day.
- HOURLY, MINUTELY and SECONDLY rules are rejected when their BYHOUR,
  BYMINUTE and BYSECOND let them through on fewer than one in 2,000 steps.
  They are also rejected when they filter both times of day and days while
  INTERVAL does not divide a day.
- Overrides match an occurrence by instant, and the first override for an
  instant wins. RANGE=THISANDFUTURE is ignored. Overrides with
  STATUS:CANCELLED are returned, and the caller decides what to do with
  them.

A recurrence that cannot be expanded is an `ErrRecurrence` from
`Occurrences`. In `Matches` it has no occurrences and makes no time range
match.

## Queries

`Calendar.Matches` evaluates a calendar-query filter (RFC 4791, section
9.7). Only an invalid filter is an error; nothing in the calendar data is.

A filter may nest comp-filters at most 16 deep, have at most 1,000 comp-,
prop- and param-filters together, and at most 16 comp-filter time ranges.

A comp-filter time range applies only to VEVENTs at the top level of the
object. It tests the recurrence set of the whole object, overrides
included, so with a master and overrides the filter's properties may be
satisfied by a different VEVENT than the occurrence in the range. A range
with both sides open matches every VEVENT. A time range on a VTODO, a VALARM
or any nested component matches nothing.

A prop-filter time range works on DTSTART, DTEND, DUE, DTSTAMP, CREATED,
LAST-MODIFIED, COMPLETED, RECURRENCE-ID and ACKNOWLEDGED. It tests the
property's instant against `[start, end)`. Other properties, and values that
do not parse, never match.

Text matches compare a property value with its backslash escapes resolved,
so `SUMMARY:Team\, meeting` contains `Team, meeting`. Parameter values are
compared as written. Negation applies to each property instance on its own.
The collations are `i;octet`, `i;ascii-casemap` and `i;unicode-casemap`,
which is the default. The match types are `equals`, `contains` (the
default), `starts-with` and `ends-with`. Each value is folded once per call
and collation. One call folds at most 64 MiB of text, and past that no text
match holds, negated or not.

## Concurrency

A `Calendar` is not changed after `Parse`. A `Resolver` is safe for
concurrent use, and so are `Matches` and `Occurrences` on one Calendar.
