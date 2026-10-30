// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical_test

import (
	"fmt"
	"time"

	"github.com/LukasHirt/groupkit/ical"
)

// Ask whether a weekly event falls inside a time window, as a calendar-query
// does. The event starts on Monday 2 November 2026 and repeats every week.
func ExampleCalendar_Matches() {
	data := []byte("BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//example//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:standup@example.com\r\n" +
		"DTSTAMP:20261001T000000Z\r\n" +
		"DTSTART:20261102T090000Z\r\n" +
		"DTEND:20261102T091500Z\r\n" +
		"RRULE:FREQ=WEEKLY\r\n" +
		"SUMMARY:Standup\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	cal, err := ical.Parse(data)
	if err != nil {
		fmt.Println(err)
		return
	}

	window := func(day int) *ical.CompFilter {
		start := time.Date(2026, time.November, day, 0, 0, 0, 0, time.UTC)
		return &ical.CompFilter{
			Name: "VCALENDAR",
			Comps: []ical.CompFilter{{
				Name:      "VEVENT",
				TimeRange: &ical.TimeRange{Start: start, End: start.AddDate(0, 0, 1)},
			}},
		}
	}

	for _, day := range []int{9, 10} { // a Monday and the Tuesday after it
		ok, err := cal.Matches(window(day), nil)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("November %d: %t\n", day, ok)
	}
	// Output:
	// November 9: true
	// November 10: false
}
