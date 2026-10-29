// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// Limits on what one VTIMEZONE may ask for. Its text comes from untrusted
// calendar data, and every rule is expanded again for each block of years
// looked up.
const (
	maxObservances = 100
	maxRDates      = 1000
	// maxOnsetsPerYear caps an RRULE, checked over its first two years.
	maxOnsetsPerYear = 25
	// maxRuleList caps each BYxxx list of an RRULE. A month has 31 days,
	// which is the longest list a zone rule has any use for.
	maxRuleList = 31
	// blockYears is how many years one entry of a zone's onset cache
	// covers. Larger blocks mean fewer expansions over a long query and
	// longer lists to search.
	blockYears = 8
	// maxCachedBlocks bounds the per-zone cache of expanded onsets.
	maxCachedBlocks = 64
	// ruleWork is the work one Resolver may spend expanding each rule of
	// a VTIMEZONE, counted in rule instances and periods walked. A
	// VTIMEZONE with n rules may spend n times that; one that needs more
	// is dropped. Evaluating a real zone over a century takes a few
	// hundred, and the 50 rules a calendar may have take at most about
	// half a second together.
	ruleWork = 20_000
	// maxRuleCount caps COUNT in an RRULE. Such a rule is expanded from
	// DTSTART once to find its last onset; real zones need a handful.
	maxRuleCount = 400
)

// observance is one STANDARD or DAYLIGHT block. Times are local wall-clock
// times labelled UTC, expressed in the offset in effect before the onset.
type observance struct {
	start    time.Time
	from, to time.Duration
	// rule is the parsed RRULE, or nil. Until is always set, in local time,
	// so a rule with no end does not stop at the library's default, and
	// COUNT has been turned into UNTIL.
	rule   *rrule.ROption
	rdates []time.Time
}

// onset is the local wall time at which an observance takes effect.
type onset struct {
	wall     time.Time
	from, to time.Duration
}

// vtz is a parsed VTIMEZONE that maps local wall times to UTC offsets.
// It is safe for concurrent use.
type vtz struct {
	obs []observance
	// before is the offset for wall times ahead of every onset: the
	// TZOFFSETFROM of the observance that starts first.
	before time.Duration

	mu     sync.Mutex
	blocks map[int][]onset
	// work is what expanding blocks has cost so far, and budget what it
	// may cost; both are guarded by mu.
	work, budget int
	// dropped is set, and never cleared, once work passes budget. A
	// Resolver then resolves the TZID as if the VTIMEZONE had failed to
	// parse.
	dropped atomic.Bool
}

func tzErr(format string, a ...any) error {
	return fmt.Errorf("%w: VTIMEZONE: "+format, append([]any{ErrInvalid}, a...)...)
}

// parseVTimezone reads the STANDARD and DAYLIGHT observances of c.
func parseVTimezone(c *Component) (*vtz, error) {
	if !contentline.EqualFoldASCII(c.Name, "VTIMEZONE") {
		return nil, tzErr("got %s", contentline.Clip(c.Name))
	}
	z := &vtz{blocks: map[int][]onset{}}
	rdates := 0
	for _, ch := range c.Children {
		if !contentline.EqualFoldASCII(ch.Name, "STANDARD") && !contentline.EqualFoldASCII(ch.Name, "DAYLIGHT") {
			continue
		}
		if len(z.obs) == maxObservances {
			return nil, tzErr("more than %d observances", maxObservances)
		}
		o, err := parseObservance(ch)
		if err != nil {
			return nil, err
		}
		rdates += len(o.rdates)
		if rdates > maxRDates {
			return nil, tzErr("more than %d RDATE values", maxRDates)
		}
		z.obs = append(z.obs, o)
	}
	if len(z.obs) == 0 {
		return nil, tzErr("no STANDARD or DAYLIGHT")
	}
	first := z.obs[0]
	for _, o := range z.obs[1:] {
		if o.start.Before(first.start) {
			first = o
		}
	}
	z.before = first.from
	rules := 0
	for _, o := range z.obs {
		if o.rule != nil {
			rules++
		}
	}
	z.budget = ruleWork * max(rules, 1)
	return z, nil
}

func parseObservance(c *Component) (observance, error) {
	var o observance
	var err error
	for _, name := range []string{"TZOFFSETFROM", "TZOFFSETTO", "DTSTART"} {
		if c.Prop(name) == nil {
			return o, tzErr("%s without %s", contentline.Clip(c.Name), name)
		}
	}
	if o.from, err = parseOffset(c.Prop("TZOFFSETFROM").Value); err != nil {
		return o, err
	}
	if o.to, err = parseOffset(c.Prop("TZOFFSETTO").Value); err != nil {
		return o, err
	}
	p := c.Prop("DTSTART")
	v, err := ParseDateTime(p.Value, p.Params)
	if err != nil {
		return o, tzErr("DTSTART: %w", err)
	}
	if v.Date {
		return o, tzErr("DTSTART must be a date-time")
	}
	o.start = o.local(v)
	if o.start.IsZero() {
		// The rule library reads a zero DTSTART as "now".
		return o, tzErr("DTSTART %s is out of range", contentline.Clip(p.Value))
	}
	for _, p := range c.PropAll("RDATE") {
		for raw := range strings.SplitSeq(p.Value, ",") {
			v, err := ParseDateTime(raw, p.Params)
			if err != nil {
				return o, tzErr("RDATE: %w", err)
			}
			if v.Date {
				return o, tzErr("RDATE must be a date-time")
			}
			if len(o.rdates) == maxRDates {
				return o, tzErr("more than %d RDATE values", maxRDates)
			}
			o.rdates = append(o.rdates, o.local(v))
		}
	}
	switch rules := c.PropAll("RRULE"); len(rules) {
	case 0:
	case 1:
		if o.rule, err = o.parseRule(rules[0].Value); err != nil {
			return o, err
		}
	default:
		return o, tzErr("more than one RRULE")
	}
	return o, nil
}

// local turns a DTSTART, RDATE or UNTIL value into the observance's local
// wall time. A UTC value is shifted by TZOFFSETFROM.
func (o *observance) local(v Value) time.Time {
	if v.UTC {
		return v.Wall.Add(o.from)
	}
	return v.Wall
}

// parseRule parses an RRULE and checks that it can be expanded cheaply. It
// returns nil for a rule with no instances at all.
func (o *observance) parseRule(raw string) (*rrule.ROption, error) {
	raw = upperASCII(raw)
	opt, err := rrule.StrToROption(raw)
	if err != nil {
		return nil, tzErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	// Time zone rules change at most a few times a year. Finer frequencies
	// would make every lookup walk through many periods.
	if opt.Freq != rrule.YEARLY && opt.Freq != rrule.MONTHLY {
		return nil, tzErr("RRULE frequency %s is not supported", opt.Freq)
	}
	// An onset happens once on its day, so a rule needs one time of day.
	// More would multiply the instances every lookup walks through.
	for _, l := range [][]int{opt.Byhour, opt.Byminute, opt.Bysecond} {
		if len(l) > 1 {
			return nil, tzErr("RRULE with more than one BYHOUR, BYMINUTE or BYSECOND")
		}
	}
	for _, l := range [][]int{opt.Bysetpos, opt.Bymonth, opt.Bymonthday, opt.Byyearday,
		opt.Byweekno, opt.Byeaster} {
		if len(l) > maxRuleList {
			return nil, tzErr("RRULE list longer than %d", maxRuleList)
		}
	}
	if len(opt.Byweekday) > maxRuleList {
		return nil, tzErr("RRULE list longer than %d", maxRuleList)
	}
	if opt.Count > maxRuleCount {
		return nil, tzErr("RRULE COUNT above %d", maxRuleCount)
	}
	opt.Dtstart = o.start
	opt.Interval = max(opt.Interval, 1)
	explicitDefaults(opt)
	// UNTIL is UTC in a VTIMEZONE, but the rule runs in local time.
	opt.Until = ruleEnd
	for part := range strings.SplitSeq(raw, ";") {
		if until, ok := strings.CutPrefix(part, "UNTIL="); ok {
			v, err := ParseDateTime(until, nil)
			if err != nil {
				return nil, tzErr("UNTIL: %w", err)
			}
			opt.Until = o.local(v)
			if v.Date {
				opt.Until = opt.Until.Add(24*time.Hour - time.Second)
			}
		}
	}

	// A rule that never matches makes the library search up to its last
	// year on every expansion. Its pattern repeats within the span that
	// shiftRule keeps, so looking near that year is enough to find out.
	// COUNT only limits a rule that has instances, so it is left out here.
	found := false
	bare := *opt
	bare.Count = 0
	if _, err := expandRule(shiftRule(bare, ruleEnd), ruleEnd, func(time.Time) bool {
		found = true
		return false
	}); err != nil {
		return nil, tzErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	if !found {
		return nil, nil
	}

	// Leaving COUNT out can only add instances, so this check is strict.
	n := 0
	if _, err := expandRule(bare, o.start.AddDate(2, 0, 0), func(time.Time) bool {
		n++
		return n <= 2*maxOnsetsPerYear
	}); err != nil {
		return nil, tzErr("RRULE: %s", contentline.Clip(err.Error()))
	}
	if n > 2*maxOnsetsPerYear {
		return nil, tzErr("RRULE has more than %d onsets a year", maxOnsetsPerYear)
	}

	// COUNT counts from DTSTART, so a rule with one cannot be shifted.
	// Find its last instance once and use that as UNTIL.
	if opt.Count > 0 {
		var last time.Time
		if _, err := expandRule(*opt, ruleEnd, func(t time.Time) bool {
			last = t
			return true
		}); err != nil {
			return nil, tzErr("RRULE: %s", contentline.Clip(err.Error()))
		}
		if last.IsZero() {
			return nil, nil
		}
		opt.Count = 0
		opt.Until = last
	}
	return opt, nil
}

// maxRuleSteps stops one expansion of a rule that slips past the checks.
// It is a variable so tests can lower it.
var maxRuleSteps = 1_000_000

// ruleSteps counts the instances the rule library has produced, for tests
// that check how much work an operation does.
var ruleSteps atomic.Int64

// ruleEnd is past the last year the rule library generates.
var ruleEnd = time.Date(rrule.MAXYEAR+1, 1, 1, 0, 0, 0, 0, time.UTC)

// explicitDefaults spells out the parts the library would otherwise take
// from DTSTART, so that moving DTSTART does not change the rule.
func explicitDefaults(opt *rrule.ROption) {
	d := opt.Dtstart
	if len(opt.Byweekno) == 0 && len(opt.Byyearday) == 0 && len(opt.Bymonthday) == 0 &&
		len(opt.Byweekday) == 0 && len(opt.Byeaster) == 0 {
		switch opt.Freq {
		case rrule.YEARLY:
			if len(opt.Bymonth) == 0 {
				opt.Bymonth = []int{int(d.Month())}
			}
			opt.Bymonthday = []int{d.Day()}
		case rrule.MONTHLY:
			opt.Bymonthday = []int{d.Day()}
		case rrule.WEEKLY:
			opt.Byweekday = []rrule.Weekday{ruleWeekdays[mondayFirst(d.Weekday())]}
		}
	}
	if len(opt.Byhour) == 0 && opt.Freq < rrule.HOURLY {
		opt.Byhour = []int{d.Hour()}
	}
	if len(opt.Byminute) == 0 && opt.Freq < rrule.MINUTELY {
		opt.Byminute = []int{d.Minute()}
	}
	if len(opt.Bysecond) == 0 && opt.Freq < rrule.SECONDLY {
		opt.Bysecond = []int{d.Second()}
	}
}

// ruleWeekdays lists the rule library's weekdays from Monday.
var ruleWeekdays = [7]rrule.Weekday{rrule.MO, rrule.TU, rrule.WE, rrule.TH, rrule.FR, rrule.SA, rrule.SU}

// mondayFirst numbers weekdays from 0 for Monday, as the rule library does.
func mondayFirst(d time.Weekday) int {
	return (int(d) + 6) % 7
}

// shiftLookback is how many months a YEARLY or MONTHLY rule with INTERVAL=1
// may need to repeat: the Gregorian calendar repeats every 400 years.
const shiftLookback = 400 * 12

// shiftRule moves the start of a rule without COUNT forward by whole
// periods, so expanding it up to end walks at most a few thousand periods
// whatever its DTSTART. The rule keeps every instance in the last
// shiftLookback times INTERVAL months before end, which covers a full
// repeat of its pattern, so the latest instance before any time in that
// span is unchanged. The rule must have gone through explicitDefaults.
func shiftRule(opt rrule.ROption, end time.Time) rrule.ROption {
	if opt.Until.Before(end) {
		end = opt.Until
	}
	// A huge interval reaches the last year in a few periods anyway.
	if opt.Count > 0 || opt.Interval > 12*rrule.MAXYEAR {
		return opt
	}
	return shiftMonths(opt, monthIndex(end)-shiftLookback*int64(opt.Interval))
}

// monthIndex numbers the months from January of year 0.
func monthIndex(t time.Time) int64 {
	return int64(t.Year())*12 + int64(t.Month()) - 1
}

// shiftMonths moves the start of a YEARLY or MONTHLY rule forward by whole
// periods, to the first of the last period's month that is not after the
// month numbered target. A rule that would not move is returned as it is.
// Starting on the first of a month, a rule cannot overflow the day.
func shiftMonths(opt rrule.ROption, target int64) rrule.ROption {
	step := int64(opt.Interval)
	if opt.Freq == rrule.YEARLY {
		step *= 12
	}
	d := monthIndex(opt.Dtstart)
	if k := (target - d) / step; k > 0 {
		m := d + k*step
		opt.Dtstart = time.Date(int(m/12), time.Month(m%12+1), 1, 0, 0, 0, 0, time.UTC)
	}
	return opt
}

// expandBack passes to fn the instances of a rule before hi, from about
// two years before lo. Until done reports true, it then goes further back
// in spans that grow fourfold, each ending where the previous one began,
// so no instance comes twice. For a rule whose UNTIL is before lo, the
// spans are counted back from UNTIL. The search stops at DTSTART, or after
// shiftLookback periods, which hold a full repeat of the rule's pattern.
// Callers use done to stop once they have the latest instance before lo.
//
// It returns the work done: the instances produced plus the periods the
// spans cover, since the library walks periods that have no instances.
func expandBack(rule rrule.ROption, lo, hi time.Time, fn func(time.Time) bool, done func() bool) (int, error) {
	step := int64(rule.Interval)
	if rule.Freq == rrule.YEARLY {
		step *= 12
	}
	maxBack := shiftLookback / 12 * min(rule.Interval, rrule.MAXYEAR)
	// A rule that ended before lo has its last instances before UNTIL.
	if rule.Until.Before(lo) {
		lo = rule.Until
	}
	end := hi
	work := 0
	for back := 2; ; back *= 4 {
		back = min(back, maxBack)
		opt := shiftMonths(rule, monthIndex(lo.AddDate(-back, 0, 0)))
		n, err := expandRule(opt, end, fn)
		last := end
		if last.After(ruleEnd) {
			last = ruleEnd
		}
		work += n + int(max(0, (monthIndex(last)-monthIndex(opt.Dtstart))/step))
		if err != nil {
			return work, err
		}
		if done() || back == maxBack || !opt.Dtstart.After(rule.Dtstart) {
			return work, nil
		}
		end = opt.Dtstart
	}
}

// expandRule calls fn for each instance of the rule before hi, until fn
// returns false, and returns how many instances it passed to fn. It never
// runs past hi, returns an error after maxRuleSteps instances, and turns a
// panic in the rule library into an error.
func expandRule(opt rrule.ROption, hi time.Time, fn func(time.Time) bool) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rule expansion failed: %v", r)
		}
	}()
	if opt.Until.After(hi) {
		opt.Until = hi
	}
	r, err := rrule.NewRRule(opt)
	if err != nil {
		return 0, err
	}
	next := r.Iterator()
	steps := 0
	defer func() { ruleSteps.Add(int64(steps)) }()
	for range maxRuleSteps {
		steps++
		t, ok := next()
		if !ok || !t.Before(hi) {
			return n, nil
		}
		n++
		if !fn(t) {
			return n, nil
		}
	}
	return n, fmt.Errorf("more than %d instances", maxRuleSteps)
}

// parseOffset parses a UTC offset of the form +hhmm or +hhmmss, with + or -.
func parseOffset(raw string) (time.Duration, error) {
	bad := tzErr("bad UTC offset %q", contentline.Clip(raw))
	if len(raw) != 5 && len(raw) != 7 || raw[0] != '+' && raw[0] != '-' {
		return 0, bad
	}
	var f [3]int
	for i := 0; 1+2*i < len(raw); i++ {
		a, b := raw[1+2*i], raw[2+2*i]
		if a < '0' || a > '9' || b < '0' || b > '9' {
			return 0, bad
		}
		f[i] = int(a-'0')*10 + int(b-'0')
	}
	if f[0] > 23 || f[1] > 59 || f[2] > 59 {
		return 0, bad
	}
	d := time.Duration(f[0])*time.Hour + time.Duration(f[1])*time.Minute + time.Duration(f[2])*time.Second
	if raw[0] == '-' {
		d = -d
	}
	return d, nil
}

// offsetAt returns the UTC offset in effect at a local wall-clock time,
// given as fields labelled UTC.
//
// The latest onset at or before wall decides the offset. A wall time that
// is skipped when clocks go forward gets the offset from before the change.
// A wall time that occurs twice when clocks go back gets the earlier one,
// which falls out of comparing against the onset in TZOFFSETFROM.
func (z *vtz) offsetAt(wall time.Time) time.Duration {
	onsets := z.onsets(wall.Year())
	i := sort.Search(len(onsets), func(i int) bool { return onsets[i].wall.After(wall) })
	if i == 0 {
		return z.before
	}
	o := onsets[i-1]
	if o.to > o.from && wall.Before(o.wall.Add(o.to-o.from)) {
		return o.from
	}
	return o.to
}

// offsetOf returns the UTC offset in effect at an instant. It is the
// inverse of offsetAt, which starts from a wall time: Instant uses
// offsetAt, WallOf uses offsetOf. Unlike offsetAt, it is never ambiguous:
// the latest onset at or before the instant, each onset's wall time read
// in its TZOFFSETFROM, decides.
func (z *vtz) offsetOf(instant time.Time) time.Duration {
	u := instant.UTC()
	off := z.before
	var latest time.Time
	found := false
	for _, o := range z.onsets(u.Year()) {
		at := o.wall.Add(-o.from)
		if !at.After(u) && (!found || !at.Before(latest)) {
			off, latest, found = o.to, at, true
		}
	}
	return off
}

// onsets returns, sorted by wall time, every onset from the start of the
// year before year's block of blockYears years to the end of the year
// after it, so at least from year-1 to year+1, preceded by the latest onset
// of each observance before that window. Results are cached per block.
// Each block expanded counts towards the zone's budget; once that is
// spent, the zone is dropped and onsets returns nil.
//
// A rule that fails here, by a panic in the library or by running into
// maxRuleSteps, already passed the checks at parse time, so the failure
// would only repeat. Its observance keeps its DTSTART and RDATE onsets but
// loses the rule for good, and the cache is cleared so that no year mixes
// results from before and after.
func (z *vtz) onsets(year int) []onset {
	z.mu.Lock()
	defer z.mu.Unlock()
	block := year / blockYears
	if year < 0 && year%blockYears != 0 {
		block--
	}
	if s, ok := z.blocks[block]; ok {
		return s
	}
	if z.dropped.Load() {
		return nil
	}
	first := block * blockYears
	lo := time.Date(first-1, 1, 1, 0, 0, 0, 0, time.UTC)
	hi := time.Date(first+blockYears+1, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []onset
	for i := range z.obs {
		o := &z.obs[i]
		var prior time.Time
		hasPrior := false
		var in []time.Time
		add := func(t time.Time) {
			switch {
			case !t.Before(hi):
			case t.Before(lo):
				if !hasPrior || t.After(prior) {
					prior, hasPrior = t, true
				}
			default:
				in = append(in, t)
			}
		}
		if o.rule != nil {
			// DTSTART is added below; the rule only adds later onsets.
			work, err := expandBack(*o.rule, lo, hi, func(t time.Time) bool {
				if !t.Equal(o.start) {
					add(t)
				}
				return true
			}, func() bool { return hasPrior })
			if z.work += work; z.work > z.budget {
				// The caller sees dropped and resolves the TZID
				// without this zone, so the rest is not needed.
				z.dropped.Store(true)
				clear(z.blocks)
				return nil
			}
			if err != nil {
				o.rule = nil
				clear(z.blocks)
				prior, hasPrior, in = time.Time{}, false, nil
			}
		}
		add(o.start)
		for _, t := range o.rdates {
			add(t)
		}
		if hasPrior {
			in = append(in, prior)
		}
		for _, t := range in {
			out = append(out, onset{t, o.from, o.to})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].wall.Before(out[j].wall) })
	if len(z.blocks) >= maxCachedBlocks {
		clear(z.blocks)
	}
	z.blocks[block] = out
	return out
}
