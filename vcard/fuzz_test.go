// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import "testing"

func FuzzParse(f *testing.F) {
	f.Add([]byte(card30))
	f.Add([]byte(card40))
	f.Add([]byte("BEGIN:VCARD\nVERSION:2.1\nEND:VCARD\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Parse(data)
		if err != nil {
			return
		}
		if c.Version != "3.0" && c.Version != "4.0" {
			t.Fatalf("Version = %q", c.Version)
		}
		for _, p := range c.Props {
			_ = p.Text()
		}
	})
}
