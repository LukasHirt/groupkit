// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseDateTime(t *testing.T) {
	w := func(y int, m time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, m, d, h, mi, s, 0, time.UTC)
	}
	tests := []struct {
		name   string
		raw    string
		params map[string][]string
		want   Value
	}{
		{"date", "20260301", nil, Value{Wall: w(2026, 3, 1, 0, 0, 0), Date: true}},
		{"date by param", "20260301", map[string][]string{"VALUE": {"DATE"}}, Value{Wall: w(2026, 3, 1, 0, 0, 0), Date: true}},
		{"floating", "20260301T101500", nil, Value{Wall: w(2026, 3, 1, 10, 15, 0)}},
		{"utc", "20260301T101500Z", nil, Value{Wall: w(2026, 3, 1, 10, 15, 0), UTC: true}},
		{"tzid", "20260301T101500", map[string][]string{"TZID": {"Europe/Prague", "x"}}, Value{Wall: w(2026, 3, 1, 10, 15, 0), TZID: "Europe/Prague"}},
		{"date-time param", "20260301T101500", map[string][]string{"VALUE": {"DATE-TIME"}}, Value{Wall: w(2026, 3, 1, 10, 15, 0)}},
		{"leap day", "20240229", nil, Value{Wall: w(2024, 2, 29, 0, 0, 0), Date: true}},
		{"year 0000", "00000101", nil, Value{Wall: w(0, 1, 1, 0, 0, 0), Date: true}},
		{"year 9999", "99991231T235959Z", nil, Value{Wall: w(9999, 12, 31, 23, 59, 59), UTC: true}},
		{"tzid on utc ignored", "20260301T101500Z", map[string][]string{"TZID": {"Europe/Prague"}},
			Value{Wall: w(2026, 3, 1, 10, 15, 0), UTC: true}},
		{"tzid on date ignored", "20260301", map[string][]string{"TZID": {"Europe/Prague"}},
			Value{Wall: w(2026, 3, 1, 0, 0, 0), Date: true}},
		{"empty tzid", "20260301T101500", map[string][]string{"TZID": {""}}, Value{Wall: w(2026, 3, 1, 10, 15, 0)}},
		{"first of several values", "20260301", map[string][]string{"VALUE": {"date", "DATE-TIME"}},
			Value{Wall: w(2026, 3, 1, 0, 0, 0), Date: true}},
		{"lookalike value is unknown", "20260301", map[string][]string{"VALUE": {"date-t\u0131me"}},
			Value{Wall: w(2026, 3, 1, 0, 0, 0), Date: true}},
		{"lower-case value", "20260301T101500", map[string][]string{"VALUE": {"date-time"}}, Value{Wall: w(2026, 3, 1, 10, 15, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDateTime(tt.raw, tt.params)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Wall.Equal(tt.want.Wall) || got.Date != tt.want.Date || got.UTC != tt.want.UTC || got.TZID != tt.want.TZID {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseDateTimeInvalid(t *testing.T) {
	for _, raw := range []string{
		"2026-03-01", "20260230", "20260301T250000", "", "20260301T1000",
		"20260301T240000", "20260301T235960", "20260301t101500", "20260301T101500z",
		"20260301T101500ZZ", " 20260301", "２０２６０３０１", "20260301T10150a", "2026030", "+0260301",
	} {
		if _, err := ParseDateTime(raw, nil); !errors.Is(err, ErrValue) {
			t.Errorf("%q: err = %v, want ErrValue", raw, err)
		}
	}
	for _, tt := range []struct {
		name   string
		raw    string
		params map[string][]string
	}{
		{"date with VALUE=DATE-TIME", "20260301", map[string][]string{"VALUE": {"DATE-TIME"}}},
		{"date forced", "20260301T101500", map[string][]string{"VALUE": {"DATE"}}},
		{"first of several values wins", "20260301T101500", map[string][]string{"VALUE": {"DATE", "DATE-TIME"}}},
		{"not a leap year", "21000229", nil},
		{"year 0000 day 0", "00000100", nil},
	} {
		if _, err := ParseDateTime(tt.raw, tt.params); !errors.Is(err, ErrValue) {
			t.Errorf("%s: err = %v, want ErrValue", tt.name, err)
		}
	}
	_, err := ParseDateTime(strings.Repeat("x", 500), nil)
	if err == nil || len(err.Error()) > 120 {
		t.Errorf("error not clipped: %v", err)
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"P1D", 24 * time.Hour},
		{"PT1H30M", 90 * time.Minute},
		{"P1W", 7 * 24 * time.Hour},
		{"-PT15M", -15 * time.Minute},
		{"+PT15M", 15 * time.Minute},
		{"P1DT2H", 26 * time.Hour},
		{"PT0S", 0},
		{"PT45S", 45 * time.Second},
		{"P2DT3H4M5S", 48*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
		{"P106751DT23H", 106751*24*time.Hour + 23*time.Hour},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.raw)
		if err != nil || got != tt.want {
			t.Errorf("%q = %v, %v; want %v", tt.raw, got, err, tt.want)
		}
	}
	for _, raw := range []string{
		"", "P", "PT", "1D", "P1.5D", "PT1H1D", "-", "+", "P1W1D", "P1WT1H", "PT1M1H", "P1DT",
		"pt1h", "P1H", "PT1", "P-1D", "PT1S ", "P99999999999999999999D", "P20000000D", "PT9223372036854775807S",
		"P106751DT24H", // each part fits, the sum does not
	} {
		if _, err := ParseDuration(raw); !errors.Is(err, ErrValue) {
			t.Errorf("%q: err = %v, want ErrValue", raw, err)
		}
	}
}

func FuzzParseDateTime(f *testing.F) {
	for _, s := range []string{"20260301", "20260301T101500", "20260301T101500Z", "20260230", "", "2026-03-01", "20260301T240000"} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, raw string, date bool) {
		var params map[string][]string
		if date {
			params = map[string][]string{"VALUE": {"DATE"}}
		}
		v, err := ParseDateTime(raw, params)
		if err != nil {
			return
		}
		w := v.Wall
		if !w.Equal(time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.UTC)) || w.Location() != time.UTC {
			t.Fatalf("invalid wall %v for %q", w, raw)
		}
	})
}

func FuzzParseDuration(f *testing.F) {
	for _, s := range []string{"P1D", "PT1H30M", "P1W", "-PT15M", "", "P", "P99999999999999999999D"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = ParseDuration(raw)
	})
}
