// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teambition/rrule-go"
)

// vevent wraps content lines in a VEVENT with a UID and DTSTAMP.
func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20260101T000000Z\r\n" +
		strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func at(y int, m time.Month, d, h, mi int) time.Time { return utc(y, m, d, h, mi) }

// occ builds an expected occurrence of a recurring master, whose
// RecurrenceID is its own start.
func occ(start, end time.Time) Occurrence {
	return Occurrence{Start: start, End: end, RecurrenceID: start}
}

func fmtOccs(os []Occurrence) string {
	var b strings.Builder
	for _, o := range os {
		fmt.Fprintf(&b, "\n  %s - %s rid=%s override=%v", o.Start.UTC().Format(time.RFC3339),
			o.End.UTC().Format(time.RFC3339), o.RecurrenceID.UTC().Format(time.RFC3339), o.Override)
	}
	return b.String()
}

func sameOccs(a, b []Occurrence) bool {
	return slices.EqualFunc(a, b, func(x, y Occurrence) bool {
		return x.Start.Equal(y.Start) && x.End.Equal(y.End) &&
			x.RecurrenceID.Equal(y.RecurrenceID) && x.Override == y.Override
	})
}

const prague = "TZID=Europe/Prague"

func TestOccurrences(t *testing.T) {
	weekly := []string{
		"DTSTART;" + prague + ":20260316T090000",
		"DTEND;" + prague + ":20260316T100000",
		"RRULE:FREQ=WEEKLY;BYDAY=MO",
	}
	march := Range{Start: at(2026, 3, 16, 0, 0), End: at(2026, 4, 7, 0, 0)}
	mon := func(d, h int) Occurrence {
		return occ(at(2026, time.Month(3+d/100), d%100, h, 0), at(2026, time.Month(3+d/100), d%100, h+1, 0))
	}
	tests := []struct {
		name     string
		events   []string
		floating *time.Location
		win      Range
		want     []Occurrence
	}{
		{
			name:   "weekly across the spring DST change",
			events: []string{vevent(weekly...)},
			win:    march,
			want:   []Occurrence{mon(16, 8), mon(23, 8), mon(30, 7), mon(106, 7)},
		},
		{
			name:   "EXDATE removes one",
			events: []string{vevent(append(weekly, "EXDATE;"+prague+":20260323T090000")...)},
			win:    march,
			want:   []Occurrence{mon(16, 8), mon(30, 7), mon(106, 7)},
		},
		{
			name:   "EXDATE list in UTC",
			events: []string{vevent(append(weekly, "EXDATE:20260323T080000Z,20260330T070000Z")...)},
			win:    march,
			want:   []Occurrence{mon(16, 8), mon(106, 7)},
		},
		{
			name: "COUNT",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3")},
			win:  Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{mon(16, 8), mon(23, 8), mon(30, 7)},
		},
		{
			name: "UNTIL in UTC after the third",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;UNTIL=20260331T000000Z")},
			win:  Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{mon(16, 8), mon(23, 8), mon(30, 7)},
		},
		{
			name: "UNTIL equal to the last instant is inclusive",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;UNTIL=20260330T070000Z")},
			win:  Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{mon(16, 8), mon(23, 8), mon(30, 7)},
		},
		{
			name: "DATE UNTIL covers its whole day",
			events: []string{vevent("DTSTART:20260316T090000Z", "DTEND:20260316T100000Z",
				"RRULE:FREQ=DAILY;UNTIL=20260318")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 3, 16, 9, 0), at(2026, 3, 16, 10, 0)),
				occ(at(2026, 3, 17, 9, 0), at(2026, 3, 17, 10, 0)),
				occ(at(2026, 3, 18, 9, 0), at(2026, 3, 18, 10, 0)),
			},
		},
		{
			name: "monthly BYMONTHDAY=31 skips short months",
			events: []string{vevent("DTSTART;"+prague+":20260131T090000", "DURATION:PT1H",
				"RRULE:FREQ=MONTHLY;BYMONTHDAY=31")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2026, 8, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 1, 31, 8, 0), at(2026, 1, 31, 9, 0)),
				occ(at(2026, 3, 31, 7, 0), at(2026, 3, 31, 8, 0)),
				occ(at(2026, 5, 31, 7, 0), at(2026, 5, 31, 8, 0)),
				occ(at(2026, 7, 31, 7, 0), at(2026, 7, 31, 8, 0)),
			},
		},
		{
			name: "RDATE with TZID adds a date",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=2", "RDATE;"+prague+":20260401T150000")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{mon(16, 8), mon(23, 8),
				occ(at(2026, 4, 1, 13, 0), at(2026, 4, 1, 14, 0))},
		},
		{
			name: "RDATE without RRULE, PERIOD and duplicates",
			events: []string{vevent("DTSTART:20260316T090000Z", "DTEND:20260316T100000Z",
				"RDATE:20260317T090000Z,20260316T090000Z",
				"RDATE;VALUE=PERIOD:20260318T120000Z/PT5H")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 3, 16, 9, 0), at(2026, 3, 16, 10, 0)),
				occ(at(2026, 3, 17, 9, 0), at(2026, 3, 17, 10, 0)),
				occ(at(2026, 3, 18, 12, 0), at(2026, 3, 18, 13, 0)),
			},
		},
		{
			name: "override moves one occurrence and changes its duration",
			events: []string{vevent(weekly...), vevent("RECURRENCE-ID;"+prague+":20260323T090000",
				"DTSTART;"+prague+":20260324T140000", "DTEND;"+prague+":20260324T170000")},
			win: march,
			want: []Occurrence{mon(16, 8),
				{Start: at(2026, 3, 24, 13, 0), End: at(2026, 3, 24, 16, 0),
					RecurrenceID: at(2026, 3, 23, 8, 0), Override: true},
				mon(30, 7), mon(106, 7)},
		},
		{
			name: "override in UTC matches a zoned occurrence",
			events: []string{vevent(weekly...), vevent("RECURRENCE-ID:20260330T070000Z",
				"DTSTART:20260330T120000Z", "DURATION:PT30M")},
			win: march,
			want: []Occurrence{mon(16, 8), mon(23, 8),
				{Start: at(2026, 3, 30, 12, 0), End: at(2026, 3, 30, 12, 30),
					RecurrenceID: at(2026, 3, 30, 7, 0), Override: true},
				mon(106, 7)},
		},
		{
			name: "override that is not in the rule",
			events: []string{vevent(weekly...), vevent("RECURRENCE-ID;"+prague+":20260325T090000",
				"DTSTART;"+prague+":20260325T120000", "DTEND;"+prague+":20260325T130000")},
			win: march,
			want: []Occurrence{mon(16, 8), mon(23, 8),
				{Start: at(2026, 3, 25, 11, 0), End: at(2026, 3, 25, 12, 0),
					RecurrenceID: at(2026, 3, 25, 8, 0), Override: true},
				mon(30, 7), mon(106, 7)},
		},
		{
			name: "override moved out of the window removes the slot",
			events: []string{vevent(weekly...), vevent("RECURRENCE-ID;"+prague+":20260323T090000",
				"DTSTART;"+prague+":20260501T090000", "DTEND;"+prague+":20260501T100000")},
			win:  march,
			want: []Occurrence{mon(16, 8), mon(30, 7), mon(106, 7)},
		},
		{
			name: "a second override of the same instance is ignored",
			events: []string{vevent(weekly...),
				vevent("RECURRENCE-ID;"+prague+":20260323T090000",
					"DTSTART;"+prague+":20260324T140000", "DTEND;"+prague+":20260324T150000"),
				vevent("RECURRENCE-ID:20260323T080000Z",
					"DTSTART;"+prague+":20260325T140000", "DTEND;"+prague+":20260325T150000")},
			win: march,
			want: []Occurrence{mon(16, 8),
				{Start: at(2026, 3, 24, 13, 0), End: at(2026, 3, 24, 14, 0),
					RecurrenceID: at(2026, 3, 23, 8, 0), Override: true},
				mon(30, 7), mon(106, 7)},
		},
		{
			name: "override without a master",
			events: []string{vevent("RECURRENCE-ID;"+prague+":20260323T090000",
				"DTSTART;"+prague+":20260324T090000", "DTEND;"+prague+":20260324T100000")},
			win: march,
			want: []Occurrence{{Start: at(2026, 3, 24, 8, 0), End: at(2026, 3, 24, 9, 0),
				RecurrenceID: at(2026, 3, 23, 8, 0), Override: true}},
		},
		{
			name: "all-day weekly of three days has exclusive date ends",
			events: []string{vevent("DTSTART;VALUE=DATE:20260302", "DTEND;VALUE=DATE:20260305",
				"RRULE:FREQ=WEEKLY;COUNT=3")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 3, 2, 0, 0), at(2026, 3, 5, 0, 0)),
				occ(at(2026, 3, 9, 0, 0), at(2026, 3, 12, 0, 0)),
				occ(at(2026, 3, 16, 0, 0), at(2026, 3, 19, 0, 0)),
			},
		},
		{
			name:   "all-day DTEND equal to DTSTART lasts one day",
			events: []string{vevent("DTSTART;VALUE=DATE:20260302", "DTEND;VALUE=DATE:20260302")},
			win:    Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want:   []Occurrence{{Start: at(2026, 3, 2, 0, 0), End: at(2026, 3, 3, 0, 0)}},
		},
		{
			name:   "all-day DTEND before DTSTART lasts one day",
			events: []string{vevent("DTSTART;VALUE=DATE:20260310", "DTEND;VALUE=DATE:20260301")},
			win:    Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want:   []Occurrence{{Start: at(2026, 3, 10, 0, 0), End: at(2026, 3, 11, 0, 0)}},
		},
		{
			name: "all-day EXDATE as DATE",
			events: []string{vevent("DTSTART;VALUE=DATE:20260302", "DTEND;VALUE=DATE:20260305",
				"RRULE:FREQ=WEEKLY;COUNT=3", "EXDATE;VALUE=DATE:20260309")},
			win: Range{Start: at(2026, 1, 1, 0, 0), End: at(2027, 1, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 3, 2, 0, 0), at(2026, 3, 5, 0, 0)),
				occ(at(2026, 3, 16, 0, 0), at(2026, 3, 19, 0, 0)),
			},
		},
		{
			name:     "all-day across DST ends on the date, not after 4x24h",
			events:   []string{vevent("DTSTART;VALUE=DATE:20260326", "DTEND;VALUE=DATE:20260330")},
			floating: needZoneOrUTC("Europe/Prague"),
			win:      Range{Start: at(2026, 3, 1, 0, 0), End: at(2026, 5, 1, 0, 0)},
			want:     []Occurrence{{Start: at(2026, 3, 25, 23, 0), End: at(2026, 3, 29, 22, 0)}},
		},
		{
			name:   "all-day without DTEND lasts one day",
			events: []string{vevent("DTSTART;VALUE=DATE:20260326")},
			win:    Range{Start: at(2026, 3, 26, 23, 59), End: at(2026, 3, 27, 0, 0)},
			want:   []Occurrence{{Start: at(2026, 3, 26, 0, 0), End: at(2026, 3, 27, 0, 0)}},
		},
		{
			name:   "DURATION event",
			events: []string{vevent("DTSTART:20260310T090000Z", "DURATION:PT1H30M")},
			win:    Range{Start: at(2026, 3, 10, 10, 29), End: at(2026, 3, 11, 0, 0)},
			want:   []Occurrence{{Start: at(2026, 3, 10, 9, 0), End: at(2026, 3, 10, 10, 30)}},
		},
		{
			name:   "event ending at the window start does not overlap",
			events: []string{vevent("DTSTART:20260310T090000Z", "DURATION:PT1H30M")},
			win:    Range{Start: at(2026, 3, 10, 10, 30), End: at(2026, 3, 11, 0, 0)},
		},
		{
			name:   "event starting at the window end does not overlap",
			events: []string{vevent("DTSTART:20260310T090000Z", "DURATION:PT1H30M")},
			win:    Range{Start: at(2026, 3, 10, 0, 0), End: at(2026, 3, 10, 9, 0)},
		},
		{
			name:   "instant event at the window start",
			events: []string{vevent("DTSTART:20260310T090000Z")},
			win:    Range{Start: at(2026, 3, 10, 9, 0), End: at(2026, 3, 10, 10, 0)},
			want:   []Occurrence{{Start: at(2026, 3, 10, 9, 0), End: at(2026, 3, 10, 9, 0)}},
		},
		{
			name:   "instant event at the window end",
			events: []string{vevent("DTSTART:20260310T090000Z")},
			win:    Range{Start: at(2026, 3, 10, 8, 0), End: at(2026, 3, 10, 9, 0)},
		},
		{
			name:   "single event, open window",
			events: []string{vevent("DTSTART:20260310T090000Z", "DTEND:20260310T100000Z")},
			want:   []Occurrence{{Start: at(2026, 3, 10, 9, 0), End: at(2026, 3, 10, 10, 0)}},
		},
		{
			name:     "floating time uses the floating location",
			events:   []string{vevent("DTSTART:20260710T090000", "DTEND:20260710T100000")},
			floating: needZoneOrUTC("Europe/Prague"),
			want:     []Occurrence{{Start: at(2026, 7, 10, 7, 0), End: at(2026, 7, 10, 8, 0)}},
		},
		{
			name:   "window open at the start",
			events: []string{vevent(weekly...)},
			win:    Range{End: at(2026, 3, 24, 0, 0)},
			want:   []Occurrence{mon(16, 8), mon(23, 8)},
		},
		{
			name: "window open at the end",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3")},
			win:  Range{Start: at(2026, 3, 20, 0, 0)},
			want: []Occurrence{mon(23, 8), mon(30, 7)},
		},
		{
			name: "window open on both sides",
			events: []string{vevent("DTSTART;"+prague+":20260316T090000", "DTEND;"+prague+":20260316T100000",
				"RRULE:FREQ=WEEKLY;BYDAY=MO;COUNT=3")},
			want: []Occurrence{mon(16, 8), mon(23, 8), mon(30, 7)},
		},
		{
			name: "infinite rule, one-week window",
			events: []string{vevent("DTSTART;"+prague+":20260101T090000", "DTEND;"+prague+":20260101T100000",
				"RRULE:FREQ=DAILY")},
			win: Range{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 4, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 6, 1, 7, 0), at(2026, 6, 1, 8, 0)),
				occ(at(2026, 6, 2, 7, 0), at(2026, 6, 2, 8, 0)),
				occ(at(2026, 6, 3, 7, 0), at(2026, 6, 3, 8, 0)),
			},
		},
		{
			name: "daily rule from 1900 queried in 2026",
			events: []string{vevent("DTSTART;"+prague+":19000101T090000", "DTEND;"+prague+":19000101T100000",
				"RRULE:FREQ=DAILY")},
			win: Range{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 3, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 6, 1, 7, 0), at(2026, 6, 1, 8, 0)),
				occ(at(2026, 6, 2, 7, 0), at(2026, 6, 2, 8, 0)),
			},
		},
		{
			name:   "minutely rule from 1900 queried in 2026",
			events: []string{vevent("DTSTART;"+prague+":19000101T090000", "RRULE:FREQ=MINUTELY;INTERVAL=7")},
			win:    Range{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 1, 0, 15)},
			want: []Occurrence{
				// The rule steps in wall-clock time: 02:00 in Prague is on it.
				occ(at(2026, 6, 1, 0, 0), at(2026, 6, 1, 0, 0)),
				occ(at(2026, 6, 1, 0, 7), at(2026, 6, 1, 0, 7)),
				occ(at(2026, 6, 1, 0, 14), at(2026, 6, 1, 0, 14)),
			},
		},
		{
			name: "UNTIL in UTC on the autumn DST day, before the last slot",
			events: []string{vevent("DTSTART;"+prague+":20261020T090000", "DTEND;"+prague+":20261020T100000",
				"RRULE:FREQ=DAILY;UNTIL=20261025T070000Z")},
			win: Range{Start: at(2026, 10, 23, 0, 0), End: at(2026, 11, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 10, 23, 7, 0), at(2026, 10, 23, 8, 0)),
				occ(at(2026, 10, 24, 7, 0), at(2026, 10, 24, 8, 0)),
			},
		},
		{
			name: "UNTIL in UTC on the autumn DST day, at the last slot",
			events: []string{vevent("DTSTART;"+prague+":20261020T090000", "DTEND;"+prague+":20261020T100000",
				"RRULE:FREQ=DAILY;UNTIL=20261025T080000Z")},
			win: Range{Start: at(2026, 10, 23, 0, 0), End: at(2026, 11, 1, 0, 0)},
			want: []Occurrence{
				occ(at(2026, 10, 23, 7, 0), at(2026, 10, 23, 8, 0)),
				occ(at(2026, 10, 24, 7, 0), at(2026, 10, 24, 8, 0)),
				occ(at(2026, 10, 25, 8, 0), at(2026, 10, 25, 9, 0)),
			},
		},
		{
			name: "rule that never matches leaves DTSTART",
			events: []string{vevent("DTSTART:20260210T090000Z",
				"RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30")},
			want: []Occurrence{occ(at(2026, 2, 10, 9, 0), at(2026, 2, 10, 9, 0))},
		},
		{
			name: "hourly rule whose BYHOUR is never reached",
			events: []string{vevent("DTSTART:20260210T090000Z",
				"RRULE:FREQ=HOURLY;INTERVAL=24;BYHOUR=5")},
			want: []Occurrence{occ(at(2026, 2, 10, 9, 0), at(2026, 2, 10, 9, 0))},
		},
		{
			name:   "other components are ignored",
			events: []string{strings.ReplaceAll(vevent("DTSTART:20260310T090000Z", "RRULE:FREQ=DAILY"), "VEVENT", "VTODO")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := mustCal(t, append([]string{pragueTZ}, tt.events...)...)
			got, err := cal.Occurrences(NewResolver(cal), tt.floating, tt.win)
			if err != nil {
				t.Fatal(err)
			}
			if !sameOccs(got, tt.want) {
				t.Fatalf("got%s\nwant%s", fmtOccs(got), fmtOccs(tt.want))
			}
		})
	}
}

// needZoneOrUTC loads a zone at table construction time, falling back to
// UTC; tests that depend on it check the result.
func needZoneOrUTC(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func TestOccurrencesNilResolver(t *testing.T) {
	cal := mustCal(t, pragueTZ, vevent("DTSTART;"+prague+":20260710T090000", "DTEND;"+prague+":20260710T100000"))
	got, err := cal.Occurrences(nil, nil, Range{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Occurrence{{Start: at(2026, 7, 10, 7, 0), End: at(2026, 7, 10, 8, 0)}}
	if !sameOccs(got, want) {
		t.Fatalf("got%s\nwant%s", fmtOccs(got), fmtOccs(want))
	}
}

func TestOccurrencesErrors(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{"unknown FREQ", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=FOO"}},
		{"lookalike FREQ", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=DA\u0131LY"}},
		{"no FREQ", []string{"DTSTART:20260310T090000Z", "RRULE:COUNT=3"}},
		{"BYMONTH out of range", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=YEARLY;BYMONTH=13"}},
		{"unknown part", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=DAILY;FOO=1"}},
		{"BYEASTER is not iCalendar", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=YEARLY;BYEASTER=0"}},
		{"DTSTART in the rule", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=DAILY;DTSTART=20200101T000000Z"}},
		{"bad UNTIL", []string{"DTSTART:20260310T090000Z", "RRULE:FREQ=DAILY;UNTIL=2026"}},
		{"long list", []string{"DTSTART:20260310T090000Z",
			"RRULE:FREQ=YEARLY;BYSETPOS=" + strings.Repeat("1,", 100) + "1"}},
		{"too many instances a period", []string{"DTSTART:20260310T090000Z",
			"RRULE:FREQ=YEARLY;BYHOUR=" + intList(0, 23) + ";BYMINUTE=" + intList(0, 59)}},
		{"missing DTSTART", []string{"DTEND:20260310T090000Z"}},
		{"bad DTSTART", []string{"DTSTART:2026031"}},
		{"bad DTEND", []string{"DTSTART:20260310T090000Z", "DTEND:x"}},
		{"bad DURATION", []string{"DTSTART:20260310T090000Z", "DURATION:1H"}},
		{"bad EXDATE", []string{"DTSTART:20260310T090000Z", "EXDATE:x"}},
		{"bad RDATE", []string{"DTSTART:20260310T090000Z", "RDATE:x"}},
		{"too many RDATEs", []string{"DTSTART:20260310T090000Z", "RDATE:" + utcList(1001)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := mustCal(t, vevent(tt.lines...))
			got, err := cal.Occurrences(nil, nil, Range{})
			if !errors.Is(err, ErrRecurrence) {
				t.Fatalf("err = %v, want ErrRecurrence (got %d occurrences)", err, len(got))
			}
		})
	}
	t.Run("bad RECURRENCE-ID", func(t *testing.T) {
		cal := mustCal(t, vevent("DTSTART:20260310T090000Z"), vevent("RECURRENCE-ID:x", "DTSTART:20260310T090000Z"))
		if _, err := cal.Occurrences(nil, nil, Range{}); !errors.Is(err, ErrRecurrence) {
			t.Fatalf("err = %v, want ErrRecurrence", err)
		}
	})
}

func intList(from, to int) string {
	var s []string
	for i := from; i <= to; i++ {
		s = append(s, fmt.Sprint(i))
	}
	return strings.Join(s, ",")
}

// utcList returns n distinct UTC date-times one hour apart, comma-separated.
func utcList(n int) string {
	s := make([]string, n)
	for i := range s {
		s[i] = at(2026, 1, 1, 0, 0).Add(time.Duration(i) * time.Hour).Format("20060102T150405Z")
	}
	return strings.Join(s, ",")
}

func TestOccurrencesManyExdates(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20260101T000000Z", "RRULE:FREQ=HOURLY;COUNT=5001", "EXDATE:"+utcList(5000)))
	got, err := cal.Occurrences(nil, nil, Range{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Occurrence{occ(at(2026, 1, 1, 0, 0).Add(5000*time.Hour), at(2026, 1, 1, 0, 0).Add(5000*time.Hour))}
	if !sameOccs(got, want) {
		t.Fatalf("got%s\nwant%s", fmtOccs(got), fmtOccs(want))
	}
}

func TestOccurrencesClampsWindow(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20000101T000000Z", "RRULE:FREQ=YEARLY"))
	got, err := cal.Occurrences(nil, nil, Range{Start: at(2000, 1, 1, 0, 0), End: at(2200, 1, 1, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || !got[99].Start.Equal(at(2099, 1, 1, 0, 0)) {
		t.Fatalf("got %d occurrences, last %v; want 100 up to 2099", len(got), got[len(got)-1].Start)
	}
}

func TestOccurrencesCap(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20260601T000000Z", "RRULE:FREQ=SECONDLY"))
	var got []Occurrence
	var err error
	steps := stepsOf(func() {
		got, err = cal.Occurrences(nil, nil, Range{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 8, 0, 0)})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The rule stops at the cap instead of running through the week.
	if steps > maxOccurrences+10 {
		t.Errorf("%d rule instances, want about %d", steps, maxOccurrences)
	}
	if len(got) > maxOccurrences || len(got) < maxOccurrences-1 {
		t.Fatalf("got %d occurrences, want about %d", len(got), maxOccurrences)
	}
	if !slices.IsSortedFunc(got, func(a, b Occurrence) int { return a.Start.Compare(b.Start) }) {
		t.Fatal("occurrences are not sorted")
	}
}

// TestOccurrencesTerminate checks that rules with few or no instances, and
// open windows, finish. The rule library walks every period up to its last
// year looking for an instance, and those walks are not counted anywhere;
// TestCycleYears checks the move that keeps them short.
func TestOccurrencesTerminate(t *testing.T) {
	rules := []string{
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=MONTHLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=WEEKLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=DAILY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=HOURLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=MINUTELY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=SECONDLY;BYMONTH=2;BYMONTHDAY=30",
		"FREQ=SECONDLY;BYMONTH=2;BYMONTHDAY=30;BYSETPOS=" + intList(1, 60),
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYDAY=MO;BYSETPOS=" + intList(1, 60),
		"FREQ=MINUTELY;INTERVAL=1440;BYHOUR=3",
		"FREQ=SECONDLY;INTERVAL=86400;BYSECOND=1",
		"FREQ=DAILY;INTERVAL=2147483647",
		"FREQ=SECONDLY;INTERVAL=9223372036854775807",
	}
	wins := []Range{{}, {Start: at(2026, 6, 1, 0, 0)}, {End: at(9000, 1, 1, 0, 0)},
		{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 8, 0, 0)}, {Start: at(9990, 1, 1, 0, 0)}}
	for _, r := range rules {
		cal := mustCal(t, vevent("DTSTART:20260210T090000Z", "RRULE:"+r))
		for _, w := range wins {
			if _, err := cal.Occurrences(nil, nil, w); err != nil {
				t.Fatalf("%s: %v", r, err)
			}
		}
	}
}

// TestOccurrencesShiftMatchesFullExpansion compares rules that start long
// before the window with a plain expansion from DTSTART.
func TestOccurrencesShiftMatchesFullExpansion(t *testing.T) {
	rules := []string{
		"FREQ=YEARLY;BYWEEKNO=10;BYDAY=MO",
		"FREQ=YEARLY;INTERVAL=3;BYMONTH=2,3;BYMONTHDAY=29",
		"FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3",
		"FREQ=YEARLY;BYYEARDAY=-1,60;BYSETPOS=1",
		"FREQ=MONTHLY;INTERVAL=3;BYSETPOS=-1;BYDAY=MO,TU,WE,TH,FR",
		"FREQ=MONTHLY;BYMONTHDAY=31",
		"FREQ=MONTHLY;INTERVAL=131",
		"FREQ=WEEKLY;INTERVAL=3;BYDAY=TU,TH;WKST=SU",
		"FREQ=WEEKLY;INTERVAL=2;BYSETPOS=2;BYDAY=MO,WE,FR",
		"FREQ=WEEKLY;INTERVAL=5",
		"FREQ=DAILY;INTERVAL=7",
		"FREQ=DAILY;BYHOUR=1,13;BYMINUTE=5",
		"FREQ=HOURLY;INTERVAL=7",
		"FREQ=HOURLY;INTERVAL=5;BYHOUR=3,4",
		"FREQ=MINUTELY;INTERVAL=97;BYHOUR=9",
		"FREQ=SECONDLY;INTERVAL=7919;BYMINUTE=0,1,2,3",
	}
	dtstart := time.Date(1993, 5, 19, 9, 13, 27, 0, time.UTC) // a Wednesday
	wins := []Range{
		{Start: at(2026, 2, 10, 0, 0), End: at(2026, 4, 1, 0, 0)},
		// A Friday in a week of the biweekly rule: the week the expansion
		// starts in must be whole for BYSETPOS to pick the right day.
		{Start: at(2026, 2, 13, 0, 0), End: at(2026, 3, 6, 0, 0)},
	}
	for _, r := range rules {
		t.Run(r, func(t *testing.T) {
			opt, err := rrule.StrToROption(r)
			if err != nil {
				t.Fatal(err)
			}
			opt.Dtstart = dtstart
			rr, err := rrule.NewRRule(*opt)
			if err != nil {
				t.Fatal(err)
			}
			cal := mustCal(t, vevent("DTSTART:"+dtstart.Format("20060102T150405Z"), "RRULE:"+r))
			for i, win := range wins {
				var want []Occurrence
				for _, s := range rr.Between(win.Start, win.End, true) {
					if s.Before(win.End) {
						want = append(want, occ(s, s))
					}
				}
				if i == 0 && len(want) == 0 {
					t.Fatal("rule has no instances in the window; pick another")
				}
				got, err := cal.Occurrences(nil, nil, win)
				if err != nil {
					t.Fatal(err)
				}
				if !sameOccs(got, want) {
					t.Fatalf("window %d: got%s\nwant%s", i, fmtOccs(got), fmtOccs(want))
				}
			}
		})
	}
}

func TestOccurrencesConcurrent(t *testing.T) {
	cal := mustCal(t, pragueTZ, vevent("DTSTART;"+prague+":20200101T090000", "DTEND;"+prague+":20200101T100000",
		"RRULE:FREQ=DAILY", "EXDATE;"+prague+":20260302T090000"),
		vevent("RECURRENCE-ID;"+prague+":20260303T090000", "DTSTART;"+prague+":20260303T120000"))
	res := NewResolver(cal)
	win := Range{Start: at(2026, 3, 1, 0, 0), End: at(2026, 4, 1, 0, 0)}
	want, err := cal.Occurrences(res, nil, win)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := cal.Occurrences(res, nil, win)
			if err != nil || !sameOccs(got, want) {
				t.Errorf("concurrent call differs: %v", err)
			}
		})
	}
	wg.Wait()
	if len(want) != 30 {
		t.Fatalf("got %d occurrences, want 30", len(want))
	}
}

func FuzzOccurrences(f *testing.F) {
	f.Add("FREQ=WEEKLY;BYDAY=MO", "20260316T090000", "20260323T090000", int64(0), int64(7), uint8(0))
	f.Add("FREQ=SECONDLY;BYMONTH=2;BYMONTHDAY=30", "20260316", "20260323", int64(-1000), int64(0), uint8(0))
	f.Add("FREQ=MONTHLY;BYSETPOS=-1;BYDAY=MO,FR;COUNT=40", "00010101T000000", "99991231", int64(0), int64(0), uint8(0))
	f.Add("FREQ=DAILY;INTERVAL=3", "19000101T090000", "20260101T090000", int64(0), int64(30), uint8(1))
	f.Add("FREQ=HOURLY;BYHOUR=9,17", "20260101T090000", "20260101T170000", int64(5), int64(30), uint8(2))
	f.Add("FREQ=YEARLY", "00010101T000000", "00010101T000000", int64(0), int64(0), uint8(3))
	f.Fuzz(func(t *testing.T, rule, start, exdate string, from, days int64, open uint8) {
		if strings.ContainsAny(rule+start+exdate, "\r\n") {
			return
		}
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//y//EN\r\n" + pragueTZ +
			vevent("DTSTART;"+prague+":"+start, "RRULE:"+rule, "EXDATE:"+exdate, "DURATION:PT1H") +
			"END:VCALENDAR\r\n"
		cal, err := Parse([]byte(data))
		if err != nil {
			return
		}
		lo := at(2026, 1, 1, 0, 0).Add(time.Duration(from%(1<<40)) * time.Hour)
		win := Range{Start: lo, End: lo.AddDate(0, 0, int(days%100000))}
		// Bit 0 opens the start, bit 1 the end.
		if open&1 != 0 {
			win.Start = time.Time{}
		}
		if open&2 != 0 {
			win.End = time.Time{}
		}
		got, err := cal.Occurrences(nil, nil, win)
		if err != nil && !errors.Is(err, ErrRecurrence) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if len(got) > maxOccurrences {
			t.Fatalf("%d occurrences", len(got))
		}
	})
}

func TestOccurrencesStepCost(t *testing.T) {
	rejected := []string{
		"FREQ=SECONDLY;BYHOUR=23;BYMINUTE=59;BYSECOND=59",
		"FREQ=SECONDLY;INTERVAL=7;COUNT=10000;BYHOUR=23;BYMINUTE=59;BYSECOND=59",
		"FREQ=SECONDLY;BYMINUTE=7;BYSECOND=7",
	}
	for _, r := range rejected {
		cal := mustCal(t, vevent("DTSTART:20260101T000000Z", "RRULE:"+r))
		if _, err := cal.Occurrences(nil, nil, Range{}); !errors.Is(err, ErrRecurrence) {
			t.Errorf("%s: err = %v, want ErrRecurrence", r, err)
		}
	}
	accepted := []string{
		"FREQ=SECONDLY",
		"FREQ=SECONDLY;BYHOUR=9",
		"FREQ=MINUTELY;BYMINUTE=0,30",
		"FREQ=HOURLY;BYHOUR=9,10,11,12,13,14,15,16,17",
	}
	for _, r := range accepted {
		cal := mustCal(t, vevent("DTSTART:20260101T000000Z", "RRULE:"+r))
		if got, err := cal.Occurrences(nil, nil, Range{}); err != nil || len(got) == 0 {
			t.Errorf("%s: %d occurrences, err = %v", r, len(got), err)
		}
	}

	tests := []struct {
		rule        string
		cycle, hits int64
	}{
		{"FREQ=DAILY;BYHOUR=1", 1, 1},
		{"FREQ=SECONDLY", 86400, 86400},
		{"FREQ=SECONDLY;BYHOUR=23;BYMINUTE=59;BYSECOND=59", 86400, 1},
		// 7 and 86400 are coprime, so every second comes round in time.
		{"FREQ=SECONDLY;INTERVAL=7;BYHOUR=23;BYMINUTE=59;BYSECOND=59", 86400, 1},
		{"FREQ=SECONDLY;INTERVAL=2;BYSECOND=59", 43200, 0},
		{"FREQ=MINUTELY;BYHOUR=3;BYMINUTE=7", 1440, 1},
		{"FREQ=MINUTELY;BYMINUTE=0,30,30", 1440, 48},
		{"FREQ=HOURLY;INTERVAL=5;BYHOUR=3,4", 24, 2},
		{"FREQ=HOURLY;INTERVAL=24;BYHOUR=5", 1, 0},
	}
	for _, tt := range tests {
		opt, err := rrule.StrToROption(tt.rule)
		if err != nil {
			t.Fatal(err)
		}
		opt.Dtstart = at(2026, 1, 1, 0, 0)
		opt.Interval = max(opt.Interval, 1)
		if cycle, hits := stepCost(*opt); cycle != tt.cycle || hits != tt.hits {
			t.Errorf("%s: stepCost = %d, %d; want %d, %d", tt.rule, cycle, hits, tt.cycle, tt.hits)
		}
	}
}

func TestCycleYears(t *testing.T) {
	for _, tt := range []struct{ year, want int }{{2026, 7600}, {9599, 400}, {9600, 0}, {9999, 0}, {10500, 0}} {
		if got := cycleYears(at(tt.year, 6, 1, 0, 0)); got != tt.want {
			t.Errorf("cycleYears(%d) = %d, want %d", tt.year, got, tt.want)
		}
	}
}

// TestOccurrencesDenseWindowFarFromStart checks that the margins around
// the window do not use up the cap: a one-hour window of a secondly rule
// has all its 3,600 occurrences.
func TestOccurrencesDenseWindowFarFromStart(t *testing.T) {
	for _, start := range []string{"DTSTART:20200101T000000Z", "DTSTART;" + prague + ":20200101T000000"} {
		cal := mustCal(t, pragueTZ, vevent(start, "RRULE:FREQ=SECONDLY"))
		win := Range{Start: at(2026, 6, 1, 10, 0), End: at(2026, 6, 1, 11, 0)}
		got, err := cal.Occurrences(nil, nil, win)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3600 || !got[0].Start.Equal(win.Start) || !got[3599].Start.Equal(win.End.Add(-time.Second)) {
			t.Fatalf("%s: got %d occurrences%s ...", start, len(got), fmtOccs(got[:min(len(got), 2)]))
		}
	}
}

func TestOccurrencesYearOne(t *testing.T) {
	first := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		rule string
		want int
	}{
		{"", 1},
		{"RRULE:FREQ=YEARLY;COUNT=3", 3},
		{"RRULE:FREQ=YEARLY", 100},
	}
	for _, tt := range tests {
		lines := []string{"DTSTART:00010101T000000Z"}
		if tt.rule != "" {
			lines = append(lines, tt.rule)
		}
		cal := mustCal(t, vevent(lines...))
		got, err := cal.Occurrences(nil, nil, Range{})
		if err != nil {
			t.Fatalf("%q: %v", tt.rule, err)
		}
		if len(got) != tt.want || !got[0].Start.Equal(first) {
			t.Fatalf("%q: got %d occurrences%s ..., want %d from year 1", tt.rule, len(got),
				fmtOccs(got[:min(len(got), 2)]), tt.want)
		}
	}
}

func TestOccurrencesOpenStartCountsBackFromEnd(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:19000101T000000Z", "RRULE:FREQ=YEARLY"))
	got, err := cal.Occurrences(nil, nil, Range{End: at(2026, 1, 1, 0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 || !got[0].Start.Equal(at(1926, 1, 1, 0, 0)) || !got[99].Start.Equal(at(2025, 1, 1, 0, 0)) {
		t.Fatalf("got %d occurrences%s ..., want 1926 to 2025", len(got), fmtOccs(got[:min(len(got), 2)]))
	}
}

func TestOccurrencesDriftingTimeOfDay(t *testing.T) {
	var s []string
	for i := 0; i < 60; i += 7 {
		s = append(s, fmt.Sprint(i))
	}
	sevens := strings.Join(s, ",")
	times := ";BYHOUR=0,7,14,21;BYMINUTE=" + sevens + ";BYSECOND=" + sevens
	rejected := []string{
		"FREQ=SECONDLY;INTERVAL=7;BYDAY=MO" + times,
		"FREQ=SECONDLY;INTERVAL=7;BYMONTH=2;BYMONTHDAY=29" + times,
		"FREQ=SECONDLY;INTERVAL=7;BYYEARDAY=1" + times,
		"FREQ=MINUTELY;INTERVAL=7;BYWEEKNO=1;BYMINUTE=0",
		"FREQ=HOURLY;INTERVAL=5;BYDAY=MO;BYHOUR=9",
	}
	for _, r := range rejected {
		cal := mustCal(t, vevent("DTSTART:20260302T000000Z", "RRULE:"+r))
		if _, err := cal.Occurrences(nil, nil, Range{}); !errors.Is(err, ErrRecurrence) {
			t.Errorf("%s: err = %v, want ErrRecurrence", r, err)
		}
	}
	week := Range{Start: at(2026, 3, 2, 9, 0), End: at(2026, 3, 9, 9, 0)} // Monday to Monday
	accepted := []struct {
		rule string
		want int
	}{
		{"FREQ=MINUTELY;INTERVAL=15;BYDAY=MO,TU,WE,TH,FR", 5 * 96},
		{"FREQ=HOURLY;BYHOUR=9,10,11,12,13,14,15,16,17;BYDAY=MO,TU,WE,TH,FR", 5 * 9},
		{"FREQ=MINUTELY;INTERVAL=15;BYHOUR=9,10,11,12,13,14,15,16,17;BYDAY=MO,TU,WE,TH,FR", 5 * 9 * 4},
		// 09:00 to midnight, then midnight to 09:00 a week on.
		{"FREQ=MINUTELY;INTERVAL=7;BYDAY=MO", 129 + 77},
	}
	for _, tt := range accepted {
		cal := mustCal(t, vevent("DTSTART:20260302T090000Z", "RRULE:"+tt.rule))
		got, err := cal.Occurrences(nil, nil, week)
		if err != nil || len(got) != tt.want {
			t.Errorf("%s: %d occurrences, err = %v; want %d", tt.rule, len(got), err, tt.want)
		}
	}
}

// dropTZ goes back by 23 hours at 2026-05-31T12:00Z, from +12 to -11, as
// Pacific/Kwajalein did in 1969 the other way round.
const dropTZ = "BEGIN:VTIMEZONE\r\nTZID:Test/Drop\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19000101T000000\r\nTZOFFSETFROM:+1200\r\nTZOFFSETTO:+1200\r\nEND:STANDARD\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:20260601T000000\r\nTZOFFSETFROM:+1200\r\nTZOFFSETTO:-1100\r\nEND:STANDARD\r\n" +
	"END:VTIMEZONE\r\n"

// TestOccurrencesLargeFallBack checks windows next to a 23-hour fall-back
// against a wide expansion filtered to the same window.
func TestOccurrencesLargeFallBack(t *testing.T) {
	events := []string{
		vevent("DTSTART;TZID=Test/Drop:20260501T000000", "RRULE:FREQ=HOURLY"),
		vevent("DTSTART;TZID=Test/Drop:20260501T230000", "DURATION:P5D", "RRULE:FREQ=DAILY"),
	}
	wide := Range{Start: at(2026, 5, 20, 0, 0), End: at(2026, 6, 10, 0, 0)}
	wins := []Range{
		{Start: at(2026, 5, 31, 10, 0), End: at(2026, 5, 31, 13, 0)},
		{Start: at(2026, 5, 31, 12, 30), End: at(2026, 5, 31, 12, 45)},
		{Start: at(2026, 5, 31, 13, 0), End: at(2026, 6, 1, 12, 0)},
	}
	for i, ev := range events {
		cal := mustCal(t, dropTZ, ev)
		all, err := cal.Occurrences(nil, nil, wide)
		if err != nil {
			t.Fatal(err)
		}
		for j, w := range wins {
			var want []Occurrence
			for _, o := range all {
				if overlaps(o, w.Start, w.End) {
					want = append(want, o)
				}
			}
			got, err := cal.Occurrences(nil, nil, w)
			if err != nil {
				t.Fatal(err)
			}
			// The hourly event has nothing in the quarter hour after the change.
			if len(want) == 0 && (i != 0 || j != 1) {
				t.Fatalf("event %d window %d: oracle is empty", i, j)
			}
			if !sameOccs(got, want) {
				t.Errorf("event %d window %d: got%s\nwant%s", i, j, fmtOccs(got), fmtOccs(want))
			}
		}
	}
}
