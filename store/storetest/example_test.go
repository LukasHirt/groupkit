// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package storetest_test

import (
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

// A store implementation runs the suite from its own tests. Here the
// in-memory store stands in for your own.
func ExampleRun() {
	var t *testing.T // supplied by the test function
	storetest.Run(t, func(t *testing.T) store.Store { return memory.New() })
}

// WithAccess reports one access level for every collection, to test code
// that acts on it.
func ExampleWithAccess() {
	st := storetest.WithAccess(memory.New(), store.AccessRead)
	_ = st
}
