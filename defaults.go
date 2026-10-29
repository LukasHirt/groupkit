// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit

import (
	"context"
	"errors"
	"fmt"

	"github.com/LukasHirt/groupkit/store"
)

// EnsureDefaultCollections gives owner an address book named "contacts"
// (display name "Contacts") if the owner has none. It does nothing when the
// owner already has an address book, whatever its name, so it is safe to call
// again and from several goroutines at once. An owner who deleted every
// address book gets "contacts" back the next time the application calls it.
//
// Applications call it when they provision a user, so that clients find an
// address book to save contacts in. The server never calls it.
func EnsureDefaultCollections(ctx context.Context, st store.Store, owner string) error {
	if st == nil {
		return errors.New("groupkit: store is nil")
	}
	if owner == "" {
		return errors.New("groupkit: owner is empty")
	}
	books, err := st.ListCollections(ctx, owner, store.KindAddressBook)
	if err != nil {
		return fmt.Errorf("groupkit: list address books: %w", err)
	}
	if len(books) > 0 {
		return nil
	}
	ref := store.CollectionRef{Owner: owner, Kind: store.KindAddressBook, Name: "contacts"}
	if _, err := st.CreateCollection(ctx, ref, store.Props{DisplayName: "Contacts"}); err != nil && !errors.Is(err, store.ErrExists) {
		return fmt.Errorf("groupkit: create address book: %w", err)
	}
	return nil
}
