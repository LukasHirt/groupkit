// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package ical_test

import (
	"fmt"

	"github.com/LukasHirt/groupkit/ical"
)

// Split cuts an imported file into the objects a collection stores, one per
// UID.
func ExampleSplit() {
	file := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//example//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:one\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T090000Z\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:two\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260103T090000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	objs, err := ical.Split(file)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, o := range objs {
		fmt.Println(o.UID, len(o.Calendar.Encode()) > 0)
	}
	// Output:
	// one true
	// two true
}
