// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// record is one line of a collection's change log.
type record struct {
	Rev     uint64 `json:"r"`
	Name    string `json:"n"`
	Deleted bool   `json:"d,omitempty"`
	ETag    string `json:"e,omitempty"`
	UID     string `json:"u,omitempty"`
	Size    int64  `json:"s,omitempty"`
	MTime   int64  `json:"m,omitempty"` // Unix nanoseconds
}

// encodeRecord returns the record as one JSON line ending in a newline.
func encodeRecord(r record) []byte {
	b, err := json.Marshal(r)
	if err != nil {
		// A record holds only strings and numbers, which always marshal.
		panic("filestore: encode record: " + err.Error())
	}
	return append(b, '\n')
}

// parseLog reads a change log. A last line that is cut off, or that lacks its
// newline, is ignored: it is a write that was in flight when the process died.
// validLen is the length of the complete lines, where the file should be
// truncated. An unparsable line anywhere else, or a revision that does not
// exceed the one before it, returns an error wrapping ErrCorrupt.
func parseLog(data []byte) (recs []record, validLen int, err error) {
	var prev uint64
	for validLen < len(data) {
		nl := bytes.IndexByte(data[validLen:], '\n')
		if nl < 0 {
			break // torn tail: no newline
		}
		line := data[validLen : validLen+nl]
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			// Even for the last line: a torn write cannot be followed by a newline.
			return nil, 0, fmt.Errorf("%w: log line %d: %v", ErrCorrupt, len(recs)+1, err)
		}
		if r.Rev <= prev {
			return nil, 0, fmt.Errorf("%w: log line %d: revision %d does not exceed %d",
				ErrCorrupt, len(recs)+1, r.Rev, prev)
		}
		prev = r.Rev
		recs = append(recs, r)
		validLen += nl + 1
	}
	return recs, validLen, nil
}
