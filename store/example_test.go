// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package store_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

// Create a calendar, store an object and replace it only if nobody else
// changed it in the meantime. The ETag comes from the content, so a stale
// ETag is refused.
func Example() {
	ctx := context.Background()
	st := memory.New()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "personal"}
	if _, err := st.CreateCollection(ctx, ref, store.Props{DisplayName: "Personal"}); err != nil {
		fmt.Println(err)
		return
	}

	first := []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//example//EN\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260102T090000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	obj, err := st.PutObject(ctx, ref, "a.ics", first, store.PutOptions{UID: "a", IfNoneMatch: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(obj.ETag == store.ETag(first))

	second := append([]byte(nil), first...)
	second = append(second[:len(second)-len("END:VCALENDAR\r\n")], []byte("X-NOTE:changed\r\nEND:VCALENDAR\r\n")...)
	_, err = st.PutObject(ctx, ref, "a.ics", second, store.PutOptions{UID: "a", IfMatch: "stale"})
	fmt.Println(errors.Is(err, store.ErrPreconditionFailed))
	_, err = st.PutObject(ctx, ref, "a.ics", second, store.PutOptions{UID: "a", IfMatch: obj.ETag})
	fmt.Println(err)
	// Output:
	// true
	// true
	// <nil>
}

// Names are chosen by clients, so check them before using them anywhere else.
func ExampleValidateName() {
	fmt.Println(store.ValidateName("meeting.ics"))
	fmt.Println(store.ValidateName("../escape") != nil)
	// Output:
	// <nil>
	// true
}
