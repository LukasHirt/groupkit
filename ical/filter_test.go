// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventsIn builds a VCALENDAR filter for a VEVENT in the window.
func eventsIn(start, end time.Time) *CompFilter {
	return &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{
		{Name: "VEVENT", TimeRange: &TimeRange{Start: start, End: end}},
	}}
}

// pragueCal parses events together with the Europe/Prague VTIMEZONE.
func pragueCal(t *testing.T, events ...string) *Calendar {
	t.Helper()
	return mustCal(t, append([]string{pragueTZ}, events...)...)
}

func checkMatch(t *testing.T, c *Calendar, f *CompFilter, floating *time.Location, want bool) {
	t.Helper()
	got, err := c.Matches(f, floating)
	if err != nil {
		t.Fatalf("Matches: %v", err)
	}
	if got != want {
		t.Fatalf("Matches = %v, want %v", got, want)
	}
}

const winTZ = "TZID=W. Europe Standard Time"

func TestMatchesTimeRange(t *testing.T) {
	weekly := []string{
		"DTSTART;" + prague + ":20260316T090000",
		"DTEND;" + prague + ":20260316T100000",
		"RRULE:FREQ=WEEKLY;BYDAY=MO",
	}
	exdated := vevent(append(weekly, "EXDATE;"+prague+":20260323T090000")...)
	moved := []string{vevent(weekly...), vevent("RECURRENCE-ID;"+prague+":20260323T090000",
		"DTSTART;"+prague+":20260324T140000", "DTEND;"+prague+":20260324T150000")}
	allDay := vevent("DTSTART;VALUE=DATE:20260310", "DTEND;VALUE=DATE:20260311")
	floatingEv := vevent("DTSTART:20260310T090000", "DTEND:20260310T100000")
	count3 := vevent(append(weekly[:2], "RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3")...)
	daily := vevent("DTSTART;"+prague+":20260327T090000", "DTEND;"+prague+":20260327T100000", "RRULE:FREQ=DAILY")
	until := vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
		"RRULE:FREQ=DAILY;UNTIL=20260319T080000Z")
	duration := vevent("DTSTART:20260310T090000Z", "DURATION:PT1H30M")
	noEnd := vevent("DTSTART;" + prague + ":20260310T090000")
	day31 := vevent("DTSTART;"+prague+":20260131T090000", "DURATION:PT1H", "RRULE:FREQ=MONTHLY;BYMONTHDAY=31")
	hour := vevent("DTSTART:20260310T090000Z", "DTEND:20260310T100000Z")
	long := vevent("DTSTART:20260310T090000Z", "DTEND:20260315T090000Z")
	threeDays := vevent("DTSTART;VALUE=DATE:20260302", "DTEND;VALUE=DATE:20260305", "RRULE:FREQ=WEEKLY;COUNT=3")
	unknownTZ := vevent("DTSTART;TZID=Custom/Unknown:20260310T090000", "DTEND;TZID=Custom/Unknown:20260310T100000")
	windows := vevent("DTSTART;"+winTZ+":20260310T090000", "DTEND;"+winTZ+":20260310T100000")
	windowsTZ := strings.Replace(pragueTZ, "TZID:Europe/Prague", "TZID:W. Europe Standard Time", 1)

	tests := []struct {
		name       string
		blocks     []string
		floating   string
		start, end time.Time
		want       bool
	}{
		{"EXDATE removes the date", []string{exdated}, "", at(2026, 3, 23, 8, 0), at(2026, 3, 23, 9, 0), false},
		{"EXDATE neighbour matches", []string{exdated}, "", at(2026, 3, 30, 7, 0), at(2026, 3, 30, 8, 0), true},
		{"moved occurrence at its new time", moved, "", at(2026, 3, 24, 13, 0), at(2026, 3, 24, 14, 0), true},
		{"moved occurrence not at its old time", moved, "", at(2026, 3, 23, 8, 0), at(2026, 3, 23, 9, 0), false},
		{"all-day on its own UTC day", []string{allDay}, "", at(2026, 3, 10, 0, 0), at(2026, 3, 11, 0, 0), true},
		{"all-day window inside the day", []string{allDay}, "", at(2026, 3, 10, 12, 0), at(2026, 3, 10, 13, 0), true},
		{"all-day not the next day", []string{allDay}, "", at(2026, 3, 11, 0, 0), at(2026, 3, 12, 0, 0), false},
		{"floating as UTC", []string{floatingEv}, "", at(2026, 3, 10, 8, 30), at(2026, 3, 10, 10, 30), true},
		{"floating in Prague", []string{floatingEv}, "Europe/Prague", at(2026, 3, 10, 7, 30), at(2026, 3, 10, 8, 30), true},
		{"floating in Prague is not UTC", []string{floatingEv}, "Europe/Prague", at(2026, 3, 10, 9, 0), at(2026, 3, 10, 10, 0), false},
		{"COUNT=3 third matches", []string{count3}, "", at(2026, 3, 30, 7, 0), at(2026, 3, 30, 8, 0), true},
		{"COUNT=3 fourth does not", []string{count3}, "", at(2026, 4, 6, 7, 0), at(2026, 4, 6, 8, 0), false},
		{"DST day at 07:00Z", []string{daily}, "", at(2026, 3, 29, 7, 0), at(2026, 3, 29, 7, 10), true},
		{"DST day not at 08:10Z", []string{daily}, "", at(2026, 3, 29, 8, 10), at(2026, 3, 29, 8, 20), false},
		{"UNTIL day four", []string{until}, "", at(2026, 3, 19, 8, 0), at(2026, 3, 19, 9, 0), true},
		{"UNTIL not day five", []string{until}, "", at(2026, 3, 20, 0, 0), at(2026, 3, 21, 0, 0), false},
		{"DURATION inside", []string{duration}, "", at(2026, 3, 10, 10, 0), at(2026, 3, 10, 10, 10), true},
		{"DURATION after", []string{duration}, "", at(2026, 3, 10, 10, 30), at(2026, 3, 10, 11, 0), false},
		{"no DTEND around the instant", []string{noEnd}, "", at(2026, 3, 10, 7, 59), at(2026, 3, 10, 8, 1), true},
		{"no DTEND at the window start", []string{noEnd}, "", at(2026, 3, 10, 8, 0), at(2026, 3, 10, 8, 1), true},
		{"no DTEND at the window end", []string{noEnd}, "", at(2026, 3, 10, 7, 0), at(2026, 3, 10, 8, 0), false},
		{"day 31 skips February", []string{day31}, "", at(2026, 2, 1, 0, 0), at(2026, 3, 1, 0, 0), false},
		{"day 31 in March", []string{day31}, "", at(2026, 3, 31, 7, 0), at(2026, 3, 31, 8, 0), true},
		{"starts at the window start", []string{hour}, "", at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), true},
		{"ends at the window start", []string{hour}, "", at(2026, 3, 10, 10, 0), at(2026, 3, 10, 11, 0), false},
		{"starts at the window end", []string{hour}, "", at(2026, 3, 10, 8, 0), at(2026, 3, 10, 9, 0), false},
		{"window inside a long occurrence", []string{long}, "", at(2026, 3, 12, 10, 0), at(2026, 3, 12, 11, 0), true},
		{"three-day all-day on its second day", []string{threeDays}, "", at(2026, 3, 10, 10, 0), at(2026, 3, 10, 11, 0), true},
		{"three-day all-day end is exclusive", []string{threeDays}, "", at(2026, 3, 12, 0, 0), at(2026, 3, 13, 0, 0), false},
		{"unknown TZID matches as UTC", []string{unknownTZ}, "", at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), true},
		{"unknown TZID is not Prague", []string{unknownTZ}, "", at(2026, 3, 10, 8, 0), at(2026, 3, 10, 9, 0), false},
		{"Windows TZID with VTIMEZONE", []string{windowsTZ, windows}, "", at(2026, 3, 10, 8, 0), at(2026, 3, 10, 8, 30), true},
		{"Windows TZID with VTIMEZONE is not UTC", []string{windowsTZ, windows}, "", at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), false},
		{"Windows TZID without VTIMEZONE", []string{windows}, "zonedb", at(2026, 3, 10, 8, 0), at(2026, 3, 10, 8, 30), true},
		{"Windows TZID without VTIMEZONE is not UTC", []string{windows}, "zonedb", at(2026, 3, 10, 9, 0), at(2026, 3, 10, 9, 30), false},
		{"open start", []string{hour}, "", time.Time{}, at(2026, 3, 10, 9, 1), true},
		{"open end", []string{hour}, "", at(2026, 3, 10, 9, 59), time.Time{}, true},
		{"open end after the event", []string{hour}, "", at(2026, 3, 10, 10, 0), time.Time{}, false},
		{"both open", []string{hour}, "", time.Time{}, time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var floating *time.Location
			switch tt.floating {
			case "":
			case "zonedb":
				needZone(t, "Europe/Berlin")
			default:
				floating = needZone(t, tt.floating)
			}
			c := pragueCal(t, tt.blocks...)
			checkMatch(t, c, eventsIn(tt.start, tt.end), floating, tt.want)
		})
	}
}

// TestMatchesOddObjects runs one query over a collection where some objects
// cannot be expanded or name unknown zones; the ordinary one still matches.
func TestMatchesOddObjects(t *testing.T) {
	f := eventsIn(at(2026, 3, 10, 9, 0), at(2026, 3, 10, 10, 0))
	objects := []struct {
		name string
		cal  *Calendar
		want bool
	}{
		{"malformed RRULE", mustCal(t, vevent("DTSTART:20260310T090000Z", "RRULE:FREQ=SOMETIMES")), false},
		{"unknown RRULE part", mustCal(t, vevent("DTSTART:20260310T090000Z", "RRULE:FREQ=DAILY;X=1")), false},
		{"missing DTSTART", mustCal(t, vevent("SUMMARY:x")), false},
		{"bad DTEND", mustCal(t, vevent("DTSTART:20260310T090000Z", "DTEND:tomorrow")), false},
		{"unknown TZID", mustCal(t, vevent("DTSTART;TZID=Custom/Unknown:20260310T091500")), true},
		{"ordinary", pragueCal(t, vevent("DTSTART;"+prague+":20260310T100000", "DURATION:PT1H")), true},
	}
	for _, o := range objects {
		got, err := o.cal.Matches(f, nil)
		if err != nil || got != o.want {
			t.Errorf("%s: Matches = %v, %v; want %v, nil", o.name, got, err, o.want)
		}
	}
	// Without a time range the malformed object matches by name.
	byName := &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT"}}}
	checkMatch(t, objects[0].cal, byName, nil, true)
}

func TestMatchesOtherComponents(t *testing.T) {
	todo := "BEGIN:VTODO\r\nUID:t1\r\nDTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260310T090000Z\r\nDUE:20260310T100000Z\r\nEND:VTODO\r\n"
	withAlarm := vevent("DTSTART:20260310T090000Z", "DURATION:PT1H",
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:Soon\r\nTRIGGER:-PT15M\r\nEND:VALARM")
	plain := vevent("DTSTART:20260310T090000Z", "DURATION:PT1H")
	everything := &TimeRange{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)}
	cal := func(cs ...CompFilter) *CompFilter { return &CompFilter{Name: "VCALENDAR", Comps: cs} }
	alarm := func(a CompFilter) *CompFilter {
		a.Name = "VALARM"
		return cal(CompFilter{Name: "VEVENT", Comps: []CompFilter{a}})
	}
	tests := []struct {
		name  string
		block string
		f     *CompFilter
		want  bool
	}{
		{"VTODO by name", todo, cal(CompFilter{Name: "VTODO"}), true},
		{"VTODO by lower-case name", todo, cal(CompFilter{Name: "vtodo"}), true},
		{"VTODO with a time range matches nothing", todo, cal(CompFilter{Name: "VTODO", TimeRange: everything}), false},
		{"VTODO prop filter", todo, cal(CompFilter{Name: "VTODO", Props: []PropFilter{{Name: "UID", TextMatch: &TextMatch{Value: "t1", MatchType: MatchEquals}}}}), true},
		{"VEVENT absent", todo, cal(CompFilter{Name: "VEVENT"}), false},
		{"VEVENT is-not-defined", todo, cal(CompFilter{Name: "VEVENT", IsNotDefined: true}), true},
		{"VTODO is-not-defined", todo, cal(CompFilter{Name: "VTODO", IsNotDefined: true}), false},
		{"VCALENDAR alone", plain, cal(), true},
		{"VCALENDAR is-not-defined", plain, &CompFilter{Name: "VCALENDAR", IsNotDefined: true}, false},
		{"VCALENDAR with a time range matches nothing", plain, &CompFilter{Name: "VCALENDAR", TimeRange: everything}, false},
		{"VCALENDAR prop", plain, &CompFilter{Name: "VCALENDAR", Props: []PropFilter{{Name: "PRODID", TextMatch: &TextMatch{Value: "//x//"}}}}, true},
		{"VALARM nested", withAlarm, alarm(CompFilter{}), true},
		{"VALARM nested absent", plain, alarm(CompFilter{}), false},
		{"VALARM is-not-defined", plain, alarm(CompFilter{IsNotDefined: true}), true},
		{"VALARM is-not-defined present", withAlarm, alarm(CompFilter{IsNotDefined: true}), false},
		{"VALARM prop", withAlarm, alarm(CompFilter{Props: []PropFilter{{Name: "ACTION", TextMatch: &TextMatch{Value: "DISPLAY", MatchType: MatchEquals}}}}), true},
		{"VALARM prop miss", withAlarm, alarm(CompFilter{Props: []PropFilter{{Name: "ACTION", TextMatch: &TextMatch{Value: "AUDIO", MatchType: MatchEquals}}}}), false},
		{"VALARM with a time range matches nothing", withAlarm, alarm(CompFilter{TimeRange: everything}), false},
		{"VALARM at the top level", withAlarm, cal(CompFilter{Name: "VALARM"}), false},
		{"nested VEVENT with a time range matches nothing",
			vevent("DTSTART:20260310T090000Z", "BEGIN:VEVENT\r\nDTSTART:20260310T090000Z\r\nEND:VEVENT"),
			cal(CompFilter{Name: "VEVENT", Comps: []CompFilter{{Name: "VEVENT", TimeRange: everything}}}), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkMatch(t, mustCal(t, tt.block), tt.f, nil, tt.want)
		})
	}
}

func TestMatchesProperties(t *testing.T) {
	ev := vevent("DTSTART;VALUE=DATE:20260310", "SUMMARY:Team meeting",
		"ATTENDEE;PARTSTAT=ACCEPTED;CN=Ada:mailto:ada@example.org",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:bob@example.org",
		`ATTENDEE;MEMBER="mailto:a@example.org","mailto:b@example.org":mailto:c@example.org`,
		"CATEGORIES:Work")
	escaped := vevent("DTSTART:20260310T090000Z", `SUMMARY:Team\, meeting`, "LOCATION:Room 1")
	prop := func(pf PropFilter) *CompFilter {
		return &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT", Props: []PropFilter{pf}}}}
	}
	text := func(name string, tm TextMatch) *CompFilter { return prop(PropFilter{Name: name, TextMatch: &tm}) }
	param := func(name string, pf ParamFilter) *CompFilter {
		return prop(PropFilter{Name: name, Params: []ParamFilter{pf}})
	}
	within := func(name string, start, end time.Time) *CompFilter {
		return prop(PropFilter{Name: name, TimeRange: &TimeRange{Start: start, End: end}})
	}
	tests := []struct {
		name     string
		block    string
		f        *CompFilter
		floating string
		want     bool
	}{
		{"SUMMARY contains with default collation", ev, text("SUMMARY", TextMatch{Value: "MEETING"}), "", true},
		{"SUMMARY contains miss", ev, text("SUMMARY", TextMatch{Value: "lunch"}), "", false},
		{"SUMMARY octet is exact", ev, text("SUMMARY", TextMatch{Value: "MEETING", Collation: CollationOctet}), "", false},
		{"SUMMARY ascii-casemap", ev, text("SUMMARY", TextMatch{Value: "MEETING", Collation: CollationASCII}), "", true},
		{"SUMMARY collation name case", ev, text("SUMMARY", TextMatch{Value: "MEETING", Collation: "I;ASCII-CASEMAP"}), "", true},
		{"SUMMARY starts-with", ev, text("SUMMARY", TextMatch{Value: "team", MatchType: MatchStartsWith}), "", true},
		{"SUMMARY ends-with", ev, text("SUMMARY", TextMatch{Value: "ING", MatchType: MatchEndsWith}), "", true},
		{"SUMMARY ends-with miss", ev, text("SUMMARY", TextMatch{Value: "team", MatchType: MatchEndsWith}), "", false},
		{"UID equals", ev, text("UID", TextMatch{Value: "e1", MatchType: MatchEquals}), "", true},
		{"UID equals is not contains", ev, text("UID", TextMatch{Value: "e", MatchType: MatchEquals}), "", false},
		{"property name case", ev, text("summary", TextMatch{Value: "team"}), "", true},
		{"escaped comma", escaped, text("SUMMARY", TextMatch{Value: "Team, meeting", MatchType: MatchEquals}), "", true},
		{"escape not compared", escaped, text("SUMMARY", TextMatch{Value: `\,`}), "", false},
		{"ATTENDEE with PARTSTAT", ev, param("ATTENDEE", ParamFilter{Name: "PARTSTAT", TextMatch: &TextMatch{Value: "ACCEPTED", MatchType: MatchEquals}}), "", true},
		{"ATTENDEE PARTSTAT and value on one instance", ev, prop(PropFilter{Name: "ATTENDEE", TextMatch: &TextMatch{Value: "bob"},
			Params: []ParamFilter{{Name: "PARTSTAT", TextMatch: &TextMatch{Value: "ACCEPTED", MatchType: MatchEquals}}}}), "", false},
		{"ATTENDEE PARTSTAT lower-case name", ev, param("ATTENDEE", ParamFilter{Name: "partstat", TextMatch: &TextMatch{Value: "needs"}}), "", true},
		{"ATTENDEE PARTSTAT miss", ev, param("ATTENDEE", ParamFilter{Name: "PARTSTAT", TextMatch: &TextMatch{Value: "DECLINED"}}), "", false},
		{"param defined", ev, param("ATTENDEE", ParamFilter{Name: "CN"}), "", true},
		{"param absent", ev, param("SUMMARY", ParamFilter{Name: "LANGUAGE"}), "", false},
		{"param is-not-defined", ev, param("SUMMARY", ParamFilter{Name: "LANGUAGE", IsNotDefined: true}), "", true},
		{"param is-not-defined on some instance", ev, param("ATTENDEE", ParamFilter{Name: "CN", IsNotDefined: true}), "", true},
		{"param multi-valued first", ev, param("ATTENDEE", ParamFilter{Name: "MEMBER", TextMatch: &TextMatch{Value: "mailto:a@example.org", MatchType: MatchEquals}}), "", true},
		{"param multi-valued second", ev, param("ATTENDEE", ParamFilter{Name: "MEMBER", TextMatch: &TextMatch{Value: "mailto:b@example.org", MatchType: MatchEquals}}), "", true},
		{"param multi-valued miss", ev, param("ATTENDEE", ParamFilter{Name: "MEMBER", TextMatch: &TextMatch{Value: "mailto:c@example.org", MatchType: MatchEquals}}), "", false},
		{"LOCATION is-not-defined", ev, prop(PropFilter{Name: "LOCATION", IsNotDefined: true}), "", true},
		{"LOCATION is-not-defined but present", escaped, prop(PropFilter{Name: "LOCATION", IsNotDefined: true}), "", false},
		{"LOCATION defined", escaped, prop(PropFilter{Name: "LOCATION"}), "", true},
		{"negate", ev, text("SUMMARY", TextMatch{Value: "meeting", Negate: true}), "", false},
		{"negate miss", ev, text("SUMMARY", TextMatch{Value: "lunch", Negate: true}), "", true},
		{"negate per instance", ev, text("ATTENDEE", TextMatch{Value: "ada", Negate: true}), "", true},
		{"negate on every instance", ev, text("ATTENDEE", TextMatch{Value: "mailto:", Negate: true}), "", false},
		{"negate absent property", ev, text("LOCATION", TextMatch{Value: "x", Negate: true}), "", false},
		{"negate param", ev, param("ATTENDEE", ParamFilter{Name: "PARTSTAT", TextMatch: &TextMatch{Value: "ACCEPTED", Negate: true}}), "", true},
		{"DTSTAMP in range", ev, within("DTSTAMP", at(2026, 1, 1, 0, 0), at(2026, 1, 2, 0, 0)), "", true},
		{"DTSTAMP at the exclusive end", ev, within("DTSTAMP", at(2025, 12, 31, 0, 0), at(2026, 1, 1, 0, 0)), "", false},
		{"DTSTAMP open start", ev, within("DTSTAMP", time.Time{}, at(2026, 1, 1, 0, 1)), "", true},
		{"DTSTAMP open end", ev, within("DTSTAMP", at(2026, 1, 1, 0, 1), time.Time{}), "", false},
		{"DTSTART date at floating midnight", ev, within("DTSTART", at(2026, 3, 9, 23, 0), at(2026, 3, 9, 23, 1)), "Europe/Prague", true},
		{"DTSTART date as UTC", ev, within("DTSTART", at(2026, 3, 9, 23, 0), at(2026, 3, 9, 23, 1)), "", false},
		{"text property has no time", ev, within("SUMMARY", time.Time{}, time.Time{}), "", false},
		{"two prop filters both hold", ev, &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT", Props: []PropFilter{
			{Name: "SUMMARY", TextMatch: &TextMatch{Value: "team"}}, {Name: "CATEGORIES", TextMatch: &TextMatch{Value: "work"}}}}}}, "", true},
		{"two prop filters one fails", ev, &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT", Props: []PropFilter{
			{Name: "SUMMARY", TextMatch: &TextMatch{Value: "team"}}, {Name: "CATEGORIES", TextMatch: &TextMatch{Value: "home"}}}}}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var floating *time.Location
			if tt.floating != "" {
				floating = needZone(t, tt.floating)
			}
			checkMatch(t, mustCal(t, tt.block), tt.f, floating, tt.want)
		})
	}
}

// nestComps returns a VCALENDAR filter with depth comp-filters in a chain.
func nestComps(depth int) *CompFilter {
	f := CompFilter{Name: "VALARM"}
	for i := depth; i > 2; i-- {
		f = CompFilter{Name: "VALARM", Comps: []CompFilter{f}}
	}
	if depth == 1 {
		return &CompFilter{Name: "VCALENDAR"}
	}
	return &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{f}}
}

// wide returns a VCALENDAR filter with n nodes: itself and n-1 prop-filters.
func wide(n int) *CompFilter {
	f := &CompFilter{Name: "VCALENDAR"}
	for range n - 1 {
		f.Props = append(f.Props, PropFilter{Name: "X"})
	}
	return f
}

func TestFilterValidate(t *testing.T) {
	tr := &TimeRange{Start: at(2026, 1, 1, 0, 0), End: at(2026, 1, 2, 0, 0)}
	inverted := &TimeRange{Start: at(2026, 1, 2, 0, 0), End: at(2026, 1, 1, 0, 0)}
	root := func(cs ...CompFilter) *CompFilter { return &CompFilter{Name: "VCALENDAR", Comps: cs} }
	ev := func(pfs ...PropFilter) *CompFilter { return root(CompFilter{Name: "VEVENT", Props: pfs}) }
	tm := func(tm TextMatch) *CompFilter { return ev(PropFilter{Name: "SUMMARY", TextMatch: &tm}) }
	tests := []struct {
		name string
		f    *CompFilter
		ok   bool
	}{
		{"nil", nil, false},
		{"root only", root(), true},
		{"lower-case root", &CompFilter{Name: "vcalendar"}, true},
		{"wrong root", &CompFilter{Name: "VEVENT"}, false},
		{"empty root", &CompFilter{}, false},
		{"non-ASCII fold of root", &CompFilter{Name: "VCALENDAſ"}, false},
		{"empty comp name", root(CompFilter{}), false},
		{"empty prop name", ev(PropFilter{}), false},
		{"empty param name", ev(PropFilter{Name: "ATTENDEE", Params: []ParamFilter{{}}}), false},
		{"all collations", ev(
			PropFilter{Name: "A", TextMatch: &TextMatch{Collation: CollationOctet}},
			PropFilter{Name: "A", TextMatch: &TextMatch{Collation: CollationASCII}},
			PropFilter{Name: "A", TextMatch: &TextMatch{Collation: CollationUnicode}}), true},
		{"all match types", ev(
			PropFilter{Name: "A", TextMatch: &TextMatch{MatchType: MatchEquals}},
			PropFilter{Name: "A", TextMatch: &TextMatch{MatchType: MatchContains}},
			PropFilter{Name: "A", TextMatch: &TextMatch{MatchType: MatchStartsWith}},
			PropFilter{Name: "A", TextMatch: &TextMatch{MatchType: MatchEndsWith}}), true},
		{"bad collation", tm(TextMatch{Collation: "x"}), false},
		{"ascii-numeric", tm(TextMatch{Collation: "i;ascii-numeric"}), false},
		{"bad match type", tm(TextMatch{MatchType: "regex"}), false},
		{"bad param collation", ev(PropFilter{Name: "A", Params: []ParamFilter{{Name: "P", TextMatch: &TextMatch{Collation: "x"}}}}), false},
		{"comp is-not-defined alone", root(CompFilter{Name: "VTODO", IsNotDefined: true}), true},
		{"comp is-not-defined with time range", root(CompFilter{Name: "VEVENT", IsNotDefined: true, TimeRange: tr}), false},
		{"comp is-not-defined with props", root(CompFilter{Name: "VEVENT", IsNotDefined: true, Props: []PropFilter{{Name: "A"}}}), false},
		{"comp is-not-defined with comps", root(CompFilter{Name: "VEVENT", IsNotDefined: true, Comps: []CompFilter{{Name: "VALARM"}}}), false},
		{"prop is-not-defined alone", ev(PropFilter{Name: "A", IsNotDefined: true}), true},
		{"prop is-not-defined with text", ev(PropFilter{Name: "A", IsNotDefined: true, TextMatch: &TextMatch{}}), false},
		{"prop is-not-defined with time range", ev(PropFilter{Name: "A", IsNotDefined: true, TimeRange: tr}), false},
		{"prop is-not-defined with params", ev(PropFilter{Name: "A", IsNotDefined: true, Params: []ParamFilter{{Name: "P"}}}), false},
		{"param is-not-defined alone", ev(PropFilter{Name: "A", Params: []ParamFilter{{Name: "P", IsNotDefined: true}}}), true},
		{"param is-not-defined with text", ev(PropFilter{Name: "A", Params: []ParamFilter{{Name: "P", IsNotDefined: true, TextMatch: &TextMatch{}}}}), false},
		{"time range", root(CompFilter{Name: "VEVENT", TimeRange: tr}), true},
		{"empty time range", root(CompFilter{Name: "VEVENT", TimeRange: &TimeRange{Start: tr.Start, End: tr.Start}}), true},
		{"inverted time range", root(CompFilter{Name: "VEVENT", TimeRange: inverted}), false},
		{"inverted prop time range", ev(PropFilter{Name: "DTSTAMP", TimeRange: inverted}), false},
		{"depth 16", nestComps(16), true},
		{"depth 17", nestComps(17), false},
		{"1000 nodes", wide(1000), true},
		{"1001 nodes", wide(1001), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.f.Validate()
			if tt.ok {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrFilter) {
				t.Fatalf("Validate = %v, want ErrFilter", err)
			}
			// Matches validates first.
			got, merr := mustCal(t, vevent("DTSTART:20260310T090000Z")).Matches(tt.f, nil)
			if got || !errors.Is(merr, ErrFilter) {
				t.Fatalf("Matches = %v, %v; want false, ErrFilter", got, merr)
			}
		})
	}
}

func TestFilterValidateCountsParams(t *testing.T) {
	f := &CompFilter{Name: "VCALENDAR", Props: []PropFilter{{Name: "X"}}}
	for range 998 {
		f.Props[0].Params = append(f.Props[0].Params, ParamFilter{Name: "P"})
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("1000 nodes: %v", err)
	}
	f.Props[0].Params = append(f.Props[0].Params, ParamFilter{Name: "P"})
	if err := f.Validate(); !errors.Is(err, ErrFilter) {
		t.Fatalf("1001 nodes: %v", err)
	}
}

func TestFilterErrorsClipNames(t *testing.T) {
	hostile := strings.Repeat("ä", 5000)
	root := func(cs ...CompFilter) *CompFilter { return &CompFilter{Name: "VCALENDAR", Comps: cs} }
	for _, f := range []*CompFilter{
		{Name: hostile},
		root(CompFilter{Name: hostile, IsNotDefined: true, TimeRange: &TimeRange{}}),
		root(CompFilter{Name: "VEVENT", Props: []PropFilter{{Name: hostile, IsNotDefined: true, TextMatch: &TextMatch{}}}}),
		root(CompFilter{Name: "VEVENT", Props: []PropFilter{{Name: "A", Params: []ParamFilter{{Name: hostile, IsNotDefined: true, TextMatch: &TextMatch{}}}}}}),
		root(CompFilter{Name: "VEVENT", Props: []PropFilter{{Name: "A", TextMatch: &TextMatch{Collation: hostile}}}}),
		root(CompFilter{Name: "VEVENT", Props: []PropFilter{{Name: "A", TextMatch: &TextMatch{MatchType: hostile}}}}),
	} {
		err := f.Validate()
		if !errors.Is(err, ErrFilter) {
			t.Fatalf("Validate = %v, want ErrFilter", err)
		}
		if len(err.Error()) > 200 {
			t.Fatalf("error not clipped: %d bytes", len(err.Error()))
		}
	}
}

func TestMatchesNilCalendar(t *testing.T) {
	var c *Calendar
	got, err := c.Matches(&CompFilter{Name: "VCALENDAR"}, nil)
	if got || err != nil {
		t.Fatalf("Matches = %v, %v; want false, nil", got, err)
	}
}

func TestMatchesConcurrent(t *testing.T) {
	c := pragueCal(t, vevent("DTSTART;"+prague+":20200101T090000", "DTEND;"+prague+":20200101T100000",
		"RRULE:FREQ=DAILY", "SUMMARY:Daily", "EXDATE;"+prague+":20260302T090000"))
	hit := eventsIn(at(2026, 3, 3, 8, 0), at(2026, 3, 3, 9, 0))
	hit.Comps[0].Props = []PropFilter{{Name: "SUMMARY", TextMatch: &TextMatch{Value: "daily"}}}
	miss := eventsIn(at(2026, 3, 2, 8, 0), at(2026, 3, 2, 9, 0))
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			f, want := hit, true
			if i%2 == 1 {
				f, want = miss, false
			}
			if got, err := c.Matches(f, nil); got != want || err != nil {
				t.Errorf("Matches = %v, %v; want %v", got, err, want)
			}
		})
	}
	wg.Wait()
}

// timeRanges returns a VCALENDAR filter with n VEVENT comp-filters, each with
// its own time range.
func timeRanges(n int) *CompFilter {
	f := &CompFilter{Name: "VCALENDAR"}
	for i := range n {
		start := at(2026, 1, 1, 0, 0).Add(time.Duration(i) * time.Hour)
		f.Comps = append(f.Comps, CompFilter{Name: "VEVENT", TimeRange: &TimeRange{Start: start}})
	}
	return f
}

func TestFilterValidateTimeRangeLimit(t *testing.T) {
	if err := timeRanges(16).Validate(); err != nil {
		t.Fatalf("16 time ranges: %v", err)
	}
	err := timeRanges(17).Validate()
	if !errors.Is(err, ErrFilter) {
		t.Fatalf("17 time ranges: %v, want ErrFilter", err)
	}
	// Property time ranges do not count.
	f := timeRanges(16)
	f.Comps[0].Props = []PropFilter{{Name: "DTSTAMP", TimeRange: &TimeRange{}}}
	if err := f.Validate(); err != nil {
		t.Fatalf("16 time ranges and a property one: %v", err)
	}
}

// TestMatchesNonASCIIPropertyName checks that a property name is folded on
// ASCII letters only: DTſTART (U+017F) is not DTSTART.
func TestMatchesNonASCIIPropertyName(t *testing.T) {
	const name = "DTſTART"
	c := &Calendar{Component: Component{Name: "VCALENDAR", Children: []*Component{{
		Name:  "VEVENT",
		Props: []Property{{Name: name, Value: "20260310T090000Z"}},
	}}}}
	f := &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT", Props: []PropFilter{
		{Name: name, TimeRange: &TimeRange{Start: at(2026, 3, 10, 0, 0), End: at(2026, 3, 11, 0, 0)}},
	}}}}
	checkMatch(t, c, f, nil, false)
}

// TestMatchesTimeRangeIsObjectWide pins the documented behaviour: the time
// range holds for the object as a whole, so a prop-filter met only by an
// override still matches a range that only the master occurs in.
func TestMatchesTimeRangeIsObjectWide(t *testing.T) {
	c := pragueCal(t,
		vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
			"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3", "SUMMARY:Weekly"),
		vevent("RECURRENCE-ID;"+prague+":20260323T090000", "DTSTART;"+prague+":20260324T140000",
			"DTEND;"+prague+":20260324T150000", "SUMMARY:Moved"))
	f := eventsIn(at(2026, 3, 16, 8, 0), at(2026, 3, 16, 9, 0))
	f.Comps[0].Props = []PropFilter{{Name: "SUMMARY", TextMatch: &TextMatch{Value: "moved"}}}
	checkMatch(t, c, f, nil, true)
}

// textFilters returns a filter with n text matches on DESCRIPTION.
func textFilters(n int, value, col string, negate bool) *CompFilter {
	props := make([]PropFilter, n)
	for i := range props {
		props[i] = PropFilter{Name: "DESCRIPTION", TextMatch: &TextMatch{Value: value, Collation: col, Negate: negate}}
	}
	return &CompFilter{Name: "VCALENDAR", Comps: []CompFilter{{Name: "VEVENT", Props: props}}}
}

// TestMatchesFoldsEachValueOnce checks that many text matches against one
// property fold its value once per collation, not once per match.
func TestMatchesFoldsEachValueOnce(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20260101T100000Z", "DESCRIPTION:"+strings.Repeat("Lorem ipsum\\, dolor ", 1000)))
	for _, col := range []string{CollationUnicode, CollationASCII} {
		before := textFolds.Load()
		checkMatch(t, cal, textFilters(50, "LOREM IPSUM, DOLOR", col, false), nil, true)
		if n := textFolds.Load() - before; n != 1 {
			t.Errorf("%s: %d folds of the value, want 1", col, n)
		}
	}
}

// TestMatchesFoldBudget checks that text matches stop matching once a call
// has folded more than maxFoldBytes, negated ones included.
func TestMatchesFoldBudget(t *testing.T) {
	old := maxFoldBytes
	maxFoldBytes = 1000
	t.Cleanup(func() { maxFoldBytes = old })
	cal := mustCal(t, vevent("DTSTART:20260101T100000Z", "DESCRIPTION:"+strings.Repeat("x", 600),
		"SUMMARY:"+strings.Repeat("y", 600), "LOCATION:here"))
	f := textFilters(1, "x", "", false)
	f.Comps[0].Props = append(f.Comps[0].Props, PropFilter{Name: "SUMMARY", TextMatch: &TextMatch{Value: "y"}})
	checkMatch(t, cal, f, nil, false)
	f.Comps[0].Props[1] = PropFilter{Name: "SUMMARY", TextMatch: &TextMatch{Value: "z", Negate: true}}
	checkMatch(t, cal, f, nil, false)
	// A fresh call has a fresh budget.
	checkMatch(t, cal, textFilters(1, "x", "", false), nil, true)
	f.Comps[0].Props[1] = PropFilter{Name: "LOCATION", TextMatch: &TextMatch{Value: "HERE"}}
	checkMatch(t, cal, f, nil, true)
}
