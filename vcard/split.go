// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package vcard

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/LukasHirt/groupkit/internal/contentline"
)

// Object is one card cut out of a larger file by Split.
type Object struct {
	// Data is the card's text from its BEGIN line to its END line, with
	// CRLF line endings. Folded lines are kept as they were in the file.
	Data []byte
	// Card is the parsed card, or nil when Err is set.
	Card *Card
	// Err says why the card did not parse. The other cards in the file are
	// not affected.
	Err error
}

// Split cuts a file of several vCards into its cards, in file order. A card
// runs from a BEGIN:VCARD line to the next END:VCARD line. A BEGIN:VCARD
// inside a card starts a new card, so a card that lost its END line is
// reported broken on its own and the cards after it are still read. Each
// card is parsed as Parse does, and a card that does not parse is returned
// with its Err set rather than failing the whole file.
//
// Blank lines between cards are skipped. Any other line outside a card is an
// ErrInvalid for the whole file. A file without cards gives no objects and
// no error. Split has no limit on the number of lines; the caller bounds the
// size of the file.
func Split(data []byte) ([]Object, error) {
	data = bytes.TrimPrefix(data, bom)

	var (
		objects []Object
		cur     []byte // the card being collected, nil outside a card
		phys    int    // physical line being read
	)
	flush := func() {
		if cur == nil {
			return
		}
		o := Object{Data: cur}
		o.Card, o.Err = Parse(cur)
		objects = append(objects, o)
		cur = nil
	}
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		phys++

		switch {
		case isMarkerLine(line, "BEGIN"):
			flush()
			cur = []byte{}
		case cur == nil:
			if len(bytes.TrimSpace(line)) > 0 {
				return nil, fmt.Errorf("%w: line %d: text outside a card", ErrInvalid, phys)
			}
			continue
		}
		cur = append(cur, line...)
		cur = append(cur, '\r', '\n')
		if isMarkerLine(line, "END") {
			flush()
		}
	}
	flush()
	return objects, nil
}

var bom = []byte{0xef, 0xbb, 0xbf}

// isMarkerLine reports whether the physical line is "<name>:VCARD" with the
// name and value in any ASCII case and white space around the value ignored,
// as Parse reads a BEGIN or END line. A continuation line never is one, as
// it starts with a space or a tab.
func isMarkerLine(line []byte, name string) bool {
	i := bytes.IndexByte(line, ':')
	if i < 0 || !contentline.EqualFoldASCII(string(line[:i]), name) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(string(line[i+1:])), "VCARD")
}
