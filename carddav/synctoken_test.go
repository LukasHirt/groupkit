// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/store"
)

func TestSyncTokenRoundTrip(t *testing.T) {
	for _, id := range []string{"abc", "a:b:c", "uuid-1234", "ü ö", ":"} {
		for _, rev := range []uint64{0, 1, 42, math.MaxUint64} {
			tok := syncToken(store.Collection{ID: id, Revision: rev})
			want := "urn:groupkit:sync:" + id + ":" + strconv.FormatUint(rev, 10)
			if tok != want {
				t.Fatalf("syncToken = %q, want %q", tok, want)
			}
			gotID, gotRev, err := parseSyncToken(tok)
			if err != nil || gotID != id || gotRev != rev {
				t.Errorf("parseSyncToken(%q) = %q, %d, %v", tok, gotID, gotRev, err)
			}
		}
	}
}

func TestParseSyncTokenRejects(t *testing.T) {
	tests := map[string]string{
		"empty":          "",
		"garbage":        "hello",
		"wrong prefix":   "urn:other:sync:abc:1",
		"prefix only":    "urn:groupkit:sync:",
		"no revision":    "urn:groupkit:sync:abc",
		"empty id":       "urn:groupkit:sync::5",
		"empty revision": "urn:groupkit:sync:abc:",
		"not decimal":    "urn:groupkit:sync:abc:x1",
		"negative":       "urn:groupkit:sync:abc:-1",
		"plus":           "urn:groupkit:sync:abc:+1",
		"hex":            "urn:groupkit:sync:abc:0x10",
		"overflow":       "urn:groupkit:sync:abc:18446744073709551616",
		"huge":           "urn:groupkit:sync:abc:" + strings.Repeat("9", 400),
		"too long":       "urn:groupkit:sync:" + strings.Repeat("a", 600) + ":1",
	}
	for name, tok := range tests {
		t.Run(name, func(t *testing.T) {
			if id, rev, err := parseSyncToken(tok); err == nil {
				t.Errorf("parseSyncToken(%q) = %q, %d, nil; want an error", tok, id, rev)
			}
		})
	}
}
