// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard_test

import (
	"fmt"

	"github.com/LukasHirt/groupkit/vcard"
)

// Split cuts an exported file of several cards into single cards. A card that
// does not parse comes back with Err set and the others are still returned.
func ExampleSplit() {
	file := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:a\r\nFN:Ada\r\nEND:VCARD\r\n" +
		"BEGIN:VCARD\r\nVERSION:4.0\r\nUID:b\r\nFN:Bob\r\nEND:VCARD\r\n")
	objs, err := vcard.Split(file)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, o := range objs {
		fmt.Println(o.Card.UID(), o.Err)
	}
	// Output:
	// a <nil>
	// b <nil>
}
