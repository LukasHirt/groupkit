// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"errors"
	"strconv"
	"strings"

	"github.com/LukasHirt/groupkit/store"
)

const (
	syncTokenPrefix = "urn:groupkit:sync:"
	// maxSyncTokenBytes bounds what ParseSyncToken looks at.
	maxSyncTokenBytes = 512
)

var errSyncToken = errors.New("dav: invalid sync token")

// FormatSyncToken returns the sync token of a collection at its current
// revision: "urn:groupkit:sync:{collectionID}:{revision}".
func FormatSyncToken(c store.Collection) string {
	return syncTokenPrefix + c.ID + ":" + strconv.FormatUint(c.Revision, 10)
}

// ParseSyncToken splits a token made by FormatSyncToken. The collection ID
// may contain colons, so the revision is whatever follows the last one. It
// rejects an empty ID, a revision that is not a plain decimal number that
// fits in a uint64, a wrong prefix and a token over 512 bytes.
func ParseSyncToken(tok string) (id string, rev uint64, err error) {
	if len(tok) > maxSyncTokenBytes {
		return "", 0, errSyncToken
	}
	rest, ok := strings.CutPrefix(tok, syncTokenPrefix)
	if !ok {
		return "", 0, errSyncToken
	}
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", 0, errSyncToken
	}
	digits := rest[i+1:]
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", 0, errSyncToken
		}
	}
	rev, err = strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return "", 0, errSyncToken
	}
	return rest[:i], rev, nil
}
