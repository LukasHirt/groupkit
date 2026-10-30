// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// ErrRecurrence reports a VEVENT whose recurrence cannot be expanded: a
// malformed RRULE, RDATE, EXDATE or RECURRENCE-ID, a missing DTSTART, or a
// rule beyond the limits below.
var ErrRecurrence = errors.New("ical: invalid recurrence")

// Limits on what one calendar object may ask for. Its recurrence comes from
// untrusted data.
const (
	// maxOccurrences caps the occurrences examined per object: DTSTART,
	// RDATEs and rule instances together, and separately the overrides.
	maxOccurrences = 10_000
	// maxExDates caps EXDATE values. Removing more occurrences than can be
	// examined has no use. RDATE values are capped at maxRDates.
	maxExDates = maxOccurrences
	// maxWindowYears caps the length of a window.
	maxWindowYears = 100
	// maxEventRuleList caps each BYxxx list of an RRULE. It allows every
	// distinct BYMONTHDAY, positive and negative.
	maxEventRuleList = 62
	// maxPeriodInstances caps the candidates an RRULE has in one period:
	// the days of a period times the times of a day. The rule library
	// builds them all at once.
	maxPeriodInstances = 100_000
	// maxStepsPerInstance caps how many INTERVAL steps an HOURLY, MINUTELY
	// or SECONDLY rule takes, on average, per time of day its BYHOUR,
	// BYMINUTE and BYSECOND allow. With maxOccurrences that bounds the
	// library's stepping to 20,000,000 cheap steps per expansion.
	maxStepsPerInstance = 2_000
	// wallMargin widens the wall-clock range a rule is expanded over. A
	// wall time that occurs twice when clocks go back means its earlier
	// instant, so an occurrence just before a window that starts or ends
	// after the change can have a later wall time than the window's own.
	// Clocks have gone back by up to about a day (Pacific/Kwajalein in
	// 1969, America/Sitka in 1867). Instances in the margin that miss the
	// window do not count towards maxOccurrences.
	wallMargin = 26 * time.Hour
)

func recurErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRecurrence}, a...)...)
}

// Range is a time range. A zero Start or End leaves that side open.
type Range struct {
	Start, End time.Time
}

// Occurrence is one instance of an event.
type Occurrence struct {
	// Start and End are instants in UTC. End is exclusive and equals Start
	// for an event without duration.
	Start, End time.Time
	// RecurrenceID is the instant of the instance in the recurrence set,
	// which is where an override was moved from. It is zero for an event
	// that does not recur.
	RecurrenceID time.Time
	// Override is true for an instance taken from a component with a
	// RECURRENCE-ID.
	Override bool
}

// Occurrences returns the occurrences of the calendar's VEVENT that overlap
// win, sorted by start. Other components have none.
//
// An occurrence overlaps when it starts before win.End and ends after
// win.Start; one without duration overlaps when it starts at or after
// win.Start and before win.End. A zero Start or End leaves that side open.
// A recurring event is expanded over at most 100 years, from the later of
// win.Start and its DTSTART, and not past win.End. A window that is wider,
// or open, therefore finds a series only within those 100 years, while
// single events and overrides anywhere in the window are found. A bound at
// the zero time.Time cannot be told from an open side.
//
// At most 10,000 occurrences are examined; past that the result is cut
// short without an error, so a window with more occurrences than that
// returns only its first 10,000. An RRULE with COUNT is expanded from
// DTSTART and every instance counts, so if its DTSTART lies far before the
// window, those 10,000 can run out before it. An HOURLY, MINUTELY or
// SECONDLY rule whose BYHOUR, BYMINUTE and BYSECOND let it through on
// fewer than one in 2,000 of its steps is rejected, since the rule library
// would spend that many steps per instance. So is such a rule that filters
// both times of day and days while its INTERVAL does not divide a day.
//
// The component without RECURRENCE-ID is the master; further ones are
// ignored. Its DTSTART, RDATEs and the instances of its first RRULE make
// the recurrence set, less its EXDATEs; RFC 5545 leaves a DTSTART that the
// rule does not match undefined, and here it is an instance. COUNT counts
// the instances the rule generates, before EXDATE removes any, so such a
// DTSTART comes on top of COUNT.
//
// The rule runs on wall-clock time in DTSTART's zone, so an occurrence
// keeps its local time across a change of offset. For HOURLY and finer
// rules this means the wall times in an hour skipped when clocks go
// forward map to the same instants as those of the next hour and are
// merged with them, and an hour repeated when clocks go back is expanded
// once.
//
// Each occurrence lasts as long as the master: DTEND minus DTSTART as
// instants, else DURATION, else nothing, except that an all-day event
// lasts at least one day when its DTEND is not after DTSTART or it has
// neither, and all-day lengths are counted in days.
//
// A component with RECURRENCE-ID overrides the occurrence at that instant
// with its own DTSTART and length; one that matches no occurrence is
// returned anyway. The RANGE parameter (THISANDFUTURE) is not supported and
// is ignored. An override with STATUS:CANCELLED is still returned; callers
// decide what to do with it.
//
// Values are turned into instants by res, or by NewResolver(c) when res is
// nil; floating times and dates use floating, UTC when nil. Occurrences
// only reads c and res, so concurrent calls are safe.
func (c *Calendar) Occurrences(res *Resolver, floating *time.Location, win Range) ([]Occurrence, error) {
	if res == nil {
		res = NewResolver(c)
	}
	x := expansion{res: res, floating: floating}
	var master *Component
	var out []Occurrence
	replaced := map[int64]bool{}
	for _, ev := range c.Comps("VEVENT") {
		if ev.Prop("RECURRENCE-ID") == nil {
			if master == nil {
				master = ev
			}
			continue
		}
		if len(replaced) == maxOccurrences {
			continue
		}
		o, err := x.override(ev)
		if err != nil {
			return nil, err
		}
		// A second override of the same instance is ignored.
		if key := o.RecurrenceID.Unix(); !replaced[key] {
			replaced[key] = true
			out = append(out, o)
		}
	}

	var occs []Occurrence
	if master != nil {
		var err error
		if occs, err = x.master(master, win, replaced); err != nil {
			return nil, err
		}
	}
	out = slices.DeleteFunc(out, func(o Occurrence) bool { return !overlaps(o, win.Start, win.End) })
	out = append(out, occs...)
	slices.SortStableFunc(out, func(a, b Occurrence) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// expansionRange returns the range a recurring master is expanded over: at
// most maxWindowYears from a, the later of the window's start and the
// master's DTSTART, and never past the window's end. A window that starts
// before DTSTART, or leaves its start open, begins at DTSTART; one that
// leaves its end open ends at a plus maxWindowYears.
func expansionRange(win Range, dtstart time.Time) (lo, hi time.Time) {
	lo = dtstart
	if win.Start.After(lo) {
		lo = win.Start
	}
	hi = lo.AddDate(maxWindowYears, 0, 0)
	if !win.End.IsZero() && win.End.Before(hi) {
		hi = win.End
	}
	return lo, hi
}

func overlaps(o Occurrence, lo, hi time.Time) bool {
	if !hi.IsZero() && !o.Start.Before(hi) {
		return false
	}
	if lo.IsZero() {
		return true
	}
	if o.End.After(o.Start) {
		return o.End.After(lo)
	}
	return !o.Start.Before(lo)
}

type expansion struct {
	res      *Resolver
	floating *time.Location
	// first stops the expansion of the master at its first occurrence that
	// counts, for callers that only ask whether there is one.
	first bool
}

// occursIn reports whether any occurrence of the calendar's VEVENT overlaps
// win. It follows the rules of Occurrences but stops at the first
// occurrence that does, so the cost of a hit does not depend on how many
// occurrences the window holds: overrides are tested first, then DTSTART
// and the RDATEs, then the rule is walked only up to its first instance
// that overlaps the window and is neither excluded nor replaced.
func (c *Calendar) occursIn(res *Resolver, floating *time.Location, win Range) (bool, error) {
	if res == nil {
		res = NewResolver(c)
	}
	x := expansion{res: res, floating: floating, first: true}
	var master *Component
	var overrides []*Component
	for _, ev := range c.Comps("VEVENT") {
		if ev.Prop("RECURRENCE-ID") != nil {
			overrides = append(overrides, ev)
		} else if master == nil {
			master = ev
		}
	}
	// A master that cannot be read makes the whole recurrence unexpandable,
	// whatever its overrides say, so it is read before they are tried.
	var set masterSet
	if master != nil {
		var err error
		if set, err = x.readMaster(master); err != nil {
			return false, err
		}
	}
	replaced := map[int64]bool{}
	hit := false
	for _, ev := range overrides {
		if len(replaced) == maxOccurrences {
			break
		}
		o, err := x.override(ev)
		if err != nil {
			return false, err
		}
		// A second override of the same instance is ignored, as in
		// Occurrences: it neither replaces nor matches.
		if key := o.RecurrenceID.Unix(); !replaced[key] {
			replaced[key] = true
			hit = hit || overlaps(o, win.Start, win.End)
		}
	}
	if hit || master == nil {
		return hit, nil
	}
	occs, err := x.expandMaster(set, win, replaced)
	return len(occs) > 0, err
}

func (x expansion) instant(v Value) time.Time {
	return x.res.Instant(v, x.floating)
}

// length is how long each occurrence of a component lasts: whole days on
// the calendar, then an exact duration.
type length struct {
	days  int
	exact time.Duration
}

func (x expansion) length(c *Component, start Value) (length, error) {
	if p := c.Prop("DTEND"); p != nil {
		end, err := ParseDateTime(p.Value, p.Params)
		if err != nil {
			return length{}, recurErr("DTEND: %w", err)
		}
		if start.Date && end.Date {
			// An all-day DTEND on or before DTSTART is a mistake; the
			// event is read as lasting one day, as without DTEND.
			return length{days: max(1, int((end.Wall.Unix()-start.Wall.Unix())/86400))}, nil
		}
		return length{exact: max(0, x.instant(end).Sub(x.instant(start)))}, nil
	}
	if p := c.Prop("DURATION"); p != nil {
		d, err := ParseDuration(p.Value)
		if err != nil {
			return length{}, recurErr("DURATION: %w", err)
		}
		d = max(0, d)
		if start.Date {
			const day = 24 * time.Hour
			return length{days: int(d / day), exact: d % day}, nil
		}
		return length{exact: d}, nil
	}
	if start.Date {
		return length{days: 1}, nil
	}
	return length{}, nil
}

// end returns the end of an occurrence that starts at v, whose instant is
// start. The days of an all-day occurrence are counted on the calendar of
// the floating location.
func (x expansion) end(l length, v Value, start time.Time) time.Time {
	if v.Date {
		v.Wall = v.Wall.AddDate(0, 0, l.days)
		return x.instant(v).Add(l.exact)
	}
	return start.AddDate(0, 0, l.days).Add(l.exact)
}

func (x expansion) override(c *Component) (Occurrence, error) {
	p := c.Prop("RECURRENCE-ID")
	rid, err := ParseDateTime(p.Value, p.Params)
	if err != nil {
		return Occurrence{}, recurErr("RECURRENCE-ID: %w", err)
	}
	start := rid
	if p := c.Prop("DTSTART"); p != nil {
		if start, err = ParseDateTime(p.Value, p.Params); err != nil {
			return Occurrence{}, recurErr("DTSTART: %w", err)
		}
	}
	l, err := x.length(c, start)
	if err != nil {
		return Occurrence{}, err
	}
	s := x.instant(start)
	return Occurrence{Start: s, End: x.end(l, start, s), RecurrenceID: x.instant(rid), Override: true}, nil
}

// masterSet is a master component read and checked, ready to be expanded.
type masterSet struct {
	start    Value
	length   length
	rule     *rrule.ROption
	rdates   []Value
	excluded map[int64]bool
}

// readMaster reads DTSTART, the length, the first RRULE, the RDATEs and the
// EXDATEs of the master, and fails on any that is not valid.
func (x expansion) readMaster(c *Component) (masterSet, error) {
	p := c.Prop("DTSTART")
	if p == nil {
		return masterSet{}, recurErr("VEVENT without DTSTART")
	}
	start, err := ParseDateTime(p.Value, p.Params)
	if err != nil {
		return masterSet{}, recurErr("DTSTART: %w", err)
	}
	l, err := x.length(c, start)
	if err != nil {
		return masterSet{}, err
	}
	var rule *rrule.ROption
	if rules := c.PropAll("RRULE"); len(rules) > 0 {
		// RFC 5545 deprecates more than one RRULE; only the first is used.
		if rule, err = x.parseRule(rules[0].Value, start); err != nil {
			return masterSet{}, err
		}
	}
	rdates, err := dateList(c, "RDATE", maxRDates)
	if err != nil {
		return masterSet{}, err
	}
	exVals, err := dateList(c, "EXDATE", maxExDates)
	if err != nil {
		return masterSet{}, err
	}
	excluded := make(map[int64]bool, len(exVals))
	for _, v := range exVals {
		excluded[x.instant(v).Unix()] = true
	}
	return masterSet{start: start, length: l, rule: rule, rdates: rdates, excluded: excluded}, nil
}

// master expands the master component and returns its occurrences that
// overlap win, leaving out those in replaced.
func (x expansion) master(c *Component, win Range, replaced map[int64]bool) ([]Occurrence, error) {
	set, err := x.readMaster(c)
	if err != nil {
		return nil, err
	}
	return x.expandMaster(set, win, replaced)
}

func (x expansion) expandMaster(set masterSet, win Range, replaced map[int64]bool) ([]Occurrence, error) {
	start, l, rule, rdates, excluded := set.start, set.length, set.rule, set.rdates, set.excluded
	anchor := x.instant(start)
	lo, hi := expansionRange(win, anchor)
	cands := append([]Value{start}, rdates...)
	if x.first {
		// DTSTART or an RDATE inside the window settles it without
		// touching the rule.
		for _, v := range cands {
			s := x.instant(v)
			if key := s.Unix(); excluded[key] || replaced[key] {
				continue
			}
			o := Occurrence{Start: s, End: x.end(l, v, s), RecurrenceID: s}
			if s.Before(hi) && overlaps(o, win.Start, win.End) {
				return []Occurrence{o}, nil
			}
		}
	}
	if rule != nil {
		// The rule runs on wall time in DTSTART's zone. An occurrence
		// that ends after lo starts after lo less its length.
		wallOf := func(t time.Time) time.Time {
			if start.UTC {
				return t.UTC()
			}
			return x.res.WallOf(t, start.TZID, x.floating)
		}
		genLo := start.Wall
		from := lo.AddDate(0, 0, -l.days).Add(-l.exact)
		if g := wallOf(from).Add(-wallMargin); g.After(genLo) {
			genLo = g
		}
		genHi := wallOf(hi).Add(wallMargin)
		keep := func(w time.Time) bool {
			v := start
			v.Wall = w
			s := x.instant(v)
			return s.Before(hi) && overlaps(Occurrence{Start: s, End: x.end(l, v, s)}, win.Start, win.End)
		}
		// Excluded and replaced instances count against the cap like any
		// other, as in the full expansion, but only a reported one ends a
		// first-hit walk.
		var stop func(time.Time) bool
		if x.first {
			stop = func(w time.Time) bool {
				v := start
				v.Wall = w
				key := x.instant(v).Unix()
				return !excluded[key] && !replaced[key]
			}
		}
		walls, err := expandWalls(*rule, genLo, genHi, maxOccurrences-len(cands), keep, stop)
		if err != nil {
			return nil, err
		}
		for _, w := range walls {
			v := start
			v.Wall = w
			cands = append(cands, v)
		}
	}

	recurring := rule != nil || len(rdates) > 0
	seen := make(map[int64]bool, len(cands))
	var out []Occurrence
	for _, v := range cands {
		s := x.instant(v)
		key := s.Unix()
		if seen[key] || excluded[key] || replaced[key] {
			continue
		}
		seen[key] = true
		o := Occurrence{Start: s, End: x.end(l, v, s)}
		if recurring {
			o.RecurrenceID = s
		}
		if s.Before(hi) && overlaps(o, win.Start, win.End) {
			out = append(out, o)
		}
	}
	return out, nil
}

// dateList parses the comma-separated DATE and DATE-TIME values of every
// name property, at most limit of them. A PERIOD value gives its start.
func dateList(c *Component, name string, limit int) ([]Value, error) {
	var out []Value
	for _, p := range c.PropAll(name) {
		for raw := range strings.SplitSeq(p.Value, ",") {
			if len(out) == limit {
				return nil, recurErr("more than %d %s values", limit, name)
			}
			raw, _, _ = strings.Cut(raw, "/")
			v, err := ParseDateTime(raw, p.Params)
			if err != nil {
				return nil, recurErr("%s: %w", name, err)
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// maxInterval holds, per frequency, an INTERVAL whose period is longer
// than the years the rule library covers. Larger ones change nothing and
// are cut to it, so the library's arithmetic cannot overflow.
var maxInterval = [...]int64{
	rrule.YEARLY:   10_000,
	rrule.MONTHLY:  120_000,
	rrule.WEEKLY:   530_000,
	rrule.DAILY:    3_700_000,
	rrule.HOURLY:   88_000_000,
	rrule.MINUTELY: 5_300_000_000,
	rrule.SECONDLY: 320_000_000_000,
}

// parseRule parses an RRULE for a component that starts at start. The
// rule runs on wall-clock time labelled UTC. A UTC UNTIL is turned into
// the wall time of DTSTART's zone, and a DATE UNTIL covers its whole day.
func (x expansion) parseRule(raw string, start Value) (*rrule.ROption, error) {
	raw = upperASCII(strings.TrimSpace(raw))
	hasFreq := false
	until := ""
	for part := range strings.SplitSeq(raw, ";") {
		name, val, _ := strings.Cut(part, "=")
		switch name {
		case "FREQ":
			hasFreq = true
		case "UNTIL":
			until = val
		case "DTSTART", "BYEASTER":
			// The rule library knows these; iCalendar does not.
			return nil, recurErr("RRULE: unknown part %s", name)
		}
	}
	if !hasFreq {
		return nil, recurErr("RRULE without FREQ")
	}
	opt, err := rrule.StrToROption(raw)
	if err != nil {
		return nil, recurErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	if opt.Freq < rrule.YEARLY || int(opt.Freq) >= len(maxInterval) {
		return nil, recurErr("RRULE: bad FREQ")
	}
	for _, l := range [][]int{opt.Bysetpos, opt.Bymonth, opt.Bymonthday, opt.Byyearday,
		opt.Byweekno, opt.Byhour, opt.Byminute, opt.Bysecond} {
		if len(l) > maxEventRuleList {
			return nil, recurErr("RRULE list longer than %d", maxEventRuleList)
		}
	}
	if len(opt.Byweekday) > maxEventRuleList {
		return nil, recurErr("RRULE list longer than %d", maxEventRuleList)
	}
	opt.Dtstart = start.Wall
	opt.Interval = max(opt.Interval, 1)
	if lim := maxInterval[opt.Freq]; int64(opt.Interval) > lim {
		opt.Interval = int(lim)
	}
	opt.Count = max(opt.Count, 0)
	opt.Until = time.Time{}
	if until != "" {
		v, err := ParseDateTime(until, nil)
		if err != nil {
			return nil, recurErr("UNTIL: %w", err)
		}
		opt.Until = v.Wall
		switch {
		case v.Date:
			opt.Until = v.Wall.Add(24*time.Hour - time.Second)
		case v.UTC && start.TZID != "":
			opt.Until = x.res.WallOf(v.Wall, start.TZID, x.floating)
		}
	}
	if driftingTimeOfDay(*opt) {
		return nil, recurErr("RRULE: a sub-daily INTERVAL that does not divide a day " +
			"cannot be combined with both time and day filters")
	}
	explicitDefaults(opt)
	if err := checkRule(*opt); err != nil {
		return nil, recurErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	if n := periodInstances(*opt); n > maxPeriodInstances {
		return nil, recurErr("RRULE has more than %d instances in a period", maxPeriodInstances)
	}
	if cycle, hits := stepCost(*opt); hits > 0 && cycle > maxStepsPerInstance*hits {
		return nil, recurErr("RRULE takes more than %d steps per instance", maxStepsPerInstance)
	}
	return opt, nil
}

// checkRule lets the rule library check the bounds of a rule, turning a
// panic into an error.
func checkRule(opt rrule.ROption) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rule rejected: %v", r)
		}
	}()
	_, err = rrule.NewRRule(opt)
	return err
}

// periodInstances returns how many candidates a rule that went through
// explicitDefaults has in one period: days of the period times times of
// the day.
func periodInstances(opt rrule.ROption) int {
	n := 1
	switch opt.Freq {
	case rrule.YEARLY:
		n = 366
	case rrule.MONTHLY:
		n = 31
	case rrule.WEEKLY:
		n = 7
	}
	for _, l := range [][]int{opt.Byhour, opt.Byminute, opt.Bysecond} {
		n *= max(len(l), 1)
	}
	return n
}

// driftingTimeOfDay reports an HOURLY, MINUTELY or SECONDLY rule, as
// written and before explicitDefaults, that stepCost cannot vouch for.
// When the step does not divide a day, the times of day the steps reach
// differ from one day to the next. If the rule also filters days, the
// library may step through most of every allowed day before it finds an
// allowed time, or find none, until its last year; stepCost averages over
// all days and does not see that. Only the lists the library's stepping
// loop checks count as time filters: BYHOUR for HOURLY, BYHOUR and
// BYMINUTE for MINUTELY, all three for SECONDLY.
func driftingTimeOfDay(opt rrule.ROption) bool {
	if opt.Freq < rrule.HOURLY || 86400%(int64(opt.Interval)*secondsPer(opt.Freq)) == 0 {
		return false
	}
	timeFilter := len(opt.Byhour) > 0 ||
		opt.Freq >= rrule.MINUTELY && len(opt.Byminute) > 0 ||
		opt.Freq == rrule.SECONDLY && len(opt.Bysecond) > 0
	dayFilter := len(opt.Byweekday) > 0 || len(opt.Bymonth) > 0 || len(opt.Bymonthday) > 0 ||
		len(opt.Byyearday) > 0 || len(opt.Byweekno) > 0
	return timeFilter && dayFilter
}

// stepCost describes how an HOURLY, MINUTELY or SECONDLY rule walks
// through the day. The rule library steps by INTERVAL until the time of day
// is one that BYHOUR, BYMINUTE and BYSECOND allow, so it takes cycle steps
// to come back to the same time of day, and hits of those are allowed.
// With no hits it would step for ever. Coarser rules step once a period
// and give (1, 1).
func stepCost(opt rrule.ROption) (cycle, hits int64) {
	if opt.Freq < rrule.HOURLY {
		return 1, 1
	}
	d := opt.Dtstart
	// The steps take the time of day through one residue class modulo g,
	// which has 86400/g members.
	g := gcd(int64(opt.Interval)*secondsPer(opt.Freq), 86400)
	t0 := int64(d.Hour()*3600 + d.Minute()*60 + d.Second())
	// Only the parts finer than the frequency stay fixed at DTSTART's.
	hours, minutes, seconds := allOr(opt.Byhour, 24), allOr(opt.Byminute, 60), allOr(opt.Bysecond, 60)
	if opt.Freq == rrule.HOURLY {
		minutes = []int{d.Minute()}
	}
	if opt.Freq != rrule.SECONDLY {
		seconds = []int{d.Second()}
	}
	// A list may repeat a value; count each time of day once.
	seen := map[int64]bool{}
	for _, h := range hours {
		for _, m := range minutes {
			for _, s := range seconds {
				tod := int64(h*3600 + m*60 + s)
				if (tod-t0)%g == 0 && !seen[tod] {
					seen[tod] = true
					hits++
				}
			}
		}
	}
	return 86400 / g, hits
}

func allOr(l []int, n int) []int {
	if len(l) > 0 {
		return l
	}
	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	return all
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// expandWalls returns the instances of a rule that are wall times in
// [lo, hi) and that keep accepts, at most limit of them. The rule must
// have gone through parseRule.
//
// A rule without COUNT starts at the last of its periods that begins by
// lo, and only instances that keep accepts count towards limit, so the
// margins around a window do not use it up. A rule with COUNT is expanded
// from DTSTART and every instance counts. With stop set, the walk ends at
// the first instance that keep accepts and stop approves.
func expandWalls(opt rrule.ROption, lo, hi time.Time, limit int, keep func(time.Time) bool, stop func(time.Time) bool) ([]time.Time, error) {
	if _, hits := stepCost(opt); limit <= 0 || hits == 0 || !opt.Dtstart.Before(hi) {
		return nil, nil
	}
	counted := opt.Count > 0
	if !counted {
		opt = shiftStart(opt, lo)
	}
	years := cycleYears(hi)
	opt.Dtstart = opt.Dtstart.AddDate(years, 0, 0)
	if opt.Dtstart.IsZero() {
		// The rule library reads a zero DTSTART as "now", but truncates
		// it to the second only after that check.
		opt.Dtstart = opt.Dtstart.Add(time.Nanosecond)
	}
	if opt.Until.IsZero() || opt.Until.After(hi) {
		opt.Until = hi
	}
	opt.Until = opt.Until.AddDate(years, 0, 0)
	lo, hi = lo.AddDate(years, 0, 0), hi.AddDate(years, 0, 0)

	var out []time.Time
	n := 0
	_, err := expandRule(opt, hi, func(t time.Time) bool {
		kept, done := false, false
		if !t.Before(lo) {
			w := t.AddDate(-years, 0, 0)
			if kept = keep(w); kept {
				out = append(out, w)
				done = stop != nil && stop(w)
			}
		}
		if kept || counted {
			n++
		}
		return n < limit && !done
	})
	if err != nil {
		return nil, recurErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	return out, nil
}

// cycleYears returns how many years to move a rule expanded up to hi.
// The rule library looks for a rule's next instance up to its last year,
// however far that is. Moving every time by whole 400-year cycles of the
// Gregorian calendar, which keeps weekdays and leap days in place, brings
// hi within 400 years of that year, so no search runs much past hi.
func cycleYears(hi time.Time) int {
	if y := hi.Year(); y < rrule.MAXYEAR {
		return (rrule.MAXYEAR - y) / 400 * 400
	}
	return 0
}

// shiftStart moves the start of a rule without COUNT forward by whole
// periods, to the latest start of one of its periods at or before lo,
// which leaves its instances from there on unchanged. The rule must have
// gone through explicitDefaults. A YEARLY or MONTHLY rule starts on the
// first of a month, so moving it by months cannot overflow the day; a
// WEEKLY one at the start of its week, so that week is whole.
func shiftStart(opt rrule.ROption, lo time.Time) rrule.ROption {
	d := opt.Dtstart
	if !d.Before(lo) {
		return opt
	}
	interval := int64(opt.Interval)
	switch opt.Freq {
	case rrule.YEARLY, rrule.MONTHLY:
		opt = shiftMonths(opt, monthIndex(lo))
	case rrule.WEEKLY:
		back := (mondayFirst(d.Weekday()) - opt.Wkst.Day() + 7) % 7
		week := time.Date(d.Year(), d.Month(), d.Day()-back, 0, 0, 0, 0, time.UTC)
		if t, ok := advance(week, lo, interval*7*86400); ok {
			opt.Dtstart = t
		}
	default:
		if t, ok := advance(d, lo, interval*secondsPer(opt.Freq)); ok {
			opt.Dtstart = t
		}
	}
	return opt
}

// secondsPer returns the seconds in one period of a DAILY or finer rule.
func secondsPer(f rrule.Frequency) int64 {
	switch f {
	case rrule.DAILY:
		return 86400
	case rrule.HOURLY:
		return 3600
	case rrule.MINUTELY:
		return 60
	}
	return 1
}

// advance returns base moved forward by the most whole periods of the
// given seconds that keep it at or before lo, and false if that is none.
func advance(base, lo time.Time, period int64) (time.Time, bool) {
	k := (lo.Unix() - base.Unix()) / period
	if k <= 0 {
		return base, false
	}
	return time.Unix(base.Unix()+k*period, 0).UTC(), true
}
