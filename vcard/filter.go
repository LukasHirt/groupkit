// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"errors"
	"fmt"
	"github.com/LukasHirt/groupkit/internal/collation"
	"github.com/LukasHirt/groupkit/internal/contentline"
	"strings"
)

// ErrFilter is wrapped by every error that Filter.Validate returns.
var ErrFilter = errors.New("vcard: invalid filter")

// Values of the test attribute (RFC 6352 section 10.5) for Filter.Test and
// PropFilter.Test. The empty string means TestAnyOf.
const (
	TestAnyOf = "anyof"
	TestAllOf = "allof"
)

// Values of the match-type attribute (RFC 6352 section 10.5.4) for
// TextMatch.MatchType. The empty string means MatchContains.
const (
	MatchEquals     = "equals"
	MatchContains   = "contains"
	MatchStartsWith = "starts-with"
	MatchEndsWith   = "ends-with"
)

// TextMatch is a text comparison. An empty Collation selects
// i;unicode-casemap and an empty MatchType selects contains. Negate inverts
// the result of this one comparison, and it applies to each property value
// on its own: on a card with two EMAIL lines, a negated match that fails on
// one line still matches through the other, so the card matches (SabreDAV
// behaves the same way).
type TextMatch struct {
	Value     string
	Collation string
	MatchType string
	Negate    bool

	// prep is set by Filter.Validate: the resolved collation and the folded
	// value. It is only used while it still describes Value and Collation.
	prep *textPrep
}

// textPrep is a TextMatch prepared by Validate.
type textPrep struct {
	value, collation string // what the preparation was made from
	col              collation.Collation
	folded           string
}

// foldCache holds the folded forms of one text, one per collation, for the
// duration of one property match.
type foldCache struct {
	names []string
	vals  []string
}

func (fc *foldCache) fold(col collation.Collation, s string) string {
	if fc == nil {
		return col.Fold(s)
	}
	name := col.Name()
	for i, n := range fc.names {
		if n == name {
			return fc.vals[i]
		}
	}
	v := col.Fold(s)
	fc.names = append(fc.names, name)
	fc.vals = append(fc.vals, v)
	return v
}

// ParamFilter selects properties by one of their parameters. Name is
// compared case-insensitively (ASCII letters only). IsNotDefined is not
// combined with TextMatch.
type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

// PropFilter selects cards by one property name. Test combines TextMatches
// and Params and is "anyof" when empty; an unknown Test that was never
// validated also falls back to "anyof". All the conditions are tested against
// one property instance (RFC 6352 section 10.5.1), and the filter matches
// when some instance satisfies them. Some servers (SabreDAV, Radicale)
// instead aggregate text matches across the instances of a property; here,
// allof with "first" and "second" does not match a card whose one EMAIL line
// holds "first" and another holds "second".
// IsNotDefined matches a card without the property and is not combined with
// TextMatches or Params.
type PropFilter struct {
	Name         string
	IsNotDefined bool
	Test         string
	TextMatches  []TextMatch
	Params       []ParamFilter
}

// Filter is the filter of an addressbook-query report. Test combines the
// PropFilters and is "anyof" when empty (or unknown and never validated). A
// nil Filter or one without PropFilters matches every card.
type Filter struct {
	Test  string
	Props []PropFilter
}

// Validate reports an unknown test, collation or match type, an empty
// property or parameter name, and IsNotDefined combined with other
// conditions. The returned error wraps ErrFilter. A nil Filter is valid.
func (f *Filter) Validate() error {
	if f == nil {
		return nil
	}
	if err := validTest(f.Test); err != nil {
		return err
	}
	for i := range f.Props {
		if err := f.Props[i].validate(); err != nil {
			return err
		}
	}
	return nil
}

func (pf *PropFilter) validate() error {
	if pf.Name == "" {
		return fmt.Errorf("%w: empty property name", ErrFilter)
	}
	if pf.IsNotDefined && (len(pf.TextMatches) > 0 || len(pf.Params) > 0) {
		return fmt.Errorf("%w: is-not-defined with other conditions on %s", ErrFilter, contentline.Clip(pf.Name))
	}
	if err := validTest(pf.Test); err != nil {
		return err
	}
	for i := range pf.TextMatches {
		if err := pf.TextMatches[i].validate(); err != nil {
			return err
		}
	}
	for i := range pf.Params {
		if err := pf.Params[i].validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p *ParamFilter) validate() error {
	if p.Name == "" {
		return fmt.Errorf("%w: empty parameter name", ErrFilter)
	}
	if p.TextMatch == nil {
		return nil
	}
	if p.IsNotDefined {
		return fmt.Errorf("%w: is-not-defined with a text match on %s", ErrFilter, contentline.Clip(p.Name))
	}
	return p.TextMatch.validate()
}

func validTest(test string) error {
	switch test {
	case "", TestAnyOf, TestAllOf:
		return nil
	}
	return fmt.Errorf("%w: unknown test %q", ErrFilter, contentline.Clip(test))
}

// validate checks the text match and prepares it: the collation is resolved
// and the value folded once, so matching does not repeat either.
func (tm *TextMatch) validate() error {
	col, err := collation.Get(tm.Collation)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFilter, err)
	}
	switch tm.MatchType {
	case "", MatchContains, MatchEquals, MatchStartsWith, MatchEndsWith:
		tm.prep = &textPrep{value: tm.Value, collation: tm.Collation, col: col, folded: col.Fold(tm.Value)}
		return nil
	}
	return fmt.Errorf("%w: unknown match type %q", ErrFilter, contentline.Clip(tm.MatchType))
}

// Match reports whether the card satisfies the filter. A text match with an
// unknown collation or match type never matches. A nil card matches only a
// filter without PropFilters.
func (f *Filter) Match(c *Card) bool {
	if f == nil || len(f.Props) == 0 {
		return true
	}
	if c == nil {
		return false
	}
	results := make([]bool, len(f.Props))
	for i, pf := range f.Props {
		results[i] = pf.match(c)
	}
	return combine(f.Test, results)
}

// combine applies allof, or anyof when test is anything else. An empty list
// is true, so a property filter without conditions matches on existence.
func combine(test string, results []bool) bool {
	if len(results) == 0 {
		return true
	}
	all := test == TestAllOf
	for _, r := range results {
		if r != all {
			return !all
		}
	}
	return all
}

func (pf PropFilter) match(c *Card) bool {
	props := c.Find(pf.Name)
	if pf.IsNotDefined {
		return len(props) == 0
	}
	for _, p := range props {
		if pf.matchProperty(p) {
			return true
		}
	}
	return false
}

func (pf PropFilter) matchProperty(p Property) bool {
	results := make([]bool, 0, len(pf.TextMatches)+len(pf.Params))
	text := p.Text()
	var fc foldCache // folds text once per collation for all the text-matches
	for i := range pf.TextMatches {
		results = append(results, pf.TextMatches[i].matches(text, &fc))
	}
	for _, param := range pf.Params {
		results = append(results, param.matches(p))
	}
	return combine(pf.Test, results)
}

func (pf ParamFilter) matches(p Property) bool {
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
		if pf.TextMatch.matches(v, nil) {
			return true
		}
	}
	return false
}

func (tm *TextMatch) matches(s string, fc *foldCache) bool {
	var col collation.Collation
	var needle string
	if p := tm.prep; p != nil && p.value == tm.Value && p.collation == tm.Collation {
		col, needle = p.col, p.folded
	} else {
		var err error
		col, err = collation.Get(tm.Collation)
		if err != nil {
			return false
		}
		needle = col.Fold(tm.Value)
	}
	hay := fc.fold(col, s)
	var ok bool
	switch tm.MatchType {
	case "", MatchContains:
		ok = strings.Contains(hay, needle)
	case MatchEquals:
		ok = hay == needle
	case MatchStartsWith:
		ok = strings.HasPrefix(hay, needle)
	case MatchEndsWith:
		ok = strings.HasSuffix(hay, needle)
	default:
		return false
	}
	return ok != tm.Negate
}
