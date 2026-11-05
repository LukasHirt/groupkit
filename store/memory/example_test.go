// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"fmt"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

// A store for tests. Every change is counted by the collection's revision,
// and Changes reports what happened after a revision.
func ExampleNew() {
	ctx := context.Background()
	st := memory.New(memory.WithMaxJournal(100))
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := st.CreateCollection(ctx, ref, store.Props{}); err != nil {
		fmt.Println(err)
		return
	}
	card := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nUID:ada\r\nFN:Ada\r\nEND:VCARD\r\n")
	if _, err := st.PutObject(ctx, ref, "ada.vcf", card, store.PutOptions{UID: "ada"}); err != nil {
		fmt.Println(err)
		return
	}
	ch, err := st.Changes(ctx, ref, 0)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, c := range ch.Changes {
		fmt.Println(c.Name, c.Deleted)
	}
	// Output:
	// ada.vcf false
}
