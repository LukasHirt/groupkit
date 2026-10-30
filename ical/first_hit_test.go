// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func stamp(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

// occursInBoth runs the first-hit path and the listing path and fails if
// they disagree. A recurrence that cannot be expanded matches nothing in
// either, so an error counts as false.
func occursInBoth(t *testing.T, cal *Calendar, win Range) bool {
	t.Helper()
	got, gerr := cal.occursIn(nil, nil, win)
	occs, werr := cal.Occurrences(nil, nil, win)
	newB, oldB := gerr == nil && got, werr == nil && len(occs) > 0
	if newB != oldB {
		t.Fatalf("occursIn = %v (err %v), Occurrences found %d (err %v)%s", got, gerr, len(occs), werr, fmtOccs(occs))
	}
	return newB
}

func TestOccursInStopsAtTheFirstHit(t *testing.T) {
	// DTSTART is excluded, so the answer comes from the rule.
	cal := mustCal(t, vevent("DTSTART:20000103T090000Z", "DTEND:20000103T100000Z",
		"RRULE:FREQ=WEEKLY", "EXDATE:20000103T090000Z"))
	for name, win := range map[string]Range{
		"open start":      {End: at(2100, 1, 1, 0, 0)},
		"both open":       {},
		"1900 to 2100":    {Start: at(1900, 1, 1, 0, 0), End: at(2100, 1, 1, 0, 0)},
		"start only":      {Start: at(1990, 1, 1, 0, 0)},
		"start after 2k":  {Start: at(2010, 6, 1, 0, 0)},
		"far-future only": {Start: at(2090, 1, 1, 0, 0)},
	} {
		t.Run(name, func(t *testing.T) {
			steps := stepsOf(func() {
				if ok, err := cal.occursIn(nil, nil, win); err != nil || !ok {
					t.Fatalf("occursIn = %v, %v", ok, err)
				}
			})
			if steps > 20 {
				t.Errorf("first-hit examined %d instances, want a handful", steps)
			}
			occursInBoth(t, cal, win)
		})
	}
	// The listing path, for contrast, walks far more.
	list := stepsOf(func() { _, _ = cal.Occurrences(nil, nil, Range{End: at(2100, 1, 1, 0, 0)}) })
	if list < 1000 {
		t.Errorf("Occurrences examined only %d instances; the contrast is gone", list)
	}
}

func TestOccursInManyRangesStayCheap(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20000103T090000Z", "RRULE:FREQ=DAILY", "EXDATE:20000103T090000Z"))
	// Sixteen distinct open-start ranges in one filter, as the cap allows.
	// Comp-filters all have to match, so every range is evaluated.
	f := &CompFilter{Name: "VCALENDAR"}
	for i := range 16 {
		tr := TimeRange{End: at(2050+i, 1, 1, 0, 0)}
		f.Comps = append(f.Comps, CompFilter{Name: "VEVENT", TimeRange: &tr})
	}
	steps := stepsOf(func() {
		if ok, err := cal.Matches(f, time.UTC); err != nil || !ok {
			t.Fatalf("Matches = %v, %v", ok, err)
		}
	})
	if steps > 200 {
		t.Errorf("16 ranges examined %d instances, want a few", steps)
	}
	// Ranges that end before DTSTART cost nothing.
	none := &CompFilter{Name: "VCALENDAR"}
	for i := range 16 {
		tr := TimeRange{Start: at(1900, 1, 1+i, 0, 0), End: at(1990, 1, 1+i, 0, 0)}
		none.Comps = append(none.Comps, CompFilter{Name: "VEVENT", TimeRange: &tr})
	}
	steps = stepsOf(func() {
		if ok, err := cal.Matches(none, time.UTC); err != nil || ok {
			t.Fatalf("Matches before DTSTART = %v, %v", ok, err)
		}
	})
	if steps > 200 {
		t.Errorf("16 empty ranges examined %d instances", steps)
	}
}

func TestOccursInCases(t *testing.T) {
	weekly := []string{"DTSTART:20260105T090000Z", "DTEND:20260105T100000Z", "RRULE:FREQ=WEEKLY"}
	win := func(sy int, sm time.Month, sd int, ey int, em time.Month, ed int) Range {
		return Range{Start: at(sy, sm, sd, 0, 0), End: at(ey, em, ed, 0, 0)}
	}
	tests := []struct {
		name string
		cal  []string // master lines, then overrides separated by "---"
		win  Range
		want bool
	}{
		{"hit on DTSTART", weekly, win(2026, 1, 5, 2026, 1, 6), true},
		{"before DTSTART", weekly, win(2025, 1, 1, 2026, 1, 1), false},
		{"EXDATE on DTSTART, next week hits", append(slicesClone(weekly), "EXDATE:20260105T090000Z"), win(2026, 1, 5, 2026, 1, 13), true},
		{"EXDATE on DTSTART, window only that day", append(slicesClone(weekly), "EXDATE:20260105T090000Z"), win(2026, 1, 5, 2026, 1, 6), false},
		{"every in-window occurrence excluded", append(slicesClone(weekly), "EXDATE:20260112T090000Z,20260119T090000Z,20260126T090000Z"), win(2026, 1, 12, 2026, 2, 2), false},
		{"one in-window occurrence left", append(slicesClone(weekly), "EXDATE:20260112T090000Z,20260119T090000Z"), win(2026, 1, 12, 2026, 2, 2), true},
		{"COUNT exhausted before the window", []string{"DTSTART:20260105T090000Z", "RRULE:FREQ=WEEKLY;COUNT=3"}, win(2026, 3, 1, 2026, 4, 1), false},
		{"COUNT reaches the window", []string{"DTSTART:20260105T090000Z", "RRULE:FREQ=WEEKLY;COUNT=3"}, win(2026, 1, 19, 2026, 1, 20), true},
		{"UNTIL before the window", []string{"DTSTART:20260105T090000Z", "RRULE:FREQ=WEEKLY;UNTIL=20260201T000000Z"}, win(2026, 3, 1, 2026, 4, 1), false},
		{"RDATE inside", []string{"DTSTART:20260105T090000Z", "RDATE:20260301T090000Z"}, win(2026, 3, 1, 2026, 3, 2), true},
		{"RDATE excluded", []string{"DTSTART:20260105T090000Z", "RDATE:20260301T090000Z", "EXDATE:20260301T090000Z"}, win(2026, 3, 1, 2026, 3, 2), false},
		{"open start finds a late series", []string{"DTSTART:20990105T090000Z", "RRULE:FREQ=DAILY"}, Range{End: at(2100, 1, 1, 0, 0)}, true},
		{"instant event touching the end", []string{"DTSTART:20260105T090000Z"}, Range{Start: at(2026, 1, 5, 8, 0), End: at(2026, 1, 5, 9, 0)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := mustCal(t, vevent(tt.cal...))
			if got := occursInBoth(t, cal, tt.win); got != tt.want {
				t.Errorf("occursIn = %v, want %v", got, tt.want)
			}
		})
	}
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func TestOccursInOverrides(t *testing.T) {
	master := vevent("DTSTART:20260105T090000Z", "DTEND:20260105T100000Z", "RRULE:FREQ=WEEKLY;COUNT=4")
	override := func(rid, start string, extra ...string) string {
		return vevent(append([]string{"RECURRENCE-ID:" + rid, "DTSTART:" + start, "DTEND:" + start[:9] + "100000Z"}, extra...)...)
	}
	tests := []struct {
		name   string
		blocks []string
		win    Range
		want   bool
	}{
		{"moved into a window from outside", []string{master, override("20260112T090000Z", "20260601T090000Z")},
			Range{Start: at(2026, 6, 1, 0, 0), End: at(2026, 6, 2, 0, 0)}, true},
		{"moved out of the window, slot excluded", []string{master, override("20260112T090000Z", "20260601T090000Z")},
			Range{Start: at(2026, 1, 12, 0, 0), End: at(2026, 1, 13, 0, 0)}, false},
		{"moved out, but another week still hits", []string{master, override("20260112T090000Z", "20260601T090000Z")},
			Range{Start: at(2026, 1, 12, 0, 0), End: at(2026, 1, 20, 0, 0)}, true},
		{"cancelled override is still an occurrence", []string{master, override("20260112T090000Z", "20260112T090000Z", "STATUS:CANCELLED")},
			Range{Start: at(2026, 1, 12, 0, 0), End: at(2026, 1, 13, 0, 0)}, true},
		{"override only, no master", []string{override("20260112T090000Z", "20260113T090000Z")},
			Range{Start: at(2026, 1, 13, 0, 0), End: at(2026, 1, 14, 0, 0)}, true},
		{"override whose original slot is not in the window", []string{master, override("20260119T090000Z", "20260601T090000Z")},
			Range{Start: at(2026, 1, 12, 0, 0), End: at(2026, 1, 13, 0, 0)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cal := mustCal(t, tt.blocks...)
			if got := occursInBoth(t, cal, tt.win); got != tt.want {
				t.Errorf("occursIn = %v, want %v", got, tt.want)
			}
		})
	}
}

// The first-hit path must agree with a non-empty Occurrences list on every
// combination of rule, exclusions, overrides and window.
func TestOccursInAgreesWithOccurrences(t *testing.T) {
	rules := []string{
		"", "FREQ=DAILY", "FREQ=DAILY;COUNT=5", "FREQ=DAILY;INTERVAL=3;COUNT=10",
		"FREQ=WEEKLY;BYDAY=MO,WE", "FREQ=WEEKLY;COUNT=6;BYDAY=TU,TH",
		"FREQ=MONTHLY;BYMONTHDAY=31", "FREQ=MONTHLY;BYDAY=-1FR;UNTIL=20270101T000000Z",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29", "FREQ=HOURLY;INTERVAL=7;COUNT=40",
		"FREQ=MONTHLY;BYSETPOS=1;BYDAY=MO,TU,WE,TH,FR",
	}
	rng := rand.New(rand.NewSource(20260401))
	base := at(2026, 1, 5, 9, 0)
	day := func(n int) time.Time { return base.AddDate(0, 0, n).Add(time.Duration(rng.Intn(3)) * time.Hour) }
	hits := 0
	const cases = 2000
	for i := range cases {
		rule := rules[rng.Intn(len(rules))]
		lines := []string{"DTSTART:" + stamp(base), "DTEND:" + stamp(base.Add(time.Duration(rng.Intn(3))*time.Hour))}
		if rule != "" {
			lines = append(lines, "RRULE:"+rule)
		}
		if rng.Intn(3) == 0 {
			lines = append(lines, "RDATE:"+stamp(day(rng.Intn(200))))
		}
		// Exclusions on the series' own slots and on random ones.
		var ex []string
		for range rng.Intn(5) {
			switch rng.Intn(3) {
			case 0:
				ex = append(ex, stamp(base))
			case 1:
				ex = append(ex, stamp(base.AddDate(0, 0, 7*rng.Intn(30))))
			default:
				ex = append(ex, stamp(base.AddDate(0, 0, rng.Intn(200))))
			}
		}
		if len(ex) > 0 {
			lines = append(lines, "EXDATE:"+strings.Join(ex, ","))
		}
		// Now and then break the master, so errors have to agree too.
		switch rng.Intn(40) {
		case 0:
			lines = append(lines, "EXDATE:garbage")
		case 1:
			lines = append(lines, "RDATE:garbage")
		case 2:
			lines = append(lines, "DTEND:garbage")
		case 3:
			lines = append(lines[1:2:2], lines[2:]...) // no DTSTART
		case 4:
			lines = append(lines, "RRULE:FREQ=WEEKLY;BYMONTHDAY=40")
		}
		blocks := []string{vevent(lines...)}
		for range rng.Intn(3) {
			rid := base.AddDate(0, 0, 7*rng.Intn(10))
			start := day(rng.Intn(300))
			extra := []string{"RECURRENCE-ID:" + stamp(rid), "DTSTART:" + stamp(start), "DTEND:" + stamp(start.Add(time.Hour))}
			if rng.Intn(4) == 0 {
				extra = append(extra, "STATUS:CANCELLED")
			}
			blocks = append(blocks, vevent(extra...))
		}
		cal := mustCal(t, blocks...)
		var win Range
		s, e := day(rng.Intn(260)), day(rng.Intn(260)+1)
		if e.Before(s) {
			s, e = e, s
		}
		switch rng.Intn(4) {
		case 0:
			win = Range{End: e}
		case 1:
			win = Range{Start: s}
		default:
			win = Range{Start: s, End: e.Add(time.Hour)}
		}
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if occursInBoth(t, cal, win) {
				hits++
			}
		})
	}
	if hits < cases/10 || hits > cases*9/10 {
		t.Errorf("%d of %d cases hit; the table is lopsided", hits, cases)
	}
}

// Sixteen open-start ranges over many recurring objects, as a client query
// would ask, examine a few instances per object and range.
func TestOccursInManyObjectsStayCheap(t *testing.T) {
	f := &CompFilter{Name: "VCALENDAR"}
	for i := range 16 {
		tr := TimeRange{End: at(2060+i, 1, 1, 0, 0)}
		f.Comps = append(f.Comps, CompFilter{Name: "VEVENT", TimeRange: &tr})
	}
	const objects = 100
	steps := stepsOf(func() {
		for i := range objects {
			cal := mustCal(t, vevent(fmt.Sprintf("DTSTART:%04d0101T090000Z", 1950+i%40), "RRULE:FREQ=WEEKLY",
				"EXDATE:"+fmt.Sprintf("%04d0101T090000Z", 1950+i%40)))
			if ok, err := cal.Matches(f, time.UTC); err != nil || !ok {
				t.Fatalf("object %d: Matches = %v, %v", i, ok, err)
			}
		}
	})
	if limit := int64(objects * 16 * 5); steps > limit {
		t.Errorf("examined %d instances, want at most %d", steps, limit)
	}
}

// A master that cannot be read makes the recurrence unexpandable, so it
// matches nothing even when an override sits in the window.
func TestOccursInBrokenMasterNeverMatches(t *testing.T) {
	override := vevent("RECURRENCE-ID:20260112T090000Z", "DTSTART:20260112T090000Z", "DTEND:20260112T100000Z")
	win := Range{Start: at(2026, 1, 12, 0, 0), End: at(2026, 1, 13, 0, 0)}
	for name, bad := range map[string]string{
		"RRULE without FREQ": "RRULE:INTERVAL=2",
		"bad BYMONTHDAY":     "RRULE:FREQ=MONTHLY;BYMONTHDAY=40",
		"bad EXDATE":         "EXDATE:garbage",
		"bad RDATE":          "RDATE:garbage",
		"bad DTEND":          "DTEND:garbage",
		"bad DURATION":       "DURATION:garbage",
	} {
		t.Run(name, func(t *testing.T) {
			// A bad RRULE stands in for the good one: only the first counts.
			rule := "RRULE:FREQ=WEEKLY"
			if strings.HasPrefix(bad, "RRULE:") {
				rule = bad
			}
			for what, cal := range map[string]*Calendar{
				"override in the window": mustCal(t, vevent("DTSTART:20260105T090000Z", rule, bad), override),
				"DTSTART in the window":  mustCal(t, vevent("DTSTART:20260112T090000Z", rule, bad)),
				"RDATE in the window":    mustCal(t, vevent("DTSTART:20260105T090000Z", "RDATE:20260112T090000Z", bad)),
			} {
				if occursInBoth(t, cal, win) {
					t.Errorf("%s: a broken master matched", what)
				}
			}
		})
	}
	t.Run("no DTSTART", func(t *testing.T) {
		cal := mustCal(t, vevent("RRULE:FREQ=WEEKLY"), override)
		if occursInBoth(t, cal, win) {
			t.Error("a master without DTSTART matched through its override")
		}
	})
}

func TestOccursInStepLimitIsAnError(t *testing.T) {
	old := maxRuleSteps
	maxRuleSteps = 50
	t.Cleanup(func() { maxRuleSteps = old })
	// The first sixty days are excluded, so the walk runs out of steps
	// before it finds anything.
	lines := []string{"DTSTART:20260105T090000Z", "RRULE:FREQ=DAILY"}
	var ex []string
	for i := range 60 {
		ex = append(ex, stamp(at(2026, 1, 5, 9, 0).AddDate(0, 0, i)))
	}
	lines = append(lines, "EXDATE:"+strings.Join(ex, ","))
	cal := mustCal(t, vevent(lines...))
	win := Range{Start: at(2026, 1, 5, 0, 0), End: at(2027, 1, 1, 0, 0)}
	if _, err := cal.occursIn(nil, nil, win); err == nil {
		t.Error("no error from a walk past the step limit")
	}
	if occursInBoth(t, cal, win) {
		t.Error("matched after the step limit")
	}
}

// Instances that are skipped count against the cap of 10,000 as in the full
// expansion, so both paths give up at the same point.
func TestOccursInCapEdge(t *testing.T) {
	var lines []string
	start := time.Date(1900, 1, 1, 9, 0, 0, 0, time.UTC)
	for c := range 100 {
		days := make([]string, 100)
		for i := range days {
			days[i] = stamp(start.AddDate(0, 0, c*100+i))
		}
		lines = append(lines, "EXDATE:"+strings.Join(days, ","))
	}
	cal := mustCal(t, vevent(append([]string{"DTSTART:19000101T090000Z", "RRULE:FREQ=DAILY"}, lines...)...))
	win := Range{Start: at(1900, 1, 1, 0, 0), End: at(2000, 1, 1, 0, 0)}
	if occursInBoth(t, cal, win) {
		t.Error("the cap should have ended the search before the first free day")
	}
	// One exclusion fewer and the 10,000th instance is free.
	cal = mustCal(t, vevent(append([]string{"DTSTART:19000101T090000Z", "RRULE:FREQ=DAILY"}, lines[:99]...)...))
	if !occursInBoth(t, cal, win) {
		t.Error("an instance inside the cap was missed")
	}
}

func TestOccursInCountPastTheCap(t *testing.T) {
	cal := mustCal(t, vevent("DTSTART:20000101T090000Z", "RRULE:FREQ=DAILY;COUNT=20000"))
	if occursInBoth(t, cal, Range{Start: at(2030, 1, 1, 0, 0), End: at(2030, 2, 1, 0, 0)}) {
		t.Error("COUNT counts from DTSTART, so the cap ends the search before 2030")
	}
	if !occursInBoth(t, cal, Range{Start: at(2020, 1, 1, 0, 0), End: at(2020, 2, 1, 0, 0)}) {
		t.Error("an instance inside the cap was missed")
	}
	// All of the rule excluded still finds an RDATE.
	cal = mustCal(t, vevent("DTSTART:20000101T090000Z", "RRULE:FREQ=DAILY;UNTIL=20000105T090000Z", "RDATE:20000110T090000Z",
		"EXDATE:20000101T090000Z,20000102T090000Z,20000103T090000Z,20000104T090000Z,20000105T090000Z"))
	if !occursInBoth(t, cal, Range{Start: at(2000, 1, 1, 0, 0), End: at(2000, 2, 1, 0, 0)}) {
		t.Error("RDATE missed")
	}
}
