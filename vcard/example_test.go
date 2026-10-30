// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard_test

import (
	"fmt"

	"github.com/LukasHirt/groupkit/vcard"
)

// Select cards whose e-mail address contains a string, as an
// addressbook-query does.
func ExampleFilter_Match() {
	card, err := vcard.Parse([]byte("BEGIN:VCARD\r\n" +
		"VERSION:4.0\r\n" +
		"UID:ada-1\r\n" +
		"FN:Ada Lovelace\r\n" +
		"EMAIL:ada@example.com\r\n" +
		"END:VCARD\r\n"))
	if err != nil {
		fmt.Println(err)
		return
	}

	email := func(needle string) *vcard.Filter {
		return &vcard.Filter{Props: []vcard.PropFilter{{
			Name:        "EMAIL",
			TextMatches: []vcard.TextMatch{{Value: needle}},
		}}}
	}
	for _, needle := range []string{"example.com", "example.org"} {
		f := email(needle)
		if err := f.Validate(); err != nil {
			fmt.Println(err)
			return
		}
		fmt.Printf("%s: %t\n", needle, f.Match(card))
	}
	// Output:
	// example.com: true
	// example.org: false
}
