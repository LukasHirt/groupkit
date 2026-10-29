// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return memory.New() })
}

func TestJournalLimit(t *testing.T) {
	storetest.RunJournalLimit(t, func(t *testing.T, maxJournal int) store.Store {
		return memory.New(memory.WithMaxJournal(maxJournal))
	})
}
