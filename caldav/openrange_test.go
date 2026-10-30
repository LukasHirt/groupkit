// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Clients such as Thunderbird send time ranges with one side open. Over
// weekly series that begin in different years the answer must still be
// exact.
func TestQueryOpenStartTimeRangesOverRecurringObjects(t *testing.T) {
	f := newFixture(t)
	var all []string
	for i := range 30 {
		name := fmt.Sprintf("s%02d.ics", i)
		all = append(all, name)
		start := fmt.Sprintf("%04d0101T090000Z", 2000+i)
		f.put(t, name, wrapCal(vev("UID:s"+fmt.Sprint(i), "DTSTART:"+start, "DTEND:"+start[:9]+"100000Z", "RRULE:FREQ=WEEKLY")))
	}
	// A series whose start is excluded still answers from its next week.
	f.put(t, "ex.ics", wrapCal(vev("UID:ex", "DTSTART:20000103T090000Z", "DTEND:20000103T100000Z",
		"RRULE:FREQ=WEEKLY;COUNT=2", "EXDATE:20000103T090000Z")))
	// A series that is over before the range.
	f.put(t, "done.ics", wrapCal(vev("UID:done", "DTSTART:19990103T090000Z", "RRULE:FREQ=WEEKLY;COUNT=3")))

	first := func(n int) []string { return slices.Clone(all[:n]) }
	tests := []struct {
		name       string
		start, end string
		want       []string
	}{
		{"open start", "", cdate(2005, 1, 1, 0, 0), append([]string{"done.ics", "ex.ics"}, first(5)...)},
		{"open start, narrow", "", cdate(2001, 6, 1, 0, 0), append([]string{"done.ics", "ex.ics"}, first(2)...)},
		{"start only", cdate(2028, 6, 1, 0, 0), "", all},
		{"1900 to 2100", cdate(1900, 1, 1, 0, 0), cdate(2100, 1, 1, 0, 0), append([]string{"done.ics", "ex.ics"}, all...)},
		{"before everything", "", cdate(1990, 1, 1, 0, 0), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bases(parseReply(t, f.report(calURL, timeQuery(tt.start, tt.end))))
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
		})
	}

	// Sixteen distinct ranges in one filter; all of them must hold.
	var b strings.Builder
	b.WriteString(`<C:filter><C:comp-filter name="VCALENDAR">`)
	for k := range 16 {
		b.WriteString(`<C:comp-filter name="VEVENT"><C:time-range` + trAttrs("", cdate(2010+k, 1, 1, 0, 0)) + `/></C:comp-filter>`)
	}
	b.WriteString(`</C:comp-filter></C:filter>`)
	got := bases(parseReply(t, f.report(calURL, query(b.String()))))
	want := append([]string{"done.ics", "ex.ics"}, all[:10]...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("16 ranges: got %v\nwant %v", got, want)
	}
}
