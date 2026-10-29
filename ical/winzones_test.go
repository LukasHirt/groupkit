// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical

import (
	"testing"
	"time"
)

func TestLookupWindowsZone(t *testing.T) {
	cases := map[string]string{
		"W. Europe Standard Time":      "Europe/Berlin",
		"Eastern Standard Time":        "America/New_York",
		"Pacific Standard Time":        "America/Los_Angeles",
		"Central Europe Standard Time": "Europe/Budapest",
		"Romance Standard Time":        "Europe/Paris",
		"Tokyo Standard Time":          "Asia/Tokyo",
		"UTC":                          "Etc/UTC",
	}
	for win, want := range cases {
		got, ok := lookupWindowsZone(win)
		if !ok || got != want {
			t.Errorf("lookupWindowsZone(%q) = %q, %v; want %q, true", win, got, ok, want)
		}
	}
}

func TestLookupWindowsZoneExact(t *testing.T) {
	for _, name := range []string{"w. europe standard time", "W. Europe Standard Time ", "", "Nowhere"} {
		if got, ok := lookupWindowsZone(name); ok {
			t.Errorf("lookupWindowsZone(%q) = %q, true; want not found", name, got)
		}
	}
}

func TestWindowsZonesSize(t *testing.T) {
	if n := len(windowsZones); n < 100 {
		t.Fatalf("windowsZones has %d entries, want at least 100", n)
	}
}

func TestWindowsZonesLoadable(t *testing.T) {
	if _, err := time.LoadLocation("UTC"); err != nil {
		t.Skip("no time zone database available")
	}
	if _, err := time.LoadLocation("Europe/Berlin"); err != nil {
		t.Skip("no time zone database available")
	}
	for win, iana := range windowsZones {
		if _, err := time.LoadLocation(iana); err != nil {
			t.Errorf("%q maps to %q, which does not load: %v", win, iana, err)
		}
	}
}
