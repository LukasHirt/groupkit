// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"errors"
	"fmt"
	"time"

	"github.com/LukasHirt/groupkit/ical"
	"github.com/LukasHirt/groupkit/internal/collation"
	"github.com/LukasHirt/groupkit/internal/dav"
)

// Limits on the size of a query filter, so a hostile body cannot make every
// object cost a long evaluation. ical.CompFilter.Validate adds its own: 16
// levels of comp-filters, 1000 filter elements and 16 comp-filter time
// ranges.
const (
	maxPropFilters  = 100
	maxTextMatches  = 100
	maxParamFilters = 100

	// maxTextMatchBytes is the longest value of one text-match and
	// maxNeedleBytes the sum of all of them in one filter.
	maxTextMatchBytes = 8 << 10
	maxNeedleBytes    = 64 << 10

	// maxFilterDepth is the deepest nesting of comp-filters the parser
	// follows, the outer one included. It matches the ical limit and keeps a
	// hostile body from recursing further.
	maxFilterDepth = 16
)

// timeFormat is the UTC form of time-range bounds (RFC 4791 section 9.9).
const timeFormat = "20060102T150405Z"

// errFilter marks a structural problem in a filter. Like ical.ErrFilter it
// answers 403 valid-filter.
var errFilter = errors.New("caldav: invalid filter")

func filterErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errFilter, fmt.Sprintf(format, args...))
}

// filterError maps an error from parseCompFilter to the response it
// deserves: a malformed time-range value is the 400 it carries, an unknown
// collation is supported-collation, anything else valid-filter.
func filterError(err error) *dav.Error {
	var de *dav.Error
	if errors.As(err, &de) {
		return de
	}
	if errors.Is(err, collation.ErrUnknown) {
		return dav.Forbidden(dav.CalSupportedCollation)
	}
	return dav.Forbidden(dav.CalValidFilter)
}

// filterCounts tracks how much of the limits a filter has used.
type filterCounts struct{ props, texts, params, needle int }

// parseCompFilter reads a C:filter element (RFC 4791 section 9.7) and checks
// the result with CompFilter.Validate. The filter holds exactly one
// top-level comp-filter, which must name VCALENDAR. A filter without any
// child selects every object and yields nil, nil.
//
// An unparsable time-range value is a *dav.Error with status 400; any other
// error wraps errFilter or ical.ErrFilter (use filterError to answer them).
//
// Limitations: C:timezone and C:timezone-id in the query body, and the
// expand, limit-recurrence-set, limit-freebusy-set and partial calendar-data
// children of a calendar-data property, are not read. The full stored object
// is returned and floating times are read in UTC. The match-type attribute of
// a text-match is not part of CalDAV and is ignored: the match is "contains".
func parseCompFilter(n *dav.Node) (*ical.CompFilter, error) {
	if n.Name != dav.CalFilter {
		return nil, filterErr("not a filter element")
	}
	if len(n.Children) == 0 {
		return nil, nil
	}
	if len(n.Children) > 1 || n.Children[0].Name != dav.CalCompFilter {
		return nil, filterErr("a filter holds exactly one comp-filter")
	}
	var c filterCounts
	f, err := parseComp(n.Children[0], 1, &c)
	if err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func parseComp(n *dav.Node, depth int, c *filterCounts) (*ical.CompFilter, error) {
	if depth > maxFilterDepth {
		return nil, filterErr("comp-filters nested more than %d deep", maxFilterDepth)
	}
	f := &ical.CompFilter{Name: n.Attr("name")}
	if f.Name == "" {
		return nil, filterErr("comp-filter without a name")
	}
	for _, k := range n.Children {
		switch k.Name {
		case dav.CalIsNotDefined:
			f.IsNotDefined = true
		case dav.CalTimeRange:
			if f.TimeRange != nil {
				return nil, filterErr("more than one time-range in a comp-filter")
			}
			tr, err := parseTimeRange(k)
			if err != nil {
				return nil, err
			}
			f.TimeRange = tr
		case dav.CalPropFilter:
			if c.props++; c.props > maxPropFilters {
				return nil, filterErr("more than %d prop-filters", maxPropFilters)
			}
			pf, err := parsePropFilter(k, c)
			if err != nil {
				return nil, err
			}
			f.Props = append(f.Props, pf)
		case dav.CalCompFilter:
			cf, err := parseComp(k, depth+1, c)
			if err != nil {
				return nil, err
			}
			f.Comps = append(f.Comps, *cf)
		default:
			return nil, filterErr("unexpected element %s in comp-filter", k.Name.Local)
		}
	}
	return f, nil
}

func parsePropFilter(n *dav.Node, c *filterCounts) (ical.PropFilter, error) {
	pf := ical.PropFilter{Name: n.Attr("name")}
	if pf.Name == "" {
		return pf, filterErr("prop-filter without a name")
	}
	for _, k := range n.Children {
		switch k.Name {
		case dav.CalIsNotDefined:
			pf.IsNotDefined = true
		case dav.CalTimeRange:
			if pf.TimeRange != nil {
				return pf, filterErr("more than one time-range in a prop-filter")
			}
			tr, err := parseTimeRange(k)
			if err != nil {
				return pf, err
			}
			pf.TimeRange = tr
		case dav.CalTextMatch:
			if pf.TextMatch != nil {
				return pf, filterErr("more than one text-match in a prop-filter")
			}
			tm, err := parseTextMatch(k, c)
			if err != nil {
				return pf, err
			}
			pf.TextMatch = &tm
		case dav.CalParamFilter:
			p, err := parseParamFilter(k, c)
			if err != nil {
				return pf, err
			}
			pf.Params = append(pf.Params, p)
		default:
			return pf, filterErr("unexpected element %s in prop-filter", k.Name.Local)
		}
	}
	return pf, nil
}

func parseParamFilter(n *dav.Node, c *filterCounts) (ical.ParamFilter, error) {
	if c.params++; c.params > maxParamFilters {
		return ical.ParamFilter{}, filterErr("more than %d param-filters", maxParamFilters)
	}
	p := ical.ParamFilter{Name: n.Attr("name")}
	if p.Name == "" {
		return p, filterErr("param-filter without a name")
	}
	for _, k := range n.Children {
		switch k.Name {
		case dav.CalIsNotDefined:
			p.IsNotDefined = true
		case dav.CalTextMatch:
			if p.TextMatch != nil {
				return p, filterErr("more than one text-match in a param-filter")
			}
			tm, err := parseTextMatch(k, c)
			if err != nil {
				return p, err
			}
			p.TextMatch = &tm
		default:
			return p, filterErr("unexpected element %s in param-filter", k.Name.Local)
		}
	}
	return p, nil
}

// parseTextMatch reads a C:text-match. RFC 4791 section 9.7.5 makes
// i;ascii-casemap the default collation, which ical would otherwise replace
// by i;unicode-casemap, so the default is set here.
func parseTextMatch(n *dav.Node, c *filterCounts) (ical.TextMatch, error) {
	if c.texts++; c.texts > maxTextMatches {
		return ical.TextMatch{}, filterErr("more than %d text-matches", maxTextMatches)
	}
	if len(n.Text) > maxTextMatchBytes {
		return ical.TextMatch{}, filterErr("text-match longer than %d bytes", maxTextMatchBytes)
	}
	if c.needle += len(n.Text); c.needle > maxNeedleBytes {
		return ical.TextMatch{}, filterErr("text-matches longer than %d bytes together", maxNeedleBytes)
	}
	tm := ical.TextMatch{Value: n.Text, Collation: n.Attr("collation"), MatchType: ical.MatchContains}
	if tm.Collation == "" {
		tm.Collation = ical.CollationASCII
	}
	switch n.Attr("negate-condition") {
	case "", "no":
	case "yes":
		tm.Negate = true
	default:
		return tm, filterErr("bad negate-condition")
	}
	return tm, nil
}

// parseTimeRange reads a C:time-range. A missing bound is open, but at least
// one must be given. A value that is not a UTC date-time is a 400.
func parseTimeRange(n *dav.Node) (*ical.TimeRange, error) {
	var tr ical.TimeRange
	s, e := n.Attr("start"), n.Attr("end")
	if s == "" && e == "" {
		return nil, dav.BadRequest("time-range without start or end")
	}
	var err error
	if s != "" {
		if tr.Start, err = time.Parse(timeFormat, s); err != nil {
			return nil, dav.BadRequest("invalid time-range start")
		}
		tr.Start = explicitBound(tr.Start)
	}
	if e != "" {
		if tr.End, err = time.Parse(timeFormat, e); err != nil {
			return nil, dav.BadRequest("invalid time-range end")
		}
		tr.End = explicitBound(tr.End)
	}
	return &tr, nil
}

// explicitBound keeps a bound that was given from reading as open: ical
// treats the zero time.Time (0001-01-01T00:00:00Z) as an open side, so a
// given bound at or before it moves to one nanosecond after it.
func explicitBound(t time.Time) time.Time {
	if t.After(time.Time{}) {
		return t
	}
	return time.Time{}.Add(time.Nanosecond)
}
