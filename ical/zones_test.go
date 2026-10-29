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
)

// fixedTZ is a VTIMEZONE with one constant offset.
func fixedTZ(tzid, offset string) string {
	return "BEGIN:VTIMEZONE\r\nTZID:" + tzid + "\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:19700101T000000\r\n" +
		"TZOFFSETFROM:" + offset + "\r\nTZOFFSETTO:" + offset + "\r\n" +
		"END:STANDARD\r\nEND:VTIMEZONE\r\n"
}

// mustCal parses the blocks wrapped in a minimal VCALENDAR.
func mustCal(t *testing.T, blocks ...string) *Calendar {
	t.Helper()
	cal, err := Parse([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//y//EN\r\n" +
		strings.Join(blocks, "") + "END:VCALENDAR\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// needZone loads an IANA zone, skipping the test when the system has no
// time zone database.
func needZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no time zone database available: %v", err)
	}
	return loc
}

func local(tzid string, w time.Time) Value {
	return Value{Wall: w, TZID: tzid}
}

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func checkInstant(t *testing.T, r *Resolver, v Value, floating *time.Location, want time.Time) {
	t.Helper()
	got := r.Instant(v, floating)
	if !got.Equal(want) {
		t.Errorf("Instant(%+v) = %s, want %s", v, got.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
	}
}

func TestInstantUTC(t *testing.T) {
	r := NewResolver(nil)
	v := Value{Wall: utc(2026, 7, 15, 12, 0), UTC: true}
	checkInstant(t, r, v, time.FixedZone("x", 5*3600), utc(2026, 7, 15, 12, 0))
}

func TestInstantFloating(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	r := NewResolver(nil)
	v := Value{Wall: utc(2026, 7, 15, 12, 0)}
	checkInstant(t, r, v, prague, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, v, nil, utc(2026, 7, 15, 12, 0))
	checkInstant(t, r, v, time.UTC, utc(2026, 7, 15, 12, 0))
}

func TestInstantDate(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	r := NewResolver(nil)
	v := Value{Wall: utc(2026, 1, 15, 0, 0), Date: true}
	checkInstant(t, r, v, prague, utc(2026, 1, 14, 23, 0))
	checkInstant(t, r, v, nil, utc(2026, 1, 15, 0, 0))
}

func TestInstantIANA(t *testing.T) {
	needZone(t, "Europe/Prague")
	r := NewResolver(mustCal(t))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 1, 15, 12, 0)), nil, utc(2026, 1, 15, 11, 0))
}

func TestInstantEmbeddedWins(t *testing.T) {
	needZone(t, "Europe/Prague")
	r := NewResolver(mustCal(t, fixedTZ("Europe/Prague", "+0530")))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 6, 30))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 1, 15, 12, 0)), nil, utc(2026, 1, 15, 6, 30))
}

func TestInstantEmbeddedRules(t *testing.T) {
	r := NewResolver(mustCal(t, strings.Replace(pragueTZ, "Europe/Prague", "Custom/Prague", 1)))
	checkInstant(t, r, local("Custom/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local("Custom/Prague", utc(2026, 1, 15, 12, 0)), nil, utc(2026, 1, 15, 11, 0))
	// The gap takes the offset from before the change, the overlap the
	// earlier of its two instants.
	checkInstant(t, r, local("Custom/Prague", utc(2026, 3, 29, 2, 30)), nil, utc(2026, 3, 29, 1, 30))
	checkInstant(t, r, local("Custom/Prague", utc(2026, 10, 25, 2, 30)), nil, utc(2026, 10, 25, 0, 30))
}

func TestInstantWindows(t *testing.T) {
	needZone(t, "Europe/Berlin")
	const win = "W. Europe Standard Time"
	r := NewResolver(nil)
	checkInstant(t, r, local(win, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local(win, utc(2026, 1, 15, 12, 0)), nil, utc(2026, 1, 15, 11, 0))

	r = NewResolver(mustCal(t, fixedTZ(win, "+0300")))
	checkInstant(t, r, local(win, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
}

func TestInstantStripsPrefix(t *testing.T) {
	needZone(t, "Europe/Berlin")
	r := NewResolver(nil)
	for _, tzid := range []string{
		"/freeassociation.sourceforge.net/Europe/Berlin",
		"/freeassociation.sourceforge.net/Tzfile/Europe/Berlin",
		"/mozilla.org/20050126_1/Europe/Berlin",
	} {
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	}
}

func TestInstantUnknownIsUTC(t *testing.T) {
	r := NewResolver(nil)
	for _, tzid := range []string{
		"Custom/Unknown",
		"Local",
		"../../etc/passwd",
		"/etc/passwd",
		"Europe/../Prague",
		"Europe\\Prague",
		"Europe/Prague\x00",
		"Europe/Prague/" + strings.Repeat("x", 300),
		"/",
		"//",
	} {
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), time.FixedZone("f", 3600), utc(2026, 7, 15, 12, 0))
	}
}

func TestInstantGoZoneGapAndOverlap(t *testing.T) {
	needZone(t, "Europe/Prague")
	needZone(t, "America/New_York")
	r := NewResolver(nil)
	tests := []struct {
		tzid string
		wall time.Time
		want time.Time
	}{
		// A skipped wall time uses the offset from before the change.
		{"Europe/Prague", utc(2026, 3, 29, 2, 30), utc(2026, 3, 29, 1, 30)},
		{"America/New_York", utc(2026, 3, 8, 2, 30), utc(2026, 3, 8, 7, 30)},
		// A repeated wall time is the earlier instant, still in summer time.
		{"Europe/Prague", utc(2026, 10, 25, 2, 30), utc(2026, 10, 25, 0, 30)},
		{"America/New_York", utc(2026, 11, 1, 1, 30), utc(2026, 11, 1, 5, 30)},
		// Either side of the changes.
		{"Europe/Prague", utc(2026, 3, 29, 1, 59), utc(2026, 3, 29, 0, 59)},
		{"Europe/Prague", utc(2026, 3, 29, 3, 0), utc(2026, 3, 29, 1, 0)},
		{"Europe/Prague", utc(2026, 10, 25, 1, 59), utc(2026, 10, 24, 23, 59)},
		{"Europe/Prague", utc(2026, 10, 25, 3, 0), utc(2026, 10, 25, 2, 0)},
	}
	for _, tt := range tests {
		checkInstant(t, r, local(tt.tzid, tt.wall), nil, tt.want)
	}
	// Floating values follow the same rules.
	prague := needZone(t, "Europe/Prague")
	checkInstant(t, r, Value{Wall: utc(2026, 3, 29, 2, 30)}, prague, utc(2026, 3, 29, 1, 30))
	checkInstant(t, r, Value{Wall: utc(2026, 10, 25, 2, 30)}, prague, utc(2026, 10, 25, 0, 30))
}

func TestInstantNilResolver(t *testing.T) {
	var r *Resolver
	checkInstant(t, r, local("Custom/Unknown", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
	if got := r.WallOf(utc(2026, 7, 15, 12, 0), "Custom/Unknown", nil); !got.Equal(utc(2026, 7, 15, 12, 0)) {
		t.Errorf("WallOf on nil resolver = %s", got)
	}
	if _, err := time.LoadLocation("Europe/Prague"); err == nil {
		checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	}
}

func TestInstantOddValues(t *testing.T) {
	r := NewResolver(mustCal(t, pragueTZ))
	for _, v := range []Value{
		{},
		{TZID: "Europe/Prague"},
		{Date: true},
		local("Europe/Prague", utc(9999, 12, 31, 23, 59)),
		local("Europe/Prague", utc(1, 1, 1, 0, 0)),
	} {
		_ = r.Instant(v, nil)
		_ = r.WallOf(v.Wall, v.TZID, nil)
	}
}

func TestNewResolverIgnoresBrokenVTimezone(t *testing.T) {
	needZone(t, "Europe/Prague")
	broken := "BEGIN:VTIMEZONE\r\nTZID:Europe/Prague\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0530\r\n" +
		"END:STANDARD\r\nEND:VTIMEZONE\r\n"
	r := NewResolver(mustCal(t, broken))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))

	// A later VTIMEZONE with the same TZID that does parse is used.
	r = NewResolver(mustCal(t, broken, fixedTZ("Europe/Prague", "+0300")))
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
}

func TestNewResolverFirstVTimezoneWins(t *testing.T) {
	r := NewResolver(mustCal(t, fixedTZ("A", "+0300"), fixedTZ("A", "+0500")))
	checkInstant(t, r, local("A", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
}

func TestNewResolverCapsVTimezones(t *testing.T) {
	var blocks []string
	for i := range maxVTimezones + 1 {
		blocks = append(blocks, fixedTZ(fmt.Sprintf("Z%d", i), "+0300"))
	}
	r := NewResolver(mustCal(t, blocks...))
	checkInstant(t, r, local("Z0", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
	last := fmt.Sprintf("Z%d", maxVTimezones-1)
	checkInstant(t, r, local(last, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
	extra := fmt.Sprintf("Z%d", maxVTimezones)
	checkInstant(t, r, local(extra, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
}

func cached(r *Resolver, tzid string) (zone, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	z, ok := r.cache[tzid]
	return z, ok
}

func TestResolverCache(t *testing.T) {
	r := NewResolver(nil)
	checkInstant(t, r, local("Custom/Unknown", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
	if z, ok := cached(r, "Custom/Unknown"); !ok || z.loc != nil || z.vtz != nil {
		t.Errorf("unknown zone not cached as UTC: %+v, %v", z, ok)
	}
	for i := range maxCachedZones + 50 {
		tzid := fmt.Sprintf("Custom/Unknown%d", i)
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
		r.mu.Lock()
		n := len(r.cache)
		r.mu.Unlock()
		if n > maxCachedZones {
			t.Fatalf("cache holds %d zones, over %d", n, maxCachedZones)
		}
	}
	// A TZID first seen once the cache filled up is still cached.
	last := fmt.Sprintf("Custom/Unknown%d", maxCachedZones+49)
	if _, ok := cached(r, last); !ok {
		t.Errorf("%s not cached after the cache filled up", last)
	}
}

func TestResolverCacheAfterFlood(t *testing.T) {
	needZone(t, "Europe/Prague")
	r := NewResolver(nil)
	for i := range maxCachedZones {
		r.Instant(local(fmt.Sprintf("Junk/%d", i), utc(2026, 7, 15, 12, 0)), nil)
	}
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	if z, ok := cached(r, "Europe/Prague"); !ok || z.loc == nil {
		t.Errorf("Europe/Prague not cached after a flood of TZIDs: %+v, %v", z, ok)
	}
}

func TestInstantTrimsAndFolds(t *testing.T) {
	needZone(t, "Europe/Prague")
	needZone(t, "Europe/Berlin")
	r := NewResolver(nil)
	for _, tzid := range []string{" Europe/Prague ", "\tEurope/Prague", "Europe/Prague\n", "Europe/Prague\r\n",
		"w. europe standard time",
		"W. EUROPE STANDARD TIME", " W. Europe Standard Time "} {
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	}
	r = NewResolver(mustCal(t, fixedTZ("Custom/Fixed", "+0300")))
	checkInstant(t, r, local(" Custom/Fixed ", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
	checkInstant(t, r, local("Custom/Fixed\n", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))
}

func TestInstantDeepTZID(t *testing.T) {
	needZone(t, "Europe/Prague")
	r := NewResolver(nil)
	for _, tzid := range []string{
		strings.Repeat("a/", 20) + "Europe/Prague",
		"/" + strings.Repeat("x.y/", 7) + "Europe/Prague",
	} {
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	}
	// Only the last few elements are tried.
	tzid := "Europe/Prague/" + strings.Repeat("a/", maxTZIDDepth) + "x"
	checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
}

func TestResolverConcurrent(t *testing.T) {
	r := NewResolver(mustCal(t, strings.Replace(pragueTZ, "Europe/Prague", "Custom/Prague", 1)))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				tzid := []string{"Custom/Prague", "Europe/Prague", "Unknown", fmt.Sprintf("U%d/%d", g, i)}[i%4]
				in := r.Instant(local(tzid, utc(2026, 7, 15, 12, 0)), nil)
				_ = r.WallOf(in, tzid, nil)
			}
		})
	}
	wg.Wait()
}

// walls lists wall times through the year, every ten minutes on the days
// the clocks change and at noon on some others.
func walls() []time.Time {
	var out []time.Time
	for _, day := range []time.Time{utc(2026, 3, 29, 0, 0), utc(2026, 10, 25, 0, 0)} {
		for m := -24 * 60; m < 48*60; m += 10 {
			out = append(out, day.Add(time.Duration(m)*time.Minute))
		}
	}
	for _, m := range []time.Month{1, 4, 7, 11} {
		out = append(out, utc(2026, m, 15, 12, 0))
	}
	return out
}

func TestWallOfRoundTrip(t *testing.T) {
	needZone(t, "Europe/Prague")
	r := NewResolver(mustCal(t, strings.Replace(pragueTZ, "Europe/Prague", "Custom/Prague", 1)))
	for _, tzid := range []string{"Custom/Prague", "Europe/Prague"} {
		for _, w := range walls() {
			in := r.Instant(local(tzid, w), nil)
			got := r.WallOf(in, tzid, nil)
			want := w
			if w.Month() == 3 && w.Day() == 29 && w.Hour() == 2 {
				// Skipped: the wall time reads an hour later.
				want = w.Add(time.Hour)
			}
			if !got.Equal(want) || got.Location() != time.UTC {
				t.Errorf("%s: WallOf(Instant(%s)) = %s, want %s", tzid, w, got, want)
			}
		}
	}
}

func TestWallOfMatchesGo(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	r := NewResolver(mustCal(t, strings.Replace(pragueTZ, "Europe/Prague", "Custom/Prague", 1)))
	for _, day := range []time.Time{utc(2026, 3, 28, 0, 0), utc(2026, 10, 24, 0, 0), utc(2026, 7, 1, 0, 0)} {
		for m := 0; m < 72*60; m += 10 {
			in := day.Add(time.Duration(m) * time.Minute)
			l := in.In(prague)
			want := time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
			for _, tzid := range []string{"Custom/Prague", "Europe/Prague"} {
				if got := r.WallOf(in, tzid, nil); !got.Equal(want) {
					t.Errorf("%s: WallOf(%s) = %s, want %s", tzid, in, got, want)
				}
			}
			if got := r.WallOf(in, "", prague); !got.Equal(want) {
				t.Errorf("floating: WallOf(%s) = %s, want %s", in, got, want)
			}
		}
	}
	// The second 02:30 on the day clocks go back.
	if got := r.WallOf(utc(2026, 10, 25, 1, 30), "Custom/Prague", nil); !got.Equal(utc(2026, 10, 25, 2, 30)) {
		t.Errorf("WallOf second 02:30 = %s", got)
	}
}

func TestWallOfFallbacks(t *testing.T) {
	r := NewResolver(nil)
	in := time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("x", 7200))
	if got := r.WallOf(in, "", nil); !got.Equal(utc(2026, 7, 15, 10, 0)) || got.Location() != time.UTC {
		t.Errorf("WallOf floating nil = %s", got)
	}
	if got := r.WallOf(in, "Custom/Unknown", nil); !got.Equal(utc(2026, 7, 15, 10, 0)) {
		t.Errorf("WallOf unknown = %s", got)
	}
	if got := r.WallOf(in, "", time.FixedZone("y", 3600)); !got.Equal(utc(2026, 7, 15, 11, 0)) {
		t.Errorf("WallOf floating fixed = %s", got)
	}
}

const newYorkTZ = "BEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:20071104T020000\r\nTZOFFSETFROM:-0400\r\nTZOFFSETTO:-0500\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:20070311T020000\r\nTZOFFSETFROM:-0500\r\nTZOFFSETTO:-0400\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

const sydneyTZ = "BEGIN:VTIMEZONE\r\nTZID:Australia/Sydney\r\n" +
	"BEGIN:STANDARD\r\nDTSTART:20080406T030000\r\nTZOFFSETFROM:+1100\r\nTZOFFSETTO:+1000\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=4;BYDAY=1SU\r\nEND:STANDARD\r\n" +
	"BEGIN:DAYLIGHT\r\nDTSTART:20081005T020000\r\nTZOFFSETFROM:+1000\r\nTZOFFSETTO:+1100\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=1SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n"

// TestEmbeddedZonesMatchGo compares real zones written as VTIMEZONEs with
// the zone database, wall times in gaps and overlaps included.
func TestEmbeddedZonesMatchGo(t *testing.T) {
	for _, z := range []struct {
		name, block string
		from        int
	}{
		{"Europe/Prague", pragueTZ, 1997},
		{"America/New_York", newYorkTZ, 2008},
		{"Australia/Sydney", sydneyTZ, 2009},
	} {
		loc := needZone(t, z.name)
		r := NewResolver(mustCal(t, z.block))
		if r.embedded[z.name] == nil {
			t.Fatalf("%s: VTIMEZONE not embedded", z.name)
		}
		for w := utc(z.from, 1, 1, 0, 0); w.Year() < 2100; w = w.Add(97*time.Hour + 13*time.Minute) {
			if got, want := r.Instant(local(z.name, w), nil), locInstant(w, loc); !got.Equal(want) {
				t.Fatalf("%s: Instant(%s) = %s, want %s", z.name, w, got, want)
			}
		}
		// Every transition, a minute either side and inside the hour.
		for y := z.from; y < 2100; y++ {
			for _, m := range []time.Month{3, 4, 10, 11} {
				for d := 1; d <= 31; d++ {
					for _, mi := range []int{1*60 + 59, 2*60 + 30, 3 * 60} {
						w := utc(y, m, d, mi/60, mi%60)
						if got, want := r.Instant(local(z.name, w), nil), locInstant(w, loc); !got.Equal(want) {
							t.Fatalf("%s: Instant(%s) = %s, want %s", z.name, w, got, want)
						}
						inst := w.Add(-3 * time.Hour)
						if got, want := r.WallOf(inst, z.name, nil), wallFields(inst.In(loc)); !got.Equal(want) {
							t.Fatalf("%s: WallOf(%s) = %s, want %s", z.name, inst, got, want)
						}
					}
				}
			}
		}
	}
}

// ruleZones returns zones VTIMEZONEs named Z0, Z1, ... with obs
// observances each, all with the given RRULE line.
func ruleZones(zones, obs int, rule string) string {
	var b strings.Builder
	for z := range zones {
		fmt.Fprintf(&b, "BEGIN:VTIMEZONE\r\nTZID:Z%d\r\n", z)
		for range obs {
			b.WriteString("BEGIN:STANDARD\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
				"DTSTART:00010101T020000\r\n" + rule + "END:STANDARD\r\n")
		}
		b.WriteString("END:VTIMEZONE\r\n")
	}
	return b.String()
}

// stepsOf returns how many instances the rule library produced during fn.
func stepsOf(fn func()) int64 {
	before := ruleSteps.Load()
	fn()
	return ruleSteps.Load() - before
}

func TestNewResolverRuleBudget(t *testing.T) {
	yearly := "RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\n"
	r := NewResolver(mustCal(t, ruleZones(60, 1, yearly)))
	if len(r.embedded) != maxRuleObservances {
		t.Errorf("embedded %d zones, want %d", len(r.embedded), maxRuleObservances)
	}
	// A zone over the budget is ignored; a later one that fits is not.
	r = NewResolver(mustCal(t, ruleZones(1, maxRuleObservances+1, yearly),
		strings.ReplaceAll(ruleZones(1, 2, yearly), "Z0", "Small")))
	if r.embedded["Z0"] != nil || r.embedded["Small"] == nil {
		t.Errorf("embedded = %v", r.embedded)
	}
	// Observances without a rule cost nothing.
	r = NewResolver(mustCal(t, ruleZones(1, maxObservances, ""), strings.ReplaceAll(ruleZones(1, 2, yearly), "Z0", "Z1")))
	if r.embedded["Z0"] == nil || r.embedded["Z1"] == nil {
		t.Errorf("embedded = %v", r.embedded)
	}
}

// The work of a resolver over hostile VTIMEZONEs is counted in rule
// instances, not timed. The limits are about ten times what the code
// needs; before the per-calendar budget and the cap on COUNT, the first
// case needed hundreds of millions.
func TestResolverWorkIsBounded(t *testing.T) {
	event := vevent("DTSTART;TZID=Z0:20260105T090000", "DURATION:PT1H", "RRULE:FREQ=YEARLY")
	century := eventsIn(utc(2026, 1, 1, 0, 0), time.Time{})
	tests := []struct {
		name            string
		zones           string
		parse, matching int64
	}{
		{"huge COUNT everywhere", ruleZones(50, 100, "RRULE:FREQ=MONTHLY;BYMONTHDAY=1,2;COUNT=1000000000\r\n"), 1000, 2000},
		{"largest COUNT in 50 zones", ruleZones(50, 1, "RRULE:FREQ=MONTHLY;BYMONTHDAY=1,2;COUNT=400\r\n"), 250_000, 250_000},
		{"dense rules in one zone", ruleZones(1, 20, "RRULE:FREQ=MONTHLY;BYDAY=1SU,2MO\r\n"), 10_000, 1_600_000},
		{"sparse rules in one zone", ruleZones(1, 20, "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29;BYDAY=MO\r\n"), 400, 25_000},
		{"real zone", pragueTZ, 100, 9000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zones := tt.zones
			if tt.name == "real zone" {
				zones = strings.ReplaceAll(zones, "Europe/Prague", "Z0")
			}
			cal := mustCal(t, zones, event)
			if n := stepsOf(func() { NewResolver(cal) }); n > tt.parse {
				t.Errorf("NewResolver: %d instances, want at most %d", n, tt.parse)
			}
			var ok bool
			n := stepsOf(func() {
				var err error
				if ok, err = cal.Matches(century, nil); err != nil {
					t.Fatal(err)
				}
			})
			if !ok || n > tt.matching {
				t.Errorf("Matches over a century = %v with %d instances, want true with at most %d", ok, n, tt.matching)
			}
		})
	}
}

func TestParseVTimezoneCountLimit(t *testing.T) {
	rule := func(n int) string {
		return standardWith(fmt.Sprintf("RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;COUNT=%d\r\n", n))
	}
	if _, err := vtzFrom(t, rule(maxRuleCount)); err != nil {
		t.Errorf("COUNT=%d: %v", maxRuleCount, err)
	}
	if _, err := vtzFrom(t, rule(maxRuleCount+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("COUNT=%d: err = %v, want ErrInvalid", maxRuleCount+1, err)
	}
}

// TestResolverZoneLookupBudget feeds a resolver many distinct TZIDs that
// look like zone names but are not. Database lookups are counted, not
// timed.
func TestResolverZoneLookupBudget(t *testing.T) {
	needZone(t, "Europe/Prague")
	// Make sure the process has loaded Europe/Prague once.
	checkInstant(t, NewResolver(nil), local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))

	r := NewResolver(mustCal(t, fixedTZ("Custom/Fixed", "+0300")))
	before := zoneLookups.Load()
	for i := range 20000 {
		tzid := fmt.Sprintf("Nope/%d/Europe/Berlin%d", i, i)
		checkInstant(t, r, local(tzid, utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 12, 0))
	}
	if n := zoneLookups.Load() - before; n > maxZoneMisses {
		t.Errorf("%d database lookups, want at most %d", n, maxZoneMisses)
	}
	// Zones known without the database still resolve.
	checkInstant(t, r, local("Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local("/x/y/Europe/Prague", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local("W. Europe Standard Time", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 10, 0))
	checkInstant(t, r, local("Custom/Fixed", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 9, 0))

	// The targets of the Windows table are read even with the budget
	// spent, whether or not the process has loaded them.
	needZone(t, "Asia/Tokyo")
	loaded.Lock()
	delete(loaded.m, "Asia/Tokyo")
	loaded.Unlock()
	spent := NewResolver(nil)
	spent.misses.Store(maxZoneMisses)
	checkInstant(t, spent, local("Tokyo Standard Time", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 3, 0))

	// A fresh resolver has a fresh budget.
	checkInstant(t, NewResolver(nil), local("Europe/Prague", utc(2026, 1, 15, 12, 0)), nil, utc(2026, 1, 15, 11, 0))
}

// TestZoneDatabaseCache checks that a zone is read from the database once
// per process, however many resolvers ask for it.
func TestZoneDatabaseCache(t *testing.T) {
	needZone(t, "Asia/Kathmandu")
	before := zoneLookups.Load()
	for range 10 {
		checkInstant(t, NewResolver(nil), local("Asia/Kathmandu", utc(2026, 7, 15, 12, 0)), nil, utc(2026, 7, 15, 6, 15))
	}
	if n := zoneLookups.Load() - before; n > 1 {
		t.Errorf("%d database lookups for one zone, want at most 1", n)
	}
}

// denseZone is a VTIMEZONE for Europe/Prague with n rules of 24 onsets a
// year each: costly to evaluate, and wrong, so a fallback to the zone
// database shows in the results.
func denseZone(n int) string {
	return strings.ReplaceAll(ruleZones(1, n, "RRULE:FREQ=MONTHLY;BYDAY=1SU,2MO\r\n"),
		"TZID:Z0", "TZID:Europe/Prague")
}

// cyclingWalls returns n wall times that visit 100 blocks of years in turn,
// more than a zone caches.
func cyclingWalls(n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = utc(1000+blockYears*(i%100), time.Month(1+i/100%12), 1+i/1200, 9, 0)
	}
	return out
}

// TestZoneWorkBudget feeds a costly VTIMEZONE values that keep missing its
// cache. The work is counted in rule instances; once the zone's budget is
// spent, its TZID resolves through the zone database.
func TestZoneWorkBudget(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	walls := cyclingWalls(10000)
	var exdates []string
	var overrides strings.Builder
	for i, w := range walls {
		exdates = append(exdates, w.Format("20060102T150405"))
		if i > 0 {
			rid := utc(2000, 1, 1, 9, 0).AddDate(0, 0, i)
			fmt.Fprintf(&overrides, "BEGIN:VEVENT\r\nUID:e1\r\nRECURRENCE-ID;TZID=Europe/Prague:%s\r\n"+
				"DTSTART;TZID=Europe/Prague:%s\r\nDURATION:PT1H\r\nEND:VEVENT\r\n",
				rid.Format("20060102T150405"), w.Format("20060102T150405"))
		}
	}
	tests := map[string][]string{
		"EXDATE": {vevent("DTSTART;TZID=Europe/Prague:20000601T090000", "DURATION:PT1H", "RRULE:FREQ=DAILY;COUNT=5",
			"EXDATE;TZID=Europe/Prague:"+strings.Join(exdates, ","))},
		"overrides": {vevent("DTSTART;TZID=Europe/Prague:20000101T090000", "DURATION:PT1H", "RRULE:FREQ=DAILY;COUNT=10000"),
			overrides.String()},
	}
	for name, events := range tests {
		t.Run(name, func(t *testing.T) {
			cal := mustCal(t, append([]string{denseZone(maxRuleObservances)}, events...)...)
			r := NewResolver(cal)
			z := r.embedded["Europe/Prague"]
			if z == nil {
				t.Fatal("zone not embedded")
			}
			if z.budget != ruleWork*maxRuleObservances {
				t.Fatalf("budget %d, want %d", z.budget, ruleWork*maxRuleObservances)
			}
			// One case spends the whole budget; the other, a tenth of it
			// to keep the test quick under the race detector.
			if name == "overrides" {
				z.budget /= 10
			}
			steps := stepsOf(func() {
				if _, err := cal.Occurrences(r, nil, Range{Start: at(1500, 1, 1, 0, 0), End: at(1500, 1, 2, 0, 0)}); err != nil {
					t.Fatal(err)
				}
			})
			if !z.dropped.Load() {
				t.Fatalf("zone kept after %d units of work", z.work)
			}
			if limit := int64(z.budget); steps > limit {
				t.Errorf("%d rule instances, want at most %d", steps, limit)
			}
			for _, w := range walls[:200] {
				checkInstant(t, r, local("Europe/Prague", w), nil, locInstant(w, prague))
			}
		})
	}
}

// TestZoneWorkBudgetRealUse checks that a real zone used over two centuries
// stays far inside its budget and agrees with the zone database.
func TestZoneWorkBudgetRealUse(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	var exdates []string
	for i := range 10000 {
		exdates = append(exdates, utc(1900, 1, 1, 9, 0).AddDate(0, 0, 7*i+i%5).Format("20060102T150405"))
	}
	cal := mustCal(t, pragueTZ, vevent("DTSTART;TZID=Europe/Prague:19000101T090000", "DURATION:PT1H",
		"RRULE:FREQ=WEEKLY", "EXDATE;TZID=Europe/Prague:"+strings.Join(exdates, ",")))
	r := NewResolver(cal)
	z := r.embedded["Europe/Prague"]
	for _, win := range []Range{{Start: at(1990, 1, 1, 0, 0), End: at(2000, 1, 1, 0, 0)}, {Start: at(2000, 1, 1, 0, 0)}} {
		if _, err := cal.Occurrences(r, nil, win); err != nil {
			t.Fatal(err)
		}
	}
	if z.dropped.Load() || z.work > z.budget/10 {
		t.Fatalf("work %d of budget %d, dropped %v", z.work, z.budget, z.dropped.Load())
	}
	// The VTIMEZONE holds the rules in force since 1996.
	for _, raw := range exdates {
		v, err := ParseDateTime(raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if v.Wall.Year() < 1997 {
			continue
		}
		checkInstant(t, r, local("Europe/Prague", v.Wall), nil, locInstant(v.Wall, prague))
	}
	if z.dropped.Load() {
		t.Fatal("zone dropped")
	}
}

// TestZoneWorkBudgetConcurrent spends a zone's budget from many goroutines
// at once; run it with -race. Once dropped, the zone stays dropped.
func TestZoneWorkBudgetConcurrent(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	r := NewResolver(mustCal(t, denseZone(maxRuleObservances)))
	z := r.embedded["Europe/Prague"]
	z.budget /= 10
	walls := cyclingWalls(4000)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i, w := range walls {
				if i%8 != g {
					continue
				}
				dropped := z.dropped.Load()
				got := r.Instant(local("Europe/Prague", w), nil)
				if dropped && !got.Equal(locInstant(w, prague)) {
					t.Errorf("Instant(%s) = %s after the zone was dropped", w, got)
				}
				r.WallOf(got, "Europe/Prague", nil)
			}
		})
	}
	wg.Wait()
	if !z.dropped.Load() {
		t.Fatal("zone kept")
	}
	for _, w := range walls[:100] {
		checkInstant(t, r, local("Europe/Prague", w), nil, locInstant(w, prague))
	}
}

// TestZoneWorkBudgetSwitchingCall checks that the call which spends the
// budget already answers without the zone.
func TestZoneWorkBudgetSwitchingCall(t *testing.T) {
	prague := needZone(t, "Europe/Prague")
	summer := utc(2026, 7, 15, 12, 0)
	r := NewResolver(mustCal(t, denseZone(maxRuleObservances)))
	r.embedded["Europe/Prague"].budget = 1
	checkInstant(t, r, local("Europe/Prague", summer), nil, locInstant(summer, prague))

	r = NewResolver(mustCal(t, denseZone(maxRuleObservances)))
	r.embedded["Europe/Prague"].budget = 1
	inst := locInstant(summer, prague)
	if got := r.WallOf(inst, "Europe/Prague", nil); !got.Equal(summer) {
		t.Errorf("WallOf = %s, want %s", got, summer)
	}
}

// TestZoneBlockCacheHoldsTwoCenturies checks that values spread over 20
// blocks of years, about 160 years, are served from the cache.
func TestZoneBlockCacheHoldsTwoCenturies(t *testing.T) {
	r := NewResolver(mustCal(t, denseZone(10)))
	z := r.embedded["Europe/Prague"]
	for i := range 2000 {
		r.Instant(local("Europe/Prague", utc(2000+blockYears*(i%20), time.Month(1+i%12), 1, 9, 0)), nil)
	}
	if z.dropped.Load() || z.work > z.budget/2 {
		t.Fatalf("work %d of budget %d, dropped %v", z.work, z.budget, z.dropped.Load())
	}
}
