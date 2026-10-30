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
// (display name "Contacts") and a calendar named "personal" (display name
// "Personal", for events), each only if the owner has none of that kind. It
// does nothing for a kind the owner already has, whatever the collection is
// called, so it is safe to call again and from several goroutines at once. An
// owner who deleted every address book or calendar gets the default back the
// next time the application calls it.
//
// It creates both kinds. A program that disables a protocol calls
// Server.EnsureDefaultCollections instead.
//
// Applications call it when they provision a user, so that clients find a
// collection to save contacts and events in. The server never calls it.
func EnsureDefaultCollections(ctx context.Context, st store.Store, owner string) error {
	return ensureDefaults(ctx, st, owner, true, true)
}

// EnsureDefaultCollections is the package function of the same name for the
// protocols this server has enabled: it skips the address book when
// Config.DisableAddressBooks is set and the calendar when
// Config.DisableCalendars is set. The store is the one in the Config.
func (s *Server) EnsureDefaultCollections(ctx context.Context, owner string) error {
	return ensureDefaults(ctx, s.store, owner, !s.disableBooks, !s.disableCalendars)
}

func ensureDefaults(ctx context.Context, st store.Store, owner string, books, calendars bool) error {
	if st == nil {
		return errors.New("groupkit: store is nil")
	}
	if owner == "" {
		return errors.New("groupkit: owner is empty")
	}
	if books {
		err := ensureCollection(ctx, st, owner, store.KindAddressBook, "contacts", store.Props{DisplayName: "Contacts"})
		if err != nil {
			return err
		}
	}
	if calendars {
		return ensureCollection(ctx, st, owner, store.KindCalendar, "personal",
			store.Props{DisplayName: "Personal", Components: []string{"VEVENT"}})
	}
	return nil
}

// ensureCollection creates the named collection unless owner has one of the
// kind. Losing a race to another caller is not an error.
func ensureCollection(ctx context.Context, st store.Store, owner string, kind store.Kind, name string, props store.Props) error {
	existing, err := st.ListCollections(ctx, owner, kind)
	if err != nil {
		return fmt.Errorf("groupkit: list %ss: %w", kind, err)
	}
	if len(existing) > 0 {
		return nil
	}
	ref := store.CollectionRef{Owner: owner, Kind: kind, Name: name}
	if _, err := st.CreateCollection(ctx, ref, props); err != nil && !errors.Is(err, store.ErrExists) {
		return fmt.Errorf("groupkit: create %s: %w", kind, err)
	}
	return nil
}
