// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"errors"
	"fmt"

	"github.com/LukasHirt/groupkit/internal/collation"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/vcard"
)

// Limits on the size of a query filter, so a hostile body cannot make every
// object cost a long evaluation.
const (
	maxPropFilters  = 100
	maxTextMatches  = 100
	maxParamFilters = 100

	// maxTextMatchBytes is the longest value of one text-match and
	// maxNeedleBytes the sum of all of them in one filter.
	maxTextMatchBytes = 8 << 10
	maxNeedleBytes    = 64 << 10
)

// errFilter marks a structural problem in a filter. Like vcard.ErrFilter it
// answers 403 valid-filter.
var errFilter = errors.New("carddav: invalid filter")

func filterErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errFilter, fmt.Sprintf(format, args...))
}

// filterError maps an error from parseFilter to the response it deserves: an
// unknown collation is supported-collation, anything else valid-filter.
func filterError(err error) *dav.Error {
	if errors.Is(err, collation.ErrUnknown) {
		return dav.Forbidden(dav.CRSupportedCollation)
	}
	return dav.Forbidden(dav.CRValidFilter)
}

// filterCounts tracks how much of the limits a filter has used.
type filterCounts struct{ props, texts, params, needle int }

// parseFilter reads a CR:filter element (RFC 6352 section 10.5) and checks it
// with Filter.Validate. The returned error wraps errFilter or vcard.ErrFilter;
// use filterError to answer it.
func parseFilter(n *dav.Node) (*vcard.Filter, error) {
	if n.Name != dav.CRFilter {
		return nil, filterErr("not a filter element")
	}
	test, err := testAttr(n)
	if err != nil {
		return nil, err
	}
	f := &vcard.Filter{Test: test}
	var c filterCounts
	for _, k := range n.Children {
		if k.Name != dav.CRPropFilter {
			return nil, filterErr("unexpected element %s in filter", k.Name.Local)
		}
		if c.props++; c.props > maxPropFilters {
			return nil, filterErr("more than %d prop-filters", maxPropFilters)
		}
		pf, err := parsePropFilter(k, &c)
		if err != nil {
			return nil, err
		}
		f.Props = append(f.Props, pf)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func testAttr(n *dav.Node) (string, error) {
	switch t := n.Attr("test"); t {
	case "", vcard.TestAnyOf, vcard.TestAllOf:
		return t, nil
	default:
		return "", filterErr("unknown test")
	}
}

func parsePropFilter(n *dav.Node, c *filterCounts) (vcard.PropFilter, error) {
	test, err := testAttr(n)
	if err != nil {
		return vcard.PropFilter{}, err
	}
	pf := vcard.PropFilter{Name: n.Attr("name"), Test: test}
	if pf.Name == "" {
		return pf, filterErr("prop-filter without a name")
	}
	for _, k := range n.Children {
		switch k.Name {
		case dav.CRIsNotDefined:
			pf.IsNotDefined = true
		case dav.CRTextMatch:
			tm, err := parseTextMatch(k, c)
			if err != nil {
				return pf, err
			}
			pf.TextMatches = append(pf.TextMatches, tm)
		case dav.CRParamFilter:
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

func parseParamFilter(n *dav.Node, c *filterCounts) (vcard.ParamFilter, error) {
	if c.params++; c.params > maxParamFilters {
		return vcard.ParamFilter{}, filterErr("more than %d param-filters", maxParamFilters)
	}
	p := vcard.ParamFilter{Name: n.Attr("name")}
	if p.Name == "" {
		return p, filterErr("param-filter without a name")
	}
	for _, k := range n.Children {
		switch k.Name {
		case dav.CRIsNotDefined:
			p.IsNotDefined = true
		case dav.CRTextMatch:
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

func parseTextMatch(n *dav.Node, c *filterCounts) (vcard.TextMatch, error) {
	if c.texts++; c.texts > maxTextMatches {
		return vcard.TextMatch{}, filterErr("more than %d text-matches", maxTextMatches)
	}
	if len(n.Text) > maxTextMatchBytes {
		return vcard.TextMatch{}, filterErr("text-match longer than %d bytes", maxTextMatchBytes)
	}
	if c.needle += len(n.Text); c.needle > maxNeedleBytes {
		return vcard.TextMatch{}, filterErr("text-matches longer than %d bytes together", maxNeedleBytes)
	}
	tm := vcard.TextMatch{Value: n.Text, Collation: n.Attr("collation"), MatchType: n.Attr("match-type")}
	switch n.Attr("negate-condition") {
	case "", "no":
	case "yes":
		tm.Negate = true
	default:
		return tm, filterErr("bad negate-condition")
	}
	return tm, nil
}
