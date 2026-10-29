// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// Limits on the zones one Resolver deals with. TZID values and VTIMEZONEs
// come from untrusted calendar data.
const (
	// maxVTimezones caps the VTIMEZONEs NewResolver parses; later ones
	// are ignored.
	maxVTimezones = 50
	// maxRuleObservances caps the observances with an RRULE across all
	// the VTIMEZONEs of a calendar. Each costs rule expansions for every
	// year looked up. A VTIMEZONE that would go over is ignored.
	maxRuleObservances = 50
	// maxCachedZones bounds the per-Resolver cache. When it is full, it is
	// emptied before the next zone goes in.
	maxCachedZones = 256
	// maxTZIDLen and maxTZIDDepth bound the TZIDs that reach the system
	// zone database, and with them the files read for one TZID. Only the
	// last maxTZIDDepth path elements of a TZID are tried.
	maxTZIDLen   = 256
	maxTZIDDepth = 8
	// maxZoneMisses caps the names one Resolver looks up in the system
	// zone database without finding them. Each costs a file read, and a
	// calendar can hold many distinct TZIDs. Once it is spent, only zones
	// the process has already loaded and Windows zone names resolve;
	// other names read as UTC.
	maxZoneMisses = 64
	// maxLoadedZones bounds the process-wide cache of zones read from the
	// database. A system has about 600 names, and on a case-insensitive
	// file system each spelling of a name is cached on its own.
	maxLoadedZones = 1024
)

// loaded caches the zones read from the system zone database, by name,
// for every Resolver in the process. Only zones that exist go in.
var loaded = struct {
	sync.Mutex
	m map[string]*time.Location
}{m: map[string]*time.Location{}}

// zoneLookups counts the reads of the system zone database, for tests that
// check how much work an operation does.
var zoneLookups atomic.Int64

// zone is a resolved TZID: an embedded VTIMEZONE, a Go location, or UTC
// when both are nil.
type zone struct {
	vtz *vtz
	loc *time.Location
}

// Resolver turns iCalendar DATE and DATE-TIME values into instants, using
// the VTIMEZONEs of one calendar object and the system zone database.
// It is safe for concurrent use. A nil *Resolver knows no VTIMEZONEs and
// has no limit on database lookups.
//
// TZIDs are resolved as Instant describes. Surrounding white space is
// ignored and Windows zone names match in any ASCII case; other names go
// to the system zone database as written, so whether a name in another
// case is found there depends on the platform. One Resolver gives up on
// the database after 64 names it does not find there.
type Resolver struct {
	embedded map[string]*vtz

	mu    sync.Mutex
	cache map[string]zone
	// misses counts database lookups that found nothing.
	misses atomic.Int32
}

// NewResolver parses the VTIMEZONEs at the top level of cal, ignoring any
// that fail to parse, so their TZIDs fall through to the zone database.
// TZIDs are compared after trimming surrounding white space, and when
// several share a TZID, the first that parses wins. cal may be nil.
//
// Only the first 50 VTIMEZONEs are read, and a VTIMEZONE is skipped when
// it would bring the STANDARD and DAYLIGHT blocks with an RRULE, counted
// over all of them, past 50: each such rule is expanded again for every
// stretch of years looked up.
func NewResolver(cal *Calendar) *Resolver {
	r := &Resolver{embedded: map[string]*vtz{}, cache: map[string]zone{}}
	if cal == nil {
		return r
	}
	tzs := cal.Comps("VTIMEZONE")
	if len(tzs) > maxVTimezones {
		tzs = tzs[:maxVTimezones]
	}
	ruled := 0
	for _, c := range tzs {
		p := c.Prop("TZID")
		if p == nil {
			continue
		}
		tzid := strings.TrimSpace(p.Value)
		if _, ok := r.embedded[tzid]; ok || tzid == "" {
			continue
		}
		n := ruleObservances(c)
		if ruled+n > maxRuleObservances {
			continue
		}
		ruled += n
		if z, err := parseVTimezone(c); err == nil {
			r.embedded[tzid] = z
		}
	}
	return r
}

// ruleObservances counts the STANDARD and DAYLIGHT blocks of a VTIMEZONE
// that have an RRULE.
func ruleObservances(c *Component) int {
	n := 0
	for _, ch := range c.Children {
		if (contentline.EqualFoldASCII(ch.Name, "STANDARD") || contentline.EqualFoldASCII(ch.Name, "DAYLIGHT")) &&
			ch.Prop("RRULE") != nil {
			n++
		}
	}
	return n
}

// Instant returns the instant v stands for, in UTC.
//
// A UTC value is its own instant. A DATE is midnight of that day in the
// floating location, and a value without TZID is a wall time there; a
// nil floating location means UTC. A TZID is looked up in the calendar's
// VTIMEZONEs, then as a Windows zone name, ignoring ASCII case, then in
// the zone database as given, then with leading path elements removed (as
// in /example.org/Tzfile/Europe/Berlin). Surrounding white space is
// ignored. A TZID that none of these knows is read as UTC, and so is any
// name the database would have to be read for once the Resolver has spent
// its budget of 64 lookups that found nothing.
//
// A wall time that is skipped when clocks go forward takes the offset from
// before the change. One that occurs twice when clocks go back is the
// earlier of the two instants.
//
// A VTIMEZONE whose rules have cost too much to evaluate in this Resolver,
// more than 20,000 rule instances and periods per rule, is dropped. From
// the call that goes over that on, its TZID resolves as if the VTIMEZONE
// had failed to parse.
func (r *Resolver) Instant(v Value, floating *time.Location) time.Time {
	switch {
	case v.UTC:
		return v.Wall
	case v.Date || v.TZID == "":
		return locInstant(v.Wall, floatingLoc(floating))
	}
	z := r.zone(v.TZID)
	if z.vtz != nil {
		off := z.vtz.offsetAt(v.Wall)
		if !z.vtz.dropped.Load() {
			return v.Wall.Add(-off)
		}
		z = r.zone(v.TZID)
	}
	switch {
	case z.loc != nil:
		return locInstant(v.Wall, z.loc)
	}
	return v.Wall
}

// WallOf returns the wall-clock time of instant in the zone named by tzid,
// or in the floating location when tzid is empty, as fields labelled UTC.
// It resolves tzid as Instant does.
func (r *Resolver) WallOf(instant time.Time, tzid string, floating *time.Location) time.Time {
	u := instant.UTC()
	loc := floatingLoc(floating)
	if tzid != "" {
		z := r.zone(tzid)
		if z.vtz != nil {
			off := z.vtz.offsetOf(u)
			if !z.vtz.dropped.Load() {
				return u.Add(off)
			}
			z = r.zone(tzid)
		}
		loc = z.loc
		if loc == nil {
			return u
		}
	}
	return wallFields(u.In(loc))
}

func floatingLoc(l *time.Location) *time.Location {
	if l == nil {
		return time.UTC
	}
	return l
}

// wallFields copies the date and time of t into UTC.
func wallFields(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(),
		t.Nanosecond(), time.UTC)
}

// locInstant returns the instant at which loc shows the wall time w, given
// as fields labelled UTC. time.Date leaves the choice open for skipped and
// repeated wall times, and in practice its choice differs between zones,
// so the offset is worked out here.
//
// A zone's offset is within a day of UTC, so the instant lies within a day
// of w read as UTC. The offsets in effect a day either side of that, and at
// it, are the candidates. An offset fits if loc uses it at w minus that
// offset. Of several that fit, the largest gives the earliest instant. If
// none fits, w falls in a gap, and the offset from the day before applies.
// This assumes the zone changes its offset at most once within a day of w,
// which holds for every real zone.
func locInstant(w time.Time, loc *time.Location) time.Time {
	if loc == time.UTC {
		return w
	}
	offsetAt := func(t time.Time) time.Duration {
		_, s := t.In(loc).Zone()
		return time.Duration(s) * time.Second
	}
	before := offsetAt(w.Add(-24 * time.Hour))
	best, found := before, false
	for _, c := range []time.Duration{before, offsetAt(w), offsetAt(w.Add(24 * time.Hour))} {
		if offsetAt(w.Add(-c)) == c && (!found || c > best) {
			best, found = c, true
		}
	}
	return w.Add(-best)
}

// zone resolves a TZID, using and filling the cache.
func (r *Resolver) zone(tzid string) zone {
	tzid = strings.TrimSpace(tzid)
	if r == nil {
		return lookupZone(tzid, nil)
	}
	if z, ok := r.embedded[tzid]; ok && !z.dropped.Load() {
		return zone{vtz: z}
	}
	r.mu.Lock()
	z, ok := r.cache[tzid]
	r.mu.Unlock()
	if ok {
		return z
	}
	z = lookupZone(tzid, &r.misses)
	if len(tzid) <= maxTZIDLen {
		r.mu.Lock()
		if len(r.cache) >= maxCachedZones {
			clear(r.cache)
		}
		r.cache[tzid] = z
		r.mu.Unlock()
	}
	return z
}

// lookupZone finds tzid as a Windows zone name in any ASCII case, or in
// the zone database: directly, then without leading path elements. It
// returns UTC for a TZID it does not know or will not look up. Lookups in
// the database that find nothing count in misses, and none are made once
// misses reaches maxZoneMisses; a nil misses sets no limit. The targets of
// the Windows table are trusted and never count.
func lookupZone(tzid string, misses *atomic.Int32) zone {
	if len(tzid) > maxTZIDLen || strings.ContainsAny(tzid, "\x00\\") {
		return zone{}
	}
	if iana, ok := lookupWindowsZoneFold(tzid); ok {
		if loc := loadZone(iana, nil); loc != nil {
			return zone{loc: loc}
		}
	}
	elems := strings.Split(tzid, "/")
	if slices.Contains(elems, "..") {
		return zone{}
	}
	for i := max(len(elems)-maxTZIDDepth, 0); i < len(elems); i++ {
		if loc := loadZone(strings.Join(elems[i:], "/"), misses); loc != nil {
			return zone{loc: loc}
		}
	}
	return zone{}
}

// loadZone loads name from the zone database if it looks like a zone name:
// letters, digits and _+- in path elements that are not empty. "Local",
// the zone of the machine, is refused. A zone loaded before comes from the
// process-wide cache. Otherwise a lookup that finds nothing counts in
// misses, as for lookupZone, and so does one that finds a zone when the
// cache is full.
func loadZone(name string, misses *atomic.Int32) *time.Location {
	if name == "" || name == "Local" {
		return nil
	}
	for _, e := range strings.Split(name, "/") {
		if e == "" {
			return nil
		}
		for i := 0; i < len(e); i++ {
			if !zoneNameByte(e[i]) {
				return nil
			}
		}
	}
	loaded.Lock()
	loc := loaded.m[name]
	loaded.Unlock()
	if loc != nil {
		return loc
	}
	if misses != nil && misses.Load() >= maxZoneMisses {
		return nil
	}
	zoneLookups.Add(1)
	loc, err := time.LoadLocation(name)
	if err != nil {
		if misses != nil {
			misses.Add(1)
		}
		return nil
	}
	loaded.Lock()
	full := len(loaded.m) >= maxLoadedZones
	if !full {
		loaded.m[name] = loc
	}
	loaded.Unlock()
	// With the cache full, the next request reads the file again, so the
	// read counts as a miss would.
	if full && misses != nil {
		misses.Add(1)
	}
	return loc
}

var (
	foldedWindowsOnce  sync.Once
	foldedWindowsZones map[string]string
)

// lookupWindowsZoneFold is lookupWindowsZone ignoring ASCII case.
func lookupWindowsZoneFold(name string) (string, bool) {
	foldedWindowsOnce.Do(func() {
		foldedWindowsZones = make(map[string]string, len(windowsZones))
		for win, iana := range windowsZones {
			foldedWindowsZones[lowerASCII(win)] = iana
		}
	})
	iana, ok := foldedWindowsZones[lowerASCII(name)]
	return iana, ok
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func zoneNameByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return c == '_' || c == '+' || c == '-'
}
