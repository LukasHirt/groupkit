// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

var syncRef = store.CollectionRef{Owner: "u", Kind: store.KindCalendar, Name: "none"}

func emptyStore(t *testing.T) store.Store {
	t.Helper()
	return memory.New()
}
