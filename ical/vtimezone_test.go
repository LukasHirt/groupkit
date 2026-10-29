// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teambition/rrule-go"
)

const pragueTZ = "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\n" +
	"TZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\n" +
	"TZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\n" +
	"END:VTIMEZONE\r\n"

// mustVTZ parses a VTIMEZONE block wrapped in a minimal VCALENDAR.
func mustVTZ(t *testing.T, block string) *vtz {
	t.Helper()
	z, err := vtzFrom(t, block)
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func vtzFrom(t *testing.T, block string) (*vtz, error) {
	t.Helper()
	cal, err := Parse([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//y//EN\r\n" +
		block + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	tz := cal.Comps("VTIMEZONE")
	if len(tz) != 1 {
		t.Fatalf("want one VTIMEZONE, got %d", len(tz))
	}
	return parseVTimezone(tz[0])
}

func wall(y int, m time.Month, d, h, mi, s int) time.Time {
	return time.Date(y, m, d, h, mi, s, 0, time.UTC)
}

func TestOffsetAtPrague(t *testing.T) {
	z := mustVTZ(t, pragueTZ)
	tests := []struct {
		name string
		at   time.Time
		want time.Duration
	}{
		{"winter", wall(2026, 1, 15, 12, 0, 0), time.Hour},
		{"summer", wall(2026, 7, 15, 12, 0, 0), 2 * time.Hour},
		{"before spring forward", wall(2026, 3, 29, 1, 59, 59), time.Hour},
		{"gap", wall(2026, 3, 29, 2, 30, 0), time.Hour},
		{"after gap", wall(2026, 3, 29, 3, 0, 0), 2 * time.Hour},
		{"overlap", wall(2026, 10, 25, 2, 30, 0), 2 * time.Hour},
		{"after overlap", wall(2026, 10, 25, 3, 30, 0), time.Hour},
		{"new year", wall(2027, 1, 1, 0, 0, 0), time.Hour},
		{"before first onset", wall(1960, 6, 1, 0, 0, 0), time.Hour},
		{"far past", wall(1900, 7, 1, 0, 0, 0), time.Hour},
		{"far future summer", wall(2500, 7, 1, 0, 0, 0), 2 * time.Hour},
		{"far future winter", wall(2500, 12, 1, 0, 0, 0), time.Hour},
		{"last year", wall(9999, 7, 1, 0, 0, 0), 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := z.offsetAt(tt.at); got != tt.want {
				t.Fatalf("offsetAt(%s) = %v, want %v", tt.at.Format(time.DateTime), got, tt.want)
			}
		})
	}
}

func TestOffsetAtSingleStandard(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\nDTSTART:20000101T000000\r\n"+
		"TZOFFSETFROM:+0300\r\nTZOFFSETTO:+0400\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n")
	if got := z.offsetAt(wall(1999, 12, 31, 23, 0, 0)); got != 3*time.Hour {
		t.Errorf("before DTSTART = %v, want 3h", got)
	}
	if got := z.offsetAt(wall(2026, 6, 1, 0, 0, 0)); got != 4*time.Hour {
		t.Errorf("after DTSTART = %v, want 4h", got)
	}
}

func TestOffsetAtRDATE(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\n"+
		"BEGIN:DAYLIGHT\r\nDTSTART:20200301T020000\r\nRDATE:20210301T020000,20220301T020000\r\n"+
		"TZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\nEND:DAYLIGHT\r\n"+
		"BEGIN:STANDARD\r\nDTSTART:20201101T020000\r\nRDATE:20211101T020000\r\n"+
		"RDATE:20221101T020000\r\n"+
		"TZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n")
	tests := []struct {
		at   time.Time
		want time.Duration
	}{
		{wall(2019, 6, 1, 0, 0, 0), -5 * time.Hour},
		{wall(2021, 6, 1, 0, 0, 0), -4 * time.Hour},
		{wall(2021, 12, 1, 0, 0, 0), -5 * time.Hour},
		{wall(2022, 6, 1, 0, 0, 0), -4 * time.Hour},
		{wall(2026, 6, 1, 0, 0, 0), -5 * time.Hour},
	}
	for _, tt := range tests {
		if got := z.offsetAt(tt.at); got != tt.want {
			t.Errorf("offsetAt(%s) = %v, want %v", tt.at.Format(time.DateTime), got, tt.want)
		}
	}
}

func TestOffsetAtNegativeAndSeconds(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\nDTSTART:19000101T000000\r\n"+
		"TZOFFSETFROM:-0500\r\nTZOFFSETTO:+053045\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n")
	if got := z.offsetAt(wall(1800, 1, 1, 0, 0, 0)); got != -5*time.Hour {
		t.Errorf("before = %v, want -5h", got)
	}
	want := 5*time.Hour + 30*time.Minute + 45*time.Second
	if got := z.offsetAt(wall(2026, 1, 1, 0, 0, 0)); got != want {
		t.Errorf("after = %v, want %v", got, want)
	}
}

func TestOffsetAtUntilUTC(t *testing.T) {
	// Daylight saving stops after 2010: the last DAYLIGHT onset is in 2010
	// and the UNTIL is given in UTC, as RFC 5545 requires.
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\n"+
		"BEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"+
		"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU;UNTIL=20101031T010000Z\r\nEND:STANDARD\r\n"+
		"BEGIN:DAYLIGHT\r\nDTSTART:19700329T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n"+
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;UNTIL=20100328T010000Z\r\nEND:DAYLIGHT\r\n"+
		"END:VTIMEZONE\r\n")
	// 2010-03-28 02:00 local +01:00 is 01:00Z, so the onset is included.
	if got := z.offsetAt(wall(2010, 7, 1, 0, 0, 0)); got != 2*time.Hour {
		t.Errorf("2010 summer = %v, want 2h", got)
	}
	// 2010-10-31 03:00 local +02:00 is 01:00Z, so the onset is included.
	if got := z.offsetAt(wall(2010, 12, 1, 0, 0, 0)); got != time.Hour {
		t.Errorf("2010 winter = %v, want 1h", got)
	}
	if got := z.offsetAt(wall(2026, 7, 1, 0, 0, 0)); got != time.Hour {
		t.Errorf("2026 summer = %v, want 1h", got)
	}
}

func TestParseVTimezoneErrors(t *testing.T) {
	obs := func(props string) string {
		return "BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\n" + props + "END:STANDARD\r\nEND:VTIMEZONE\r\n"
	}
	good := "DTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"
	var many strings.Builder
	many.WriteString("BEGIN:VTIMEZONE\r\nTZID:X\r\n")
	for range 101 {
		many.WriteString("BEGIN:STANDARD\r\n" + good + "END:STANDARD\r\n")
	}
	many.WriteString("END:VTIMEZONE\r\n")
	var rdates strings.Builder
	rdates.WriteString(good)
	for i := range 1001 {
		fmt.Fprintf(&rdates, "RDATE:%04d0101T000000\r\n", 1000+i)
	}
	tests := map[string]string{
		"no observances":     "BEGIN:VTIMEZONE\r\nTZID:X\r\nEND:VTIMEZONE\r\n",
		"bad offset":         obs("DTSTART:19701025T030000\r\nTZOFFSETFROM:+2400\r\nTZOFFSETTO:+0100\r\n"),
		"offset no sign":     obs("DTSTART:19701025T030000\r\nTZOFFSETFROM:0200\r\nTZOFFSETTO:+0100\r\n"),
		"offset minutes":     obs("DTSTART:19701025T030000\r\nTZOFFSETFROM:+0260\r\nTZOFFSETTO:+0100\r\n"),
		"offset seconds":     obs("DTSTART:19701025T030000\r\nTZOFFSETFROM:+020060\r\nTZOFFSETTO:+0100\r\n"),
		"offset length":      obs("DTSTART:19701025T030000\r\nTZOFFSETFROM:+02000\r\nTZOFFSETTO:+0100\r\n"),
		"missing offset":     obs("DTSTART:19701025T030000\r\nTZOFFSETTO:+0100\r\n"),
		"missing dtstart":    obs("TZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"),
		"date dtstart":       obs("DTSTART;VALUE=DATE:19701025\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"),
		"bad rrule":          obs(good + "RRULE:FREQ=YEARLY;BYMONTH=13\r\n"),
		"rrule garbage":      obs(good + "RRULE:;;==\r\n"),
		"rrule no freq":      obs(good + "RRULE:BYMONTH=10\r\n"),
		"rrule daily":        obs(good + "RRULE:FREQ=DAILY\r\n"),
		"rrule bad until":    obs(good + "RRULE:FREQ=YEARLY;UNTIL=2010\r\n"),
		"rrule bad byday":    obs(good + "RRULE:FREQ=YEARLY;BYDAY=-99SU\r\n"),
		"bad rdate":          obs(good + "RDATE:20261301T000000\r\n"),
		"too many obs":       many.String(),
		"too many rdates":    obs(rdates.String()),
		"two rrules":         obs(good + "RRULE:FREQ=YEARLY\r\nRRULE:FREQ=YEARLY\r\n"),
		"rrule huge integer": obs(good + "RRULE:FREQ=YEARLY;INTERVAL=99999999999999999999\r\n"),
		"zero dtstart":       obs("DTSTART:00010101T000000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"),
		"rrule dense times": obs(good + "RRULE:FREQ=YEARLY;BYSETPOS=1;BYHOUR=" + seq(0, 23) +
			";BYMINUTE=" + seq(0, 59) + ";BYSECOND=" + seq(0, 59) + "\r\n"),
		"rrule two hours":     obs(good + "RRULE:FREQ=YEARLY;BYHOUR=1,2\r\n"),
		"rrule two minutes":   obs(good + "RRULE:FREQ=YEARLY;BYMINUTE=0,30\r\n"),
		"rrule two seconds":   obs(good + "RRULE:FREQ=YEARLY;BYSECOND=0,30\r\n"),
		"rrule long monthday": obs(good + "RRULE:FREQ=MONTHLY;BYMONTHDAY=" + seq(1, 31) + ",-1\r\n"),
		"rrule long setpos":   obs(good + "RRULE:FREQ=YEARLY;BYDAY=SU;BYSETPOS=" + seq(1, 32) + "\r\n"),
		"rrule long yearday":  obs(good + "RRULE:FREQ=YEARLY;BYYEARDAY=" + seq(1, 32) + "\r\n"),
		"rrule long weekno":   obs(good + "RRULE:FREQ=YEARLY;BYDAY=SU;BYWEEKNO=" + seq(1, 32) + "\r\n"),
		"rrule long byday":    obs(good + "RRULE:FREQ=YEARLY;BYDAY=" + strings.Repeat("SU,", 31) + "SU\r\n"),
		"rrule long month":    obs(good + "RRULE:FREQ=YEARLY;BYMONTH=" + strings.Repeat("1,", 31) + "1\r\n"),
		"rdate date":          obs(good + "RDATE;VALUE=DATE:20261025\r\n"),
		"rrule many onsets":   obs(good + "RRULE:FREQ=MONTHLY;BYMONTHDAY=1,8,15,22\r\n"),
		"rrule lookalike day": obs(good + "RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1\u017fU\r\n"),
	}
	for name, block := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := vtzFrom(t, block); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := parseVTimezone(&Component{Name: "VEVENT"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("VEVENT: err = %v, want ErrInvalid", err)
	}
}

// seq returns the numbers from a to b joined by commas.
func seq(a, b int) string {
	var parts []string
	for i := a; i <= b; i++ {
		parts = append(parts, fmt.Sprint(i))
	}
	return strings.Join(parts, ",")
}

func TestParseOffset(t *testing.T) {
	tests := map[string]time.Duration{
		"+0100":   time.Hour,
		"-0500":   -5 * time.Hour,
		"+0000":   0,
		"-0000":   0,
		"+053045": 5*time.Hour + 30*time.Minute + 45*time.Second,
		"-235959": -(23*time.Hour + 59*time.Minute + 59*time.Second),
	}
	for raw, want := range tests {
		got, err := parseOffset(raw)
		if err != nil || got != want {
			t.Errorf("parseOffset(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "+", "0100", "+01", "+1:00", "+01:00", "+2400", "+0160", "+010060", "+01000", "+0100000", "+01a0", "１"} {
		if _, err := parseOffset(raw); err == nil {
			t.Errorf("parseOffset(%q): want error", raw)
		}
	}
}

// Rules that would expand to a huge or endless number of onsets must not
// hang offsetAt.
// setRuleSteps lowers the per-expansion step limit for one test.
func setRuleSteps(t *testing.T, n int) {
	t.Helper()
	old := maxRuleSteps
	maxRuleSteps = n
	t.Cleanup(func() { maxRuleSteps = old })
}

func standardWith(rule string) string {
	return "BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\nDTSTART:00010101T020000\r\n" +
		"TZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" + rule + "END:STANDARD\r\nEND:VTIMEZONE\r\n"
}

// Rules that never match would make every expansion search up to year
// 9999, so parsing drops them.
func TestParseVTimezoneDropsNeverMatchingRules(t *testing.T) {
	for _, rule := range []string{
		"RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30\r\n",
		"RRULE:FREQ=MONTHLY;BYMONTH=2;BYMONTHDAY=30\r\n",
	} {
		if z := mustVTZ(t, standardWith(rule)); z.obs[0].rule != nil {
			t.Errorf("%s: rule kept", rule)
		}
	}
}

// A lookup far from DTSTART walks only a bounded number of instances. The
// limits below are far smaller than walking from year 1 would need, so the
// rule would be dropped as broken if the start were not moved forward.
func TestOffsetAtBoundedSteps(t *testing.T) {
	tests := []struct {
		rule  string
		steps int
	}{
		{"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n", 1000},
		{"RRULE:FREQ=MONTHLY;BYMONTHDAY=1,15\r\n", 12000},
	}
	for _, tt := range tests {
		z := mustVTZ(t, standardWith(tt.rule))
		setRuleSteps(t, tt.steps)
		for _, y := range []int{1, 1900, 2026, 2500, 9999} {
			if got := z.offsetAt(wall(y, 6, 1, 0, 0, 0)); got != 2*time.Hour {
				t.Errorf("%s %d: offsetAt = %v, want 2h", tt.rule, y, got)
			}
		}
		if z.obs[0].rule == nil {
			t.Errorf("%s: rule dropped as broken", tt.rule)
		}
	}
}

// A rule that fails during a lookup is dropped for good; DTSTART and RDATE
// onsets still count.
func TestOffsetAtBrokenRule(t *testing.T) {
	z := mustVTZ(t, pragueTZ)
	if got := z.offsetAt(wall(2015, 7, 1, 0, 0, 0)); got != 2*time.Hour {
		t.Fatalf("2015 summer = %v, want 2h", got)
	}
	setRuleSteps(t, 3)
	for range 2 {
		if got := z.offsetAt(wall(2026, 7, 1, 0, 0, 0)); got != time.Hour {
			t.Fatalf("2026 summer with broken rules = %v, want 1h", got)
		}
	}
	if z.obs[0].rule != nil || z.obs[1].rule != nil {
		t.Fatal("broken rules kept")
	}
	// Years cached while the rules worked are not reused.
	if got := z.offsetAt(wall(2015, 7, 1, 0, 0, 0)); got != time.Hour {
		t.Fatalf("2015 summer after break = %v, want 1h", got)
	}
}

func TestOffsetAtConcurrent(t *testing.T) {
	z := mustVTZ(t, pragueTZ)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for j := range 50 {
				y := 1950 + (i*50+j)%200
				got := z.offsetAt(wall(y, 7, 1, 12, 0, 0))
				if got != 2*time.Hour && y >= 1970 {
					t.Errorf("year %d: %v", y, got)
				}
			}
		})
	}
	wg.Wait()
}

func TestOffsetAtCount(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\n"+
		"BEGIN:STANDARD\r\nDTSTART:20001029T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\n"+
		"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU;COUNT=3\r\nEND:STANDARD\r\n"+
		"BEGIN:DAYLIGHT\r\nDTSTART:20000326T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n"+
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;COUNT=4\r\nEND:DAYLIGHT\r\n"+
		"END:VTIMEZONE\r\n")
	tests := []struct {
		at   time.Time
		want time.Duration
	}{
		{wall(2002, 12, 1, 0, 0, 0), time.Hour},
		{wall(2003, 7, 1, 0, 0, 0), 2 * time.Hour},
		{wall(2003, 12, 1, 0, 0, 0), 2 * time.Hour},
		{wall(2500, 12, 1, 0, 0, 0), 2 * time.Hour},
	}
	for _, tt := range tests {
		if got := z.offsetAt(tt.at); got != tt.want {
			t.Errorf("offsetAt(%s) = %v, want %v", tt.at.Format(time.DateTime), got, tt.want)
		}
	}
}

func TestParseVTimezoneDropsEmptyRule(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\n"+
		"TZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nRRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30\r\n"+
		"END:STANDARD\r\nEND:VTIMEZONE\r\n")
	if z.obs[0].rule != nil {
		t.Fatal("rule with no instances was kept")
	}
	if got := z.offsetAt(wall(2026, 1, 1, 0, 0, 0)); got != 2*time.Hour {
		t.Fatalf("offsetAt = %v, want 2h", got)
	}
}

// Starting a rule later, on a whole number of periods, must not change
// which instances fall in a window.
func TestShiftRule(t *testing.T) {
	rules := []string{
		"FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU",
		"FREQ=YEARLY;INTERVAL=3;BYMONTH=3;BYDAY=2SU",
		"FREQ=YEARLY",
		"FREQ=YEARLY;BYMONTH=8",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
		"FREQ=YEARLY;BYWEEKNO=53;BYDAY=MO;WKST=SU",
		"FREQ=YEARLY;BYYEARDAY=-1",
		"FREQ=YEARLY;BYMONTH=3,10;BYDAY=SU;BYSETPOS=-1",
		"FREQ=MONTHLY",
		"FREQ=MONTHLY;INTERVAL=7;BYDAY=1FR",
		"FREQ=MONTHLY;BYMONTHDAY=31",
		"FREQ=YEARLY;UNTIL=24000101T000000Z",
	}
	start := wall(1601, 7, 31, 2, 30, 15)
	for _, raw := range rules {
		o := observance{start: start, to: time.Hour}
		opt, err := o.parseRule(raw)
		if err != nil || opt == nil {
			t.Fatalf("%s: %v", raw, err)
		}
		for _, y := range []int{2026, 2401, 4000, 9998} {
			lo := wall(y-1, 1, 1, 0, 0, 0)
			hi := wall(y+2, 1, 1, 0, 0, 0)
			collect := func(opt rrule.ROption) (prior time.Time, in []time.Time) {
				if _, err := expandRule(opt, hi, func(t time.Time) bool {
					if t.Before(lo) {
						prior = t
					} else {
						in = append(in, t)
					}
					return true
				}); err != nil {
					t.Fatal(err)
				}
				return prior, in
			}
			s := shiftRule(*opt, hi)
			if !s.Dtstart.After(opt.Dtstart) && y == 9998 {
				t.Errorf("%s %d: start not moved: %s", raw, y, s.Dtstart)
			}
			wantPrior, want := collect(*opt)
			gotPrior, got := collect(s)
			if !gotPrior.Equal(wantPrior) || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s %d: shifted %s %v, want %s %v", raw, y, gotPrior, got, wantPrior, want)
			}
		}
	}
}

// TestOffsetAtSecondSunday covers the old way of writing "second Sunday":
// a Sunday on one of the days 8 to 14.
func TestOffsetAtSecondSunday(t *testing.T) {
	for _, rule := range []string{
		"FREQ=MONTHLY;BYMONTH=3;BYDAY=SU;BYMONTHDAY=8,9,10,11,12,13,14",
		"FREQ=YEARLY;BYMONTH=3;BYDAY=SU;BYMONTHDAY=8,9,10,11,12,13,14",
	} {
		t.Run(rule, func(t *testing.T) {
			z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n"+
				"BEGIN:STANDARD\r\nDTSTART:19701101T020000\r\n"+
				"TZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n"+
				"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n"+
				"BEGIN:DAYLIGHT\r\nDTSTART:19700308T020000\r\n"+
				"TZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n"+
				"RRULE:"+rule+"\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n")
			for _, tt := range []struct {
				at   time.Time
				want time.Duration
			}{
				{wall(2026, 3, 8, 1, 59, 59), -5 * time.Hour},
				{wall(2026, 3, 8, 3, 0, 0), -4 * time.Hour},
				{wall(2026, 3, 1, 12, 0, 0), -5 * time.Hour},
				{wall(2027, 3, 13, 12, 0, 0), -5 * time.Hour},
				{wall(2027, 3, 14, 3, 0, 0), -4 * time.Hour},
				{wall(2027, 7, 1, 0, 0, 0), -4 * time.Hour},
				{wall(2027, 11, 7, 3, 0, 0), -5 * time.Hour},
			} {
				if got := z.offsetAt(tt.at); got != tt.want {
					t.Errorf("offsetAt(%s) = %v, want %v", tt.at.Format(time.DateTime), got, tt.want)
				}
			}
		})
	}
}

// TestOffsetAtMonthlySecondSunday uses the rule without BYMONTH, so it
// fires every month; a first-Sunday rule switches back each month.
func TestOffsetAtMonthlySecondSunday(t *testing.T) {
	z := mustVTZ(t, "BEGIN:VTIMEZONE\r\nTZID:X\r\n"+
		"BEGIN:STANDARD\r\nDTSTART:19700104T020000\r\n"+
		"TZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n"+
		"RRULE:FREQ=MONTHLY;BYDAY=SU;BYMONTHDAY=1,2,3,4,5,6,7\r\nEND:STANDARD\r\n"+
		"BEGIN:DAYLIGHT\r\nDTSTART:19700111T020000\r\n"+
		"TZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n"+
		"RRULE:FREQ=MONTHLY;BYDAY=SU;BYMONTHDAY=8,9,10,11,12,13,14\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n")
	for _, tt := range []struct {
		at   time.Time
		want time.Duration
	}{
		{wall(2026, 4, 5, 3, 0, 0), -5 * time.Hour},
		{wall(2026, 4, 12, 1, 59, 59), -5 * time.Hour},
		{wall(2026, 4, 12, 3, 0, 0), -4 * time.Hour},
		{wall(2026, 4, 30, 12, 0, 0), -4 * time.Hour},
		{wall(2026, 5, 3, 3, 0, 0), -5 * time.Hour},
		{wall(2026, 5, 10, 3, 0, 0), -4 * time.Hour},
	} {
		if got := z.offsetAt(tt.at); got != tt.want {
			t.Errorf("offsetAt(%s) = %v, want %v", tt.at.Format(time.DateTime), got, tt.want)
		}
	}
}

// TestOnsetsMatchFullExpansion checks that looking back from a window in
// growing spans finds the same onsets as expanding a rule from DTSTART.
func TestOnsetsMatchFullExpansion(t *testing.T) {
	rules := []string{
		"FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU",
		"FREQ=YEARLY;INTERVAL=3;BYMONTH=3;BYDAY=2SU",
		"FREQ=YEARLY;INTERVAL=150;BYMONTH=3",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYDAY=MO",
		"FREQ=YEARLY;BYWEEKNO=53;BYDAY=MO;WKST=SU",
		"FREQ=MONTHLY;INTERVAL=7;BYDAY=1FR",
		"FREQ=MONTHLY;BYDAY=SU;BYMONTHDAY=8,9,10,11,12,13,14",
		"FREQ=YEARLY;UNTIL=19000101T000000Z",
		"FREQ=YEARLY;BYMONTH=3;COUNT=5",
	}
	start := wall(1601, 7, 31, 2, 30, 15)
	for _, raw := range rules {
		o := observance{start: start, to: time.Hour}
		opt, err := o.parseRule(raw)
		if err != nil || opt == nil {
			t.Fatalf("%s: %v", raw, err)
		}
		o.rule = opt
		z := &vtz{obs: []observance{o}, blocks: map[int][]onset{}, budget: 1 << 40}
		for _, y := range []int{1600, 1602, 1610, 1899, 1905, 2026, 2401, 4000, 9990} {
			got := z.onsets(y)
			first := y / blockYears * blockYears
			lo := wall(first-1, 1, 1, 0, 0, 0)
			hi := wall(first+blockYears+1, 1, 1, 0, 0, 0)
			var want []time.Time
			var prior time.Time
			if _, err := expandRule(*opt, hi, func(t time.Time) bool {
				if t.Before(lo) {
					prior = t
				} else {
					want = append(want, t)
				}
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if !prior.IsZero() {
				want = append([]time.Time{prior}, want...)
			}
			// DTSTART is always an onset.
			if !start.Before(lo) && start.Before(hi) && (len(want) == 0 || !want[0].Equal(start)) {
				want = append([]time.Time{start}, want...)
			} else if start.Before(lo) && prior.IsZero() {
				want = append([]time.Time{start}, want...)
			}
			var walls []time.Time
			for _, on := range got {
				walls = append(walls, on.wall)
			}
			if fmt.Sprint(walls) != fmt.Sprint(want) {
				t.Errorf("%s %d:\n got %v\nwant %v", raw, y, walls, want)
			}
		}
	}
}
