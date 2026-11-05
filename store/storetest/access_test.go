// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package storetest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
	"github.com/LukasHirt/groupkit/store/storetest"
)

var ctx = context.Background()

// expect checks that err is nil when allowed, and ErrPermission otherwise.
func expect(t *testing.T, op string, err error, allowed bool) {
	t.Helper()
	switch {
	case allowed && err != nil:
		t.Errorf("%s: %v, want success", op, err)
	case !allowed && !errors.Is(err, store.ErrPermission):
		t.Errorf("%s: %v, want ErrPermission", op, err)
	}
}

func TestWithAccess(t *testing.T) {
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	other := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "other"}
	for _, level := range []store.Access{store.AccessFull, store.AccessObjects, store.AccessRead} {
		t.Run(level.String(), func(t *testing.T) {
			inner := memory.New()
			if _, err := inner.CreateCollection(ctx, ref, store.Props{DisplayName: "Work"}); err != nil {
				t.Fatal(err)
			}
			if _, err := inner.PutObject(ctx, ref, "a.ics", []byte("a"), store.PutOptions{}); err != nil {
				t.Fatal(err)
			}
			s := storetest.WithAccess(inner, level)

			// Every collection reports the level, however it is reached.
			c, err := s.GetCollection(ctx, ref)
			if err != nil || c.Access != level {
				t.Errorf("GetCollection: Access = %v, %v; want %v", c.Access, err, level)
			}
			list, err := s.ListCollections(ctx, "alice", store.KindCalendar)
			if err != nil || len(list) != 1 || list[0].Access != level {
				t.Errorf("ListCollections: %+v, %v; want one collection at %v", list, err, level)
			}

			// Reads always work.
			if _, err := s.GetObject(ctx, ref, "a.ics"); err != nil {
				t.Errorf("GetObject: %v", err)
			}
			if objs, err := s.ListObjects(ctx, ref, store.ListOptions{}); err != nil || len(objs) != 1 {
				t.Errorf("ListObjects: %d objects, %v", len(objs), err)
			}
			if ch, err := s.Changes(ctx, ref, 0); err != nil || len(ch.Changes) != 1 {
				t.Errorf("Changes: %+v, %v", ch, err)
			}

			objects, props := level.AllowsObjectWrites(), level.AllowsPropertyWrites()
			_, err = s.PutObject(ctx, ref, "b.ics", []byte("b"), store.PutOptions{})
			expect(t, "PutObject", err, objects)
			expect(t, "DeleteObject", s.DeleteObject(ctx, ref, "a.ics", store.DeleteOptions{}), objects)

			name := "Renamed"
			upd, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &name})
			expect(t, "UpdateCollection", err, props)
			if err == nil && upd.Access != level {
				t.Errorf("UpdateCollection: Access = %v, want %v", upd.Access, level)
			}
			created, err := s.CreateCollection(ctx, other, store.Props{})
			expect(t, "CreateCollection", err, props)
			if err == nil && created.Access != level {
				t.Errorf("CreateCollection: Access = %v, want %v", created.Access, level)
			}
			expect(t, "DeleteCollection", s.DeleteCollection(ctx, ref), props)

			// A refusal leaves the inner store as it was.
			if !props {
				got, err := inner.GetCollection(ctx, ref)
				if err != nil || got.Props.DisplayName != "Work" {
					t.Errorf("a refused update or delete changed the collection: %+v, %v", got, err)
				}
				if _, err := inner.GetCollection(ctx, other); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("a refused create made the collection: %v", err)
				}
			}
			if !objects {
				got, err := inner.GetCollection(ctx, ref)
				if err != nil || got.Revision != 1 {
					t.Errorf("a refused object write changed the revision: %d, %v", got.Revision, err)
				}
			}
		})
	}
}

func TestWithAccessRefusesBeforeLookingUp(t *testing.T) {
	// The level is checked first, so a refused write on a missing collection
	// is a permission error, not a not-found one.
	s := storetest.WithAccess(memory.New(), store.AccessRead)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "nope"}
	if _, err := s.PutObject(ctx, ref, "a.ics", []byte("a"), store.PutOptions{}); !errors.Is(err, store.ErrPermission) {
		t.Errorf("PutObject on a missing collection: %v, want ErrPermission", err)
	}
	if _, err := s.GetCollection(ctx, ref); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetCollection on a missing collection: %v, want ErrNotFound", err)
	}
}
