// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LukasHirt/groupkit/internal/collation"
	"github.com/LukasHirt/groupkit/internal/contentline"
)

// ErrFilter is wrapped by every error that CompFilter.Validate returns.
var ErrFilter = errors.New("ical: invalid filter")

// Values of the match-type attribute (RFC 4791 section 9.7.5, extended by
// RFC 6352) for TextMatch.MatchType. The empty string means MatchContains.
const (
	MatchEquals     = "equals"
	MatchContains   = "contains"
	MatchStartsWith = "starts-with"
	MatchEndsWith   = "ends-with"
)

// Collations for TextMatch.Collation. The empty string means
// CollationUnicode. Names are matched with ASCII letters folded.
const (
	CollationOctet   = "i;octet"
	CollationASCII   = "i;ascii-casemap"
	CollationUnicode = "i;unicode-casemap"
)

// Limits on the size of a filter, which comes from a client.
const (
	// maxFilterDepth caps the nesting of comp-filters, the outer one
	// included.
	maxFilterDepth = 16
	// maxFilterNodes caps the comp-, prop- and param-filters together.
	maxFilterNodes = 1000
	// maxTimeRanges caps the comp-filter time ranges. Each distinct one
	// expands the object's recurrences; clients send one.
	maxTimeRanges = 16
)

// TimeRange is the time-range element of a query. Start and End are
// instants; a zero one leaves that side open. End must not be before Start.
// On a VEVENT comp-filter an open side reaches at most 100 years from the
// other bound, or from DTSTART when both are open (see
// Calendar.Occurrences); a range with both sides open matches every VEVENT.
type TimeRange struct {
	Start, End time.Time
}

// TextMatch is a text comparison. An empty Collation selects
// i;unicode-casemap and an empty MatchType selects contains. Negate inverts
// the result of this one comparison, and it applies to each property value
// on its own: an event with two ATTENDEE lines matches a negated match that
// fails on one of them through the other.
type TextMatch struct {
	Value     string
	Collation string
	MatchType string
	Negate    bool
}

// ParamFilter selects properties by one of their parameters. Name is
// compared case-insensitively (ASCII letters only). A parameter with several
// values matches when any value does. IsNotDefined is not combined with
// TextMatch.
type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

// PropFilter selects components by one property name, compared
// case-insensitively (ASCII letters only). TimeRange, TextMatch and Params
// are all tested against one property instance, and the filter matches when
// some instance satisfies them all; without any of them it matches when the
// property exists. IsNotDefined matches a component without the property
// and is not combined with the other conditions.
//
// TextMatch compares the value with its backslash escapes resolved, so
// "Team, meeting" matches SUMMARY:Team\, meeting. TimeRange matches a
// date-time property (DTSTART, DTEND, DUE, DTSTAMP, CREATED, LAST-MODIFIED,
// COMPLETED, RECURRENCE-ID or ACKNOWLEDGED) whose instant lies in
// [Start, End); a DATE stands for midnight in the floating location. Other
// properties, and values that do not parse, never match a TimeRange.
type PropFilter struct {
	Name         string
	IsNotDefined bool
	TimeRange    *TimeRange
	TextMatch    *TextMatch
	Params       []ParamFilter
}

// CompFilter selects components by name, compared case-insensitively (ASCII
// letters only). The outer filter of a calendar-query names VCALENDAR and
// is tested against the calendar itself; a nested one matches when some
// child component of its name satisfies all of its Props, Comps and
// TimeRange, or, with IsNotDefined, when there is no such child.
// IsNotDefined is not combined with the other conditions.
//
// A TimeRange applies to VEVENTs at the top level of the calendar object:
// it holds when the object's recurrence set, overrides included, has an
// occurrence that overlaps the range (see Calendar.Occurrences). It is a
// property of the whole object, so with several VEVENTs (a master and its
// overrides) the Props of the filter may be satisfied by a different VEVENT
// than the occurrence in the range. A TimeRange with both sides open
// matches every VEVENT. On any other component, VTODO and VALARM included,
// a TimeRange matches nothing. A filter holds at most 16 comp-filter time
// ranges, since each distinct one expands the object's recurrences.
type CompFilter struct {
	Name         string
	IsNotDefined bool
	TimeRange    *TimeRange
	Props        []PropFilter
	Comps        []CompFilter
}

// Validate reports a nil filter, an outer filter that does not name
// VCALENDAR, an empty name, an unknown collation or match type, a time
// range whose End is before its Start, IsNotDefined combined with other
// conditions, and a filter that nests comp-filters more than 16 deep, has
// more than 1000 comp-, prop- and param-filters, or has more than 16
// comp-filter time ranges. The returned error wraps ErrFilter.
func (f *CompFilter) Validate() error {
	if f == nil {
		return fmt.Errorf("%w: no filter", ErrFilter)
	}
	if !contentline.EqualFoldASCII(f.Name, "VCALENDAR") {
		return fmt.Errorf("%w: outer filter names %q, not VCALENDAR", ErrFilter, contentline.Clip(f.Name))
	}
	var n counts
	return f.validate(1, &n)
}

// counts tallies a filter's elements during Validate.
type counts struct {
	nodes, timeRanges int
}

func (n *counts) node() error {
	n.nodes++
	if n.nodes > maxFilterNodes {
		return fmt.Errorf("%w: more than %d filter elements", ErrFilter, maxFilterNodes)
	}
	return nil
}

func (f *CompFilter) validate(depth int, nodes *counts) error {
	if depth > maxFilterDepth {
		return fmt.Errorf("%w: comp-filters nested more than %d deep", ErrFilter, maxFilterDepth)
	}
	if err := nodes.node(); err != nil {
		return err
	}
	if f.Name == "" {
		return fmt.Errorf("%w: empty component name", ErrFilter)
	}
	if f.IsNotDefined && (f.TimeRange != nil || len(f.Props) > 0 || len(f.Comps) > 0) {
		return fmt.Errorf("%w: is-not-defined with other conditions on %s", ErrFilter, contentline.Clip(f.Name))
	}
	if err := f.TimeRange.validate(); err != nil {
		return err
	}
	if f.TimeRange != nil {
		if nodes.timeRanges++; nodes.timeRanges > maxTimeRanges {
			return fmt.Errorf("%w: more than %d comp-filter time ranges", ErrFilter, maxTimeRanges)
		}
	}
	for i := range f.Props {
		if err := f.Props[i].validate(nodes); err != nil {
			return err
		}
	}
	for i := range f.Comps {
		if err := f.Comps[i].validate(depth+1, nodes); err != nil {
			return err
		}
	}
	return nil
}

func (pf *PropFilter) validate(nodes *counts) error {
	if err := nodes.node(); err != nil {
		return err
	}
	if pf.Name == "" {
		return fmt.Errorf("%w: empty property name", ErrFilter)
	}
	if pf.IsNotDefined && (pf.TimeRange != nil || pf.TextMatch != nil || len(pf.Params) > 0) {
		return fmt.Errorf("%w: is-not-defined with other conditions on %s", ErrFilter, contentline.Clip(pf.Name))
	}
	if err := pf.TimeRange.validate(); err != nil {
		return err
	}
	if err := pf.TextMatch.validate(); err != nil {
		return err
	}
	for i := range pf.Params {
		if err := pf.Params[i].validate(nodes); err != nil {
			return err
		}
	}
	return nil
}

func (p *ParamFilter) validate(nodes *counts) error {
	if err := nodes.node(); err != nil {
		return err
	}
	if p.Name == "" {
		return fmt.Errorf("%w: empty parameter name", ErrFilter)
	}
	if p.IsNotDefined && p.TextMatch != nil {
		return fmt.Errorf("%w: is-not-defined with a text match on %s", ErrFilter, contentline.Clip(p.Name))
	}
	return p.TextMatch.validate()
}

func (tr *TimeRange) validate() error {
	if tr != nil && !tr.Start.IsZero() && !tr.End.IsZero() && tr.End.Before(tr.Start) {
		return fmt.Errorf("%w: time range ends before it starts", ErrFilter)
	}
	return nil
}

func (tm *TextMatch) validate() error {
	if tm == nil {
		return nil
	}
	if _, err := collation.Get(tm.Collation); err != nil {
		return fmt.Errorf("%w: %w", ErrFilter, err)
	}
	switch tm.MatchType {
	case "", MatchContains, MatchEquals, MatchStartsWith, MatchEndsWith:
		return nil
	}
	return fmt.Errorf("%w: unknown match type %q", ErrFilter, contentline.Clip(tm.MatchType))
}

// Matches reports whether the calendar object satisfies the calendar-query
// filter f, whose outer element names VCALENDAR. It returns an error only
// for an invalid filter, one that Validate rejects. The calendar's content
// never causes an error: a VEVENT whose recurrence cannot be expanded (see
// ErrRecurrence) does not match a time range, and an unknown TZID is read
// as UTC, so one odd object does not fail a query over a collection.
//
// Floating times and dates are read in floating, UTC when nil. All the time
// ranges of one call share one Resolver for the calendar's VTIMEZONEs.
// Each value is folded once per collation for text matches, and one call
// folds at most 64 MiB; past that, no text match holds, negated or not.
// Matches only reads c, so concurrent calls on one Calendar are safe. A nil
// Calendar matches nothing.
func (c *Calendar) Matches(f *CompFilter, floating *time.Location) (bool, error) {
	if err := f.Validate(); err != nil {
		return false, err
	}
	if c == nil || f.IsNotDefined {
		return false, nil
	}
	m := &matcher{cal: c, res: NewResolver(c), floating: floating}
	return m.component(&c.Component, f, false), nil
}

// matcher holds the state of one Matches call.
type matcher struct {
	cal      *Calendar
	res      *Resolver
	floating *time.Location
	// events caches whether the calendar's VEVENTs occur in a range.
	events map[TimeRange]bool
	// collations caches collation.Get by name.
	collations map[string]collation.Collation
	// folded caches property values unescaped and folded, and patterns
	// the folded text match values.
	folded   map[foldKey]string
	patterns map[*TextMatch]string
	// foldedBytes counts the bytes folded so far, against maxFoldBytes.
	foldedBytes int
}

// foldKey names one property value folded by one collation.
type foldKey struct {
	p   *Property
	col string
}

// maxFoldBytes bounds the text one Matches call folds for text matches.
// A filter can test one long value many times with different collations,
// or many long values. It is a variable so tests can lower it.
var maxFoldBytes = 64 << 20

// textFolds counts the property and parameter values folded, for tests
// that check how much work a query does.
var textFolds atomic.Int64

// component reports whether comp satisfies the conditions of f other than
// its name. topLevel is true for a direct child of the calendar.
func (m *matcher) component(comp *Component, f *CompFilter, topLevel bool) bool {
	for i := range f.Props {
		if !m.prop(comp, &f.Props[i]) {
			return false
		}
	}
	for i := range f.Comps {
		if !m.child(comp, &f.Comps[i], comp == &m.cal.Component) {
			return false
		}
	}
	if f.TimeRange != nil {
		return topLevel && contentline.EqualFoldASCII(comp.Name, "VEVENT") && m.eventsIn(*f.TimeRange)
	}
	return true
}

// child reports whether some child of parent named f.Name satisfies f, or,
// with IsNotDefined, whether there is no such child.
func (m *matcher) child(parent *Component, f *CompFilter, topLevel bool) bool {
	kids := parent.Comps(f.Name)
	if f.IsNotDefined {
		return len(kids) == 0
	}
	for _, k := range kids {
		if m.component(k, f, topLevel) {
			return true
		}
	}
	return false
}

// eventsIn reports whether the calendar's VEVENTs have an occurrence that
// overlaps tr. A recurrence that cannot be expanded has none.
func (m *matcher) eventsIn(tr TimeRange) bool {
	if tr.Start.IsZero() && tr.End.IsZero() {
		return true
	}
	// Strip monotonic readings and locations so equal instants share a key.
	key := TimeRange{Start: tr.Start.Round(0).UTC(), End: tr.End.Round(0).UTC()}
	if hit, ok := m.events[key]; ok {
		return hit
	}
	occs, err := m.cal.Occurrences(m.res, m.floating, Range(key))
	hit := err == nil && len(occs) > 0
	if m.events == nil {
		m.events = map[TimeRange]bool{}
	}
	m.events[key] = hit
	return hit
}

func (m *matcher) prop(comp *Component, f *PropFilter) bool {
	found := false
	for i := range comp.Props {
		p := &comp.Props[i]
		if !contentline.EqualFoldASCII(p.Name, f.Name) {
			continue
		}
		found = true
		if !f.IsNotDefined && m.property(p, f) {
			return true
		}
	}
	return f.IsNotDefined && !found
}

// property reports whether one property instance satisfies all the
// conditions of f.
func (m *matcher) property(p *Property, f *PropFilter) bool {
	if f.TextMatch != nil && !m.text(f.TextMatch, p, "") {
		return false
	}
	for i := range f.Params {
		if !m.param(&f.Params[i], p) {
			return false
		}
	}
	if f.TimeRange != nil {
		t, ok := m.instant(p)
		return ok && inRange(t, *f.TimeRange)
	}
	return true
}

// dateTimeProps are the properties whose value is a single DATE or
// DATE-TIME, which a property time range can test.
var dateTimeProps = []string{
	"DTSTART", "DTEND", "DUE", "DTSTAMP", "CREATED",
	"LAST-MODIFIED", "COMPLETED", "RECURRENCE-ID", "ACKNOWLEDGED",
}

// isDateTimeProp matches name against dateTimeProps, folding ASCII letters
// only, as property names are matched everywhere else.
func isDateTimeProp(name string) bool {
	for _, n := range dateTimeProps {
		if contentline.EqualFoldASCII(name, n) {
			return true
		}
	}
	return false
}

// instant returns the instant of a date-time property, and false for any
// other property or a value that does not parse.
func (m *matcher) instant(p *Property) (time.Time, bool) {
	if !isDateTimeProp(p.Name) {
		return time.Time{}, false
	}
	v, err := ParseDateTime(p.Value, p.Params)
	if err != nil {
		return time.Time{}, false
	}
	return m.res.Instant(v, m.floating), true
}

// inRange reports whether t lies in [tr.Start, tr.End), a zero side being
// open.
func inRange(t time.Time, tr TimeRange) bool {
	return (tr.Start.IsZero() || !t.Before(tr.Start)) && (tr.End.IsZero() || t.Before(tr.End))
}

func (m *matcher) param(pf *ParamFilter, p *Property) bool {
	var values []string
	found := false
	for name, v := range p.Params {
		if contentline.EqualFoldASCII(name, pf.Name) {
			values, found = append(values, v...), true
		}
	}
	if pf.IsNotDefined {
		return !found
	}
	if !found {
		return false
	}
	if pf.TextMatch == nil {
		return true
	}
	for _, v := range values {
		if m.text(pf.TextMatch, nil, v) {
			return true
		}
	}
	return false
}

// text compares a value with the text match: the unescaped value of p,
// or s when p is nil. A collation or match type that Validate would reject
// never matches. Folded property values are cached for the call, and once
// the call has folded more than maxFoldBytes, no text matches, negated or
// not.
func (m *matcher) text(tm *TextMatch, p *Property, s string) bool {
	col, ok := m.collations[tm.Collation]
	if !ok {
		var err error
		if col, err = collation.Get(tm.Collation); err != nil {
			return false
		}
		if m.collations == nil {
			m.collations = map[string]collation.Collation{}
		}
		m.collations[tm.Collation] = col
	}
	key := foldKey{p, col.Name()}
	folded, ok := m.folded[key]
	if p == nil || !ok {
		if p != nil {
			s = p.Text()
		}
		if folded, ok = m.fold(col, s); !ok {
			return false
		}
		if p != nil {
			if m.folded == nil {
				m.folded = map[foldKey]string{}
			}
			m.folded[key] = folded
		}
		textFolds.Add(1)
	}
	pattern, ok := m.patterns[tm]
	if !ok {
		if pattern, ok = m.fold(col, tm.Value); !ok {
			return false
		}
		if m.patterns == nil {
			m.patterns = map[*TextMatch]string{}
		}
		m.patterns[tm] = pattern
	}
	switch tm.MatchType {
	case "", MatchContains:
		ok = strings.Contains(folded, pattern)
	case MatchEquals:
		ok = folded == pattern
	case MatchStartsWith:
		ok = strings.HasPrefix(folded, pattern)
	case MatchEndsWith:
		ok = strings.HasSuffix(folded, pattern)
	default:
		return false
	}
	return ok != tm.Negate
}

// fold folds s with col, and returns false instead when that would take
// the call past maxFoldBytes.
func (m *matcher) fold(col collation.Collation, s string) (string, bool) {
	m.foldedBytes += len(s)
	if m.foldedBytes > maxFoldBytes {
		return "", false
	}
	return col.Fold(s), true
}
