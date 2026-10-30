// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// ErrValue reports a DATE, DATE-TIME or DURATION value that is malformed.
var ErrValue = errors.New("ical: invalid value")

// Value is a parsed DATE or DATE-TIME.
//
// Wall holds the year to second fields as a wall-clock time labelled UTC. It
// is an instant only when UTC is true; otherwise it needs a zone to mean
// anything. For a DATE, Date is true and Wall is midnight of that day.
type Value struct {
	// Wall is the date and time as a wall-clock time labelled UTC.
	Wall time.Time
	// Date is true for a DATE value.
	Date bool
	// UTC is true for a time with the Z suffix.
	UTC bool
	// TZID is the first value of the TZID parameter, for a local time tied to
	// a zone. It is empty for floating and UTC values.
	TZID string
}

func valueErr(raw, why string) error {
	return fmt.Errorf("%w: %s: %q", ErrValue, why, contentline.Clip(raw))
}

// ParseDateTime parses YYYYMMDD, YYYYMMDDTHHMMSS or YYYYMMDDTHHMMSSZ.
// VALUE=DATE in params forces a DATE; VALUE=DATE-TIME rejects one.
func ParseDateTime(raw string, params map[string][]string) (Value, error) {
	kind := ""
	if v := params["VALUE"]; len(v) > 0 {
		kind = upperASCII(v[0])
	}
	var v Value
	switch len(raw) {
	case 8:
		if kind == "DATE-TIME" {
			return v, valueErr(raw, "date given for a date-time")
		}
		v.Date = true
	case 15, 16:
		if kind == "DATE" {
			return v, valueErr(raw, "date-time given for a date")
		}
		if raw[8] != 'T' {
			return v, valueErr(raw, "missing T")
		}
		if len(raw) == 16 {
			if raw[15] != 'Z' {
				return v, valueErr(raw, "bad suffix")
			}
			v.UTC = true
		}
	default:
		return v, valueErr(raw, "bad length")
	}
	var f [6]int
	pos := 0
	for i, n := range [6]int{4, 2, 2, 2, 2, 2} {
		if i >= 3 && v.Date {
			break
		}
		if i == 3 {
			pos++ // skip the T
		}
		for j := 0; j < n; j++ {
			c := raw[pos]
			if c < '0' || c > '9' {
				return Value{}, valueErr(raw, "not a digit")
			}
			f[i] = f[i]*10 + int(c-'0')
			pos++
		}
	}
	w := time.Date(f[0], time.Month(f[1]), f[2], f[3], f[4], f[5], 0, time.UTC)
	if w.Year() != f[0] || int(w.Month()) != f[1] || w.Day() != f[2] ||
		w.Hour() != f[3] || w.Minute() != f[4] || w.Second() != f[5] {
		return Value{}, valueErr(raw, "no such date or time")
	}
	v.Wall = w
	if tz := params["TZID"]; len(tz) > 0 && !v.Date && !v.UTC {
		v.TZID = tz[0]
	}
	return v, nil
}

// ParseDuration parses an iCalendar DURATION such as P1W, P1DT2H or -PT15M.
// A day counts as exactly 24 hours.
func ParseDuration(raw string) (time.Duration, error) {
	s := raw
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" || s[0] != 'P' {
		return 0, valueErr(raw, "missing P")
	}
	s = s[1:]
	var total uint64
	count := 0
	inTime := false
	// order tracks the rank of the last unit seen so units must come in
	// sequence.
	order := -1
	weeks := false
	for s != "" {
		if s[0] == 'T' {
			if inTime || s == "T" {
				return 0, valueErr(raw, "bad time part")
			}
			inTime = true
			order = max(order, rankDay)
			s = s[1:]
			continue
		}
		i := 0
		var n uint64
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if n > (math.MaxInt64-9)/10 {
				return 0, valueErr(raw, "number too large")
			}
			n = n*10 + uint64(s[i]-'0')
			i++
		}
		if i == 0 || i == len(s) {
			return 0, valueErr(raw, "bad component")
		}
		u := s[i]
		unit, rank := durationUnit(u)
		if rank <= order {
			return 0, valueErr(raw, "bad unit")
		}
		if inTime != (rank > rankDay) {
			return 0, valueErr(raw, "unit in the wrong part")
		}
		if u == 'W' {
			weeks = true
		}
		if n > uint64(math.MaxInt64/unit) {
			return 0, valueErr(raw, "duration overflows")
		}
		add := n * uint64(unit)
		if total > uint64(math.MaxInt64)-add {
			return 0, valueErr(raw, "duration overflows")
		}
		total += add
		order = rank
		count++
		s = s[i+1:]
	}
	if count == 0 || (weeks && count > 1) {
		return 0, valueErr(raw, "bad components")
	}
	d := time.Duration(total)
	if neg {
		d = -d
	}
	return d, nil
}

// rankDay is the rank of the D unit; units after it belong to the time part.
const rankDay = 1

// durationUnit returns the length of a DURATION unit and its rank, the
// position it must keep in the value. An unknown unit has rank -1.
func durationUnit(u byte) (time.Duration, int) {
	switch u {
	case 'W':
		return 7 * 24 * time.Hour, 0
	case 'D':
		return 24 * time.Hour, rankDay
	case 'H':
		return time.Hour, 2
	case 'M':
		return time.Minute, 3
	case 'S':
		return time.Second, 4
	}
	return 0, -1
}
