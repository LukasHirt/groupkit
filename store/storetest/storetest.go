// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package storetest is a conformance suite for [store.Store] implementations.
//
// A store's own tests call [Run], and [RunJournalLimit] if it can limit its change
// journal:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) store.Store { return mystore.New(t.TempDir()) })
//	}
package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// Factory returns a new, empty store for one test. Cleanup goes through t.Cleanup.
type Factory func(t *testing.T) store.Store

// JournalFactory returns a new, empty store that keeps at least maxJournal object
// changes per collection and may drop older ones.
type JournalFactory func(t *testing.T, maxJournal int) store.Store

var ctx = context.Background()

func calRef(owner, name string) store.CollectionRef {
	return store.CollectionRef{Owner: owner, Kind: store.KindCalendar, Name: name}
}

func bookRef(owner, name string) store.CollectionRef {
	return store.CollectionRef{Owner: owner, Kind: store.KindAddressBook, Name: name}
}

// Run runs the whole suite against stores made by newStore.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	groups := []struct {
		name string
		fn   func(*testing.T, Factory)
	}{
		{"Collections", testCollections},
		{"Objects", testObjects},
		{"Names", testNames},
		{"Preconditions", testPreconditions},
		{"UIDs", testUIDs},
		{"Changes", testChanges},
		{"Isolation", testIsolation},
		{"Validation", testValidation},
		{"Context", testContext},
		{"Concurrency", testConcurrency},
	}
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) { g.fn(t, newStore) })
	}
}

// RunJournalLimit checks that a store with a small journal reports old revisions as
// unavailable and still answers recent ones.
func RunJournalLimit(t *testing.T, newStore JournalFactory) {
	t.Helper()
	const limit = 5
	s := newStore(t, limit)
	ref := calRef("alice", "personal")
	mustCreate(t, s, ref)
	for i := 1; i <= 12; i++ {
		mustPut(t, s, ref, fmt.Sprintf("e%02d.ics", i), "data", store.PutOptions{})
	}
	col := mustGet(t, s, ref)
	if col.Revision != 12 {
		t.Fatalf("revision = %d, want 12", col.Revision)
	}

	if _, err := s.Changes(ctx, ref, 0); err != nil {
		t.Errorf("Changes(0) = %v, want a full listing whatever the journal holds", err)
	}
	if got, err := s.Changes(ctx, ref, 12-limit); err != nil || len(got.Changes) != limit {
		t.Errorf("Changes(%d) = %d changes, %v; want %d and no error", 12-limit, len(got.Changes), err, limit)
	}
	// One revision before the oldest answerable one needs a record that was dropped.
	if _, err := s.Changes(ctx, ref, 12-limit-1); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Errorf("Changes(%d) = %v, want ErrChangesUnavailable", 12-limit-1, err)
	}
	if _, err := s.Changes(ctx, ref, 1); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Errorf("Changes(1) = %v, want ErrChangesUnavailable", err)
	}
}

func mustCreate(t *testing.T, s store.Store, ref store.CollectionRef) store.Collection {
	t.Helper()
	c, err := s.CreateCollection(ctx, ref, store.Props{DisplayName: ref.Name})
	if err != nil {
		t.Fatalf("CreateCollection(%+v): %v", ref, err)
	}
	return c
}

func mustGet(t *testing.T, s store.Store, ref store.CollectionRef) store.Collection {
	t.Helper()
	c, err := s.GetCollection(ctx, ref)
	if err != nil {
		t.Fatalf("GetCollection(%+v): %v", ref, err)
	}
	return c
}

func mustPut(t *testing.T, s store.Store, ref store.CollectionRef, name, data string, opts store.PutOptions) store.Object {
	t.Helper()
	o, err := s.PutObject(ctx, ref, name, []byte(data), opts)
	if err != nil {
		t.Fatalf("PutObject(%q): %v", name, err)
	}
	return o
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
}

func testCollections(t *testing.T, newStore Factory) {
	t.Run("create and get", func(t *testing.T) {
		s := newStore(t)
		ref := calRef("alice", "personal")
		props := store.Props{
			DisplayName: "Personal", Description: "My events", Color: "#ff0000ff",
			Order: 3, Timezone: "BEGIN:VTIMEZONE\r\nEND:VTIMEZONE\r\n", Components: []string{"VEVENT", "VTODO"},
		}
		created, err := s.CreateCollection(ctx, ref, props)
		if err != nil {
			t.Fatal(err)
		}
		got := mustGet(t, s, ref)
		if got.ID == "" || got.ID != created.ID {
			t.Errorf("ID = %q (created %q), want a non-empty stable ID", got.ID, created.ID)
		}
		if got.Ref != ref {
			t.Errorf("Ref = %+v, want %+v", got.Ref, ref)
		}
		if fmt.Sprint(got.Props) != fmt.Sprint(props) {
			t.Errorf("Props = %+v, want %+v", got.Props, props)
		}
		if got.Revision != 0 {
			t.Errorf("Revision = %d, want 0 for a new collection", got.Revision)
		}
		if got.Created.IsZero() {
			t.Error("Created is zero")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		s := newStore(t)
		ref := calRef("alice", "personal")
		mustCreate(t, s, ref)
		_, err := s.CreateCollection(ctx, ref, store.Props{})
		wantErr(t, err, store.ErrExists)
	})

	t.Run("same name in different kinds and owners", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, calRef("alice", "main"))
		mustCreate(t, s, bookRef("alice", "main"))
		mustCreate(t, s, calRef("bob", "main"))
	})

	t.Run("get missing", func(t *testing.T) {
		s := newStore(t)
		_, err := s.GetCollection(ctx, calRef("alice", "nope"))
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("list", func(t *testing.T) {
		s := newStore(t)
		mustCreate(t, s, calRef("alice", "work"))
		mustCreate(t, s, calRef("alice", "home"))
		mustCreate(t, s, bookRef("alice", "contacts"))
		mustCreate(t, s, calRef("bob", "bobs"))

		cals, err := s.ListCollections(ctx, "alice", store.KindCalendar)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range cals {
			names = append(names, c.Ref.Name)
		}
		if strings.Join(names, ",") != "home,work" {
			t.Errorf("calendars = %v, want [home work] sorted, without other kinds or owners", names)
		}

		none, err := s.ListCollections(ctx, "carol", store.KindCalendar)
		if err != nil || len(none) != 0 {
			t.Errorf("unknown owner: %v, %v; want an empty list and no error", none, err)
		}
	})

	t.Run("update changes only the fields that are set", func(t *testing.T) {
		s := newStore(t)
		ref := calRef("alice", "personal")
		before, _ := s.CreateCollection(ctx, ref, store.Props{DisplayName: "Old", Description: "keep", Order: 1})
		name, order := "New", 7
		got, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &name, Order: &order})
		if err != nil {
			t.Fatal(err)
		}
		if got.Props.DisplayName != "New" || got.Props.Order != 7 || got.Props.Description != "keep" {
			t.Errorf("Props = %+v", got.Props)
		}
		if got.Revision != before.Revision+1 {
			t.Errorf("Revision = %d, want %d", got.Revision, before.Revision+1)
		}
		if again := mustGet(t, s, ref); fmt.Sprint(again) != fmt.Sprint(got) {
			t.Errorf("stored collection %+v differs from the returned one %+v", again, got)
		}
	})

	t.Run("update that changes nothing keeps the revision", func(t *testing.T) {
		s := newStore(t)
		ref := calRef("alice", "personal")
		mustCreate(t, s, ref)
		same := "personal"
		got, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &same})
		if err != nil {
			t.Fatal(err)
		}
		if got.Revision != 0 {
			t.Errorf("Revision = %d, want 0", got.Revision)
		}
	})

	t.Run("update missing", func(t *testing.T) {
		s := newStore(t)
		x := "x"
		_, err := s.UpdateCollection(ctx, calRef("alice", "nope"), store.PropsUpdate{DisplayName: &x})
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("delete removes objects and a recreated collection is new", func(t *testing.T) {
		s := newStore(t)
		ref := calRef("alice", "personal")
		first := mustCreate(t, s, ref)
		mustPut(t, s, ref, "a.ics", "one", store.PutOptions{})
		if err := s.DeleteCollection(ctx, ref); err != nil {
			t.Fatal(err)
		}
		_, err := s.GetCollection(ctx, ref)
		wantErr(t, err, store.ErrNotFound)
		_, err = s.GetObject(ctx, ref, "a.ics")
		wantErr(t, err, store.ErrNotFound)

		second := mustCreate(t, s, ref)
		if second.ID == first.ID {
			t.Error("a recreated collection reused the ID of the deleted one")
		}
		if second.Revision != 0 {
			t.Errorf("Revision = %d, want 0", second.Revision)
		}
		objs, err := s.ListObjects(ctx, ref, store.ListOptions{})
		if err != nil || len(objs) != 0 {
			t.Errorf("recreated collection has %d objects, %v; want none", len(objs), err)
		}
	})

	t.Run("delete missing", func(t *testing.T) {
		s := newStore(t)
		wantErr(t, s.DeleteCollection(ctx, calRef("alice", "nope")), store.ErrNotFound)
	})
}

func testObjects(t *testing.T, newStore Factory) {
	ref := calRef("alice", "personal")
	setup := func(t *testing.T) store.Store {
		s := newStore(t)
		mustCreate(t, s, ref)
		return s
	}

	t.Run("put and get return the bytes unchanged", func(t *testing.T) {
		s := setup(t)
		data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nX-ODD;P=1:  spaces \r\n and a fold\r\nEND:VCALENDAR\r\n"
		put := mustPut(t, s, ref, "e.ics", data, store.PutOptions{UID: "u1"})
		got, err := s.GetObject(ctx, ref, "e.ics")
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Data) != data {
			t.Errorf("data = %q, want %q", got.Data, data)
		}
		if got.ETag != store.ETag([]byte(data)) || put.ETag != got.ETag {
			t.Errorf("ETag = %q (put returned %q), want %q", got.ETag, put.ETag, store.ETag([]byte(data)))
		}
		if got.Name != "e.ics" || got.UID != "u1" || got.ModTime.IsZero() {
			t.Errorf("object = %+v", got)
		}
	})

	t.Run("binary, empty and large data", func(t *testing.T) {
		s := setup(t)
		for name, data := range map[string][]byte{
			"empty.ics":  {},
			"binary.ics": {0, 1, 2, 0xff, 0xfe, '\r', '\n', 0},
			"large.ics":  bytes.Repeat([]byte("0123456789abcdef"), 64*1024),
		} {
			if _, err := s.PutObject(ctx, ref, name, data, store.PutOptions{}); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got, err := s.GetObject(ctx, ref, name)
			if err != nil || !bytes.Equal(got.Data, data) {
				t.Errorf("%s: round trip failed (%d bytes back, %v)", name, len(got.Data), err)
			}
		}
	})

	t.Run("replace changes the ETag and increases the revision", func(t *testing.T) {
		s := setup(t)
		a := mustPut(t, s, ref, "e.ics", "one", store.PutOptions{})
		rev1 := mustGet(t, s, ref).Revision
		b := mustPut(t, s, ref, "e.ics", "two", store.PutOptions{})
		if a.ETag == b.ETag {
			t.Error("ETag did not change")
		}
		if rev2 := mustGet(t, s, ref).Revision; rev2 != rev1+1 {
			t.Errorf("revision = %d, want %d", rev2, rev1+1)
		}
		if b.ModTime.Before(a.ModTime) {
			t.Error("ModTime went backwards")
		}
	})

	t.Run("writing identical content changes nothing", func(t *testing.T) {
		s := setup(t)
		a := mustPut(t, s, ref, "e.ics", "same", store.PutOptions{UID: "u"})
		before := mustGet(t, s, ref).Revision
		time.Sleep(10 * time.Millisecond)
		b := mustPut(t, s, ref, "e.ics", "same", store.PutOptions{UID: "u"})
		if after := mustGet(t, s, ref).Revision; after != before {
			t.Errorf("revision = %d, want %d", after, before)
		}
		if !b.ModTime.Equal(a.ModTime) || b.ETag != a.ETag {
			t.Errorf("object changed: %+v then %+v", a, b)
		}
	})

	t.Run("missing", func(t *testing.T) {
		s := setup(t)
		_, err := s.GetObject(ctx, ref, "nope.ics")
		wantErr(t, err, store.ErrNotFound)
		wantErr(t, s.DeleteObject(ctx, ref, "nope.ics", store.DeleteOptions{}), store.ErrNotFound)

		other := calRef("alice", "other")
		_, err = s.GetObject(ctx, other, "x.ics")
		wantErr(t, err, store.ErrNotFound)
		_, err = s.ListObjects(ctx, other, store.ListOptions{})
		wantErr(t, err, store.ErrNotFound)
		_, err = s.PutObject(ctx, other, "x.ics", []byte("x"), store.PutOptions{})
		wantErr(t, err, store.ErrNotFound)
		wantErr(t, s.DeleteObject(ctx, other, "x.ics", store.DeleteOptions{}), store.ErrNotFound)
	})

	t.Run("list is sorted and can omit data", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "b.ics", "bb", store.PutOptions{})
		mustPut(t, s, ref, "a.ics", "aa", store.PutOptions{})
		mustPut(t, s, ref, "c.ics", "cc", store.PutOptions{})

		light, err := s.ListObjects(ctx, ref, store.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		full, err := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(light) != 3 || len(full) != 3 {
			t.Fatalf("got %d and %d objects, want 3", len(light), len(full))
		}
		for i, want := range []string{"a.ics", "b.ics", "c.ics"} {
			if light[i].Name != want || full[i].Name != want {
				t.Errorf("position %d: %q and %q, want %q", i, light[i].Name, full[i].Name, want)
			}
			if len(light[i].Data) != 0 || light[i].ETag == "" {
				t.Errorf("listing without data returned %+v", light[i])
			}
			if string(full[i].Data) != strings.Repeat(string(want[0]), 2) {
				t.Errorf("listing with data returned %q for %s", full[i].Data, want)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "e.ics", "one", store.PutOptions{})
		rev := mustGet(t, s, ref).Revision
		if err := s.DeleteObject(ctx, ref, "e.ics", store.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		_, err := s.GetObject(ctx, ref, "e.ics")
		wantErr(t, err, store.ErrNotFound)
		if got := mustGet(t, s, ref).Revision; got != rev+1 {
			t.Errorf("revision = %d, want %d", got, rev+1)
		}
	})
}

func testNames(t *testing.T, newStore Factory) {
	s := newStore(t)
	ref := calRef("alice", "personal")
	mustCreate(t, s, ref)

	// Stores that keep objects as files must round-trip these exactly, including on
	// case-insensitive file systems, and must not confuse one with another.
	names := []string{
		"A.ics", "a.ics", "ä.ics", "ä.ics", "Příklad čeština.vcf", "emoji-😀.ics",
		".hidden", "..x", "a:b", `a\b`, "a%2Fb", "a b", " leading", "trailing ", "trailing.",
		"CON", "con.ics", "aux", "nul.txt", "COM1", "LPT1.ics", "12345@example.com.ics",
		"collection.json", "journal", "~tmp", "a*b?c", "a|b<c>d", `quote".ics`, strings.Repeat("x", 255),
	}
	for i, name := range names {
		mustPut(t, s, ref, name, fmt.Sprintf("content %d", i), store.PutOptions{})
	}
	for i, name := range names {
		got, err := s.GetObject(ctx, ref, name)
		if err != nil {
			t.Errorf("GetObject(%q): %v", name, err)
			continue
		}
		if string(got.Data) != fmt.Sprintf("content %d", i) {
			t.Errorf("GetObject(%q) = %q, want content %d (names were confused)", name, got.Data, i)
		}
	}
	listed, err := s.ListObjects(ctx, ref, store.ListOptions{})
	if err != nil || len(listed) != len(names) {
		t.Fatalf("listed %d objects, %v; want %d", len(listed), err, len(names))
	}
	seen := map[string]bool{}
	for _, o := range listed {
		seen[o.Name] = true
	}
	for _, name := range names {
		if !seen[name] {
			t.Errorf("listing lacks %q", name)
		}
	}

	// Owners and collection names get the same treatment.
	odd := calRef("Vs9saZ-k3RAkNbpg@H8Cwkl/..%2f", "Ünï.cöde")
	mustCreate(t, s, odd)
	mustPut(t, s, odd, "x.ics", "x", store.PutOptions{})
	if _, err := s.GetObject(ctx, odd, "x.ics"); err != nil {
		t.Errorf("object in an oddly named collection: %v", err)
	}
	if _, err := s.GetObject(ctx, ref, "x.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the odd owner's object leaked into another collection: %v", err)
	}
}

func testPreconditions(t *testing.T, newStore Factory) {
	ref := calRef("alice", "personal")
	setup := func(t *testing.T) (store.Store, store.Object) {
		s := newStore(t)
		mustCreate(t, s, ref)
		return s, mustPut(t, s, ref, "e.ics", "v1", store.PutOptions{})
	}
	unchanged := func(t *testing.T, s store.Store, rev uint64, want string) {
		t.Helper()
		got, err := s.GetObject(ctx, ref, "e.ics")
		if err != nil || string(got.Data) != want {
			t.Errorf("object = %q, %v; want %q untouched", got.Data, err, want)
		}
		if r := mustGet(t, s, ref).Revision; r != rev {
			t.Errorf("revision = %d, want %d: a failed condition must not change anything", r, rev)
		}
	}

	t.Run("if-none-match", func(t *testing.T) {
		s, _ := setup(t)
		rev := mustGet(t, s, ref).Revision
		_, err := s.PutObject(ctx, ref, "e.ics", []byte("v2"), store.PutOptions{IfNoneMatch: true})
		wantErr(t, err, store.ErrPreconditionFailed)
		unchanged(t, s, rev, "v1")
		if _, err := s.PutObject(ctx, ref, "new.ics", []byte("n"), store.PutOptions{IfNoneMatch: true}); err != nil {
			t.Errorf("create with if-none-match on a free name: %v", err)
		}
	})

	t.Run("if-match", func(t *testing.T) {
		s, v1 := setup(t)
		rev := mustGet(t, s, ref).Revision
		_, err := s.PutObject(ctx, ref, "e.ics", []byte("v2"), store.PutOptions{IfMatch: "stale"})
		wantErr(t, err, store.ErrPreconditionFailed)
		unchanged(t, s, rev, "v1")

		v2, err := s.PutObject(ctx, ref, "e.ics", []byte("v2"), store.PutOptions{IfMatch: v1.ETag})
		if err != nil || string(v2.Data) != "v2" {
			t.Fatalf("matching if-match: %v", err)
		}
		_, err = s.PutObject(ctx, ref, "e.ics", []byte("v3"), store.PutOptions{IfMatch: v1.ETag})
		wantErr(t, err, store.ErrPreconditionFailed)
	})

	t.Run("if-match on a missing object", func(t *testing.T) {
		s, _ := setup(t)
		_, err := s.PutObject(ctx, ref, "missing.ics", []byte("x"), store.PutOptions{IfMatch: "any"})
		wantErr(t, err, store.ErrPreconditionFailed)
		_, err = s.GetObject(ctx, ref, "missing.ics")
		wantErr(t, err, store.ErrNotFound)
	})

	t.Run("delete with if-match", func(t *testing.T) {
		s, v1 := setup(t)
		rev := mustGet(t, s, ref).Revision
		wantErr(t, s.DeleteObject(ctx, ref, "e.ics", store.DeleteOptions{IfMatch: "stale"}), store.ErrPreconditionFailed)
		unchanged(t, s, rev, "v1")
		if err := s.DeleteObject(ctx, ref, "e.ics", store.DeleteOptions{IfMatch: v1.ETag}); err != nil {
			t.Errorf("delete with matching if-match: %v", err)
		}
	})
}

func testUIDs(t *testing.T, newStore Factory) {
	ref := calRef("alice", "personal")
	setup := func(t *testing.T) store.Store {
		s := newStore(t)
		mustCreate(t, s, ref)
		return s
	}

	t.Run("a second object with the same UID is refused", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{UID: "u1"})
		rev := mustGet(t, s, ref).Revision
		_, err := s.PutObject(ctx, ref, "b.ics", []byte("b"), store.PutOptions{UID: "u1"})
		wantErr(t, err, store.ErrUIDConflict)
		var ce *store.ConflictError
		if !errors.As(err, &ce) || ce.Name != "a.ics" || ce.UID != "u1" {
			t.Errorf("conflict = %+v, want it to name a.ics", ce)
		}
		if _, err := s.GetObject(ctx, ref, "b.ics"); !errors.Is(err, store.ErrNotFound) {
			t.Error("the refused object was stored")
		}
		if got := mustGet(t, s, ref).Revision; got != rev {
			t.Errorf("revision = %d, want %d", got, rev)
		}
	})

	t.Run("same UID in another collection or owner", func(t *testing.T) {
		s := setup(t)
		mustCreate(t, s, calRef("alice", "work"))
		mustCreate(t, s, calRef("bob", "personal"))
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{UID: "u1"})
		mustPut(t, s, calRef("alice", "work"), "a.ics", "a", store.PutOptions{UID: "u1"})
		mustPut(t, s, calRef("bob", "personal"), "a.ics", "a", store.PutOptions{UID: "u1"})
	})

	t.Run("an object may be rewritten and may change its UID", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "1", store.PutOptions{UID: "u1"})
		mustPut(t, s, ref, "a.ics", "2", store.PutOptions{UID: "u1"})
		mustPut(t, s, ref, "a.ics", "3", store.PutOptions{UID: "u2"})
		// u1 is free again.
		mustPut(t, s, ref, "b.ics", "b", store.PutOptions{UID: "u1"})
		_, err := s.PutObject(ctx, ref, "c.ics", []byte("c"), store.PutOptions{UID: "u2"})
		wantErr(t, err, store.ErrUIDConflict)
	})

	t.Run("deleting frees the UID", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{UID: "u1"})
		if err := s.DeleteObject(ctx, ref, "a.ics", store.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		mustPut(t, s, ref, "b.ics", "b", store.PutOptions{UID: "u1"})
	})

	t.Run("an empty UID is not unique", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{})
		mustPut(t, s, ref, "b.ics", "b", store.PutOptions{})
	})
}

func testChanges(t *testing.T, newStore Factory) {
	ref := calRef("alice", "personal")
	setup := func(t *testing.T) store.Store {
		s := newStore(t)
		mustCreate(t, s, ref)
		return s
	}
	describe := func(c store.Changes) string {
		var parts []string
		for _, ch := range c.Changes {
			if ch.Deleted {
				parts = append(parts, "-"+ch.Name)
			} else {
				parts = append(parts, "+"+ch.Name)
			}
		}
		return fmt.Sprintf("@%d %s", c.Revision, strings.Join(parts, " "))
	}
	changes := func(t *testing.T, s store.Store, since uint64) string {
		t.Helper()
		c, err := s.Changes(ctx, ref, since)
		if err != nil {
			t.Fatalf("Changes(%d): %v", since, err)
		}
		return describe(c)
	}
	del := func(t *testing.T, s store.Store, name string) {
		t.Helper()
		if err := s.DeleteObject(ctx, ref, name, store.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("an empty collection", func(t *testing.T) {
		s := setup(t)
		if got := changes(t, s, 0); got != "@0 " {
			t.Errorf("Changes(0) = %q", got)
		}
	})

	t.Run("revision zero lists every current object", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "b.ics", "b", store.PutOptions{})
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{})
		mustPut(t, s, ref, "gone.ics", "g", store.PutOptions{})
		del(t, s, "gone.ics")
		if got := changes(t, s, 0); got != "@4 +a.ics +b.ics" {
			t.Errorf("Changes(0) = %q, want the two live objects and no deletions", got)
		}
	})

	t.Run("nothing changed", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{})
		if got := changes(t, s, 1); got != "@1 " {
			t.Errorf("Changes(current) = %q", got)
		}
	})

	t.Run("changes are collapsed per object", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "kept.ics", "1", store.PutOptions{})   // revision 1
		mustPut(t, s, ref, "edited.ics", "1", store.PutOptions{}) // 2
		mustPut(t, s, ref, "doomed.ics", "1", store.PutOptions{}) // 3
		since := mustGet(t, s, ref).Revision                      // 3

		mustPut(t, s, ref, "edited.ics", "2", store.PutOptions{}) // 4
		mustPut(t, s, ref, "edited.ics", "3", store.PutOptions{}) // 5
		mustPut(t, s, ref, "new.ics", "1", store.PutOptions{})    // 6
		del(t, s, "doomed.ics")                                   // 7
		mustPut(t, s, ref, "flash.ics", "1", store.PutOptions{})  // 8
		del(t, s, "flash.ics")                                    // 9

		want := "@9 -doomed.ics +edited.ics -flash.ics +new.ics"
		if got := changes(t, s, since); got != want {
			t.Errorf("Changes(%d) = %q, want %q", since, got, want)
		}
	})

	t.Run("recreating a deleted object reports it as present", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "1", store.PutOptions{}) // 1
		del(t, s, "a.ics")                                   // 2
		mustPut(t, s, ref, "a.ics", "2", store.PutOptions{}) // 3
		if got := changes(t, s, 1); got != "@3 +a.ics" {
			t.Errorf("Changes(1) = %q", got)
		}
	})

	t.Run("each object write is one revision", func(t *testing.T) {
		s := setup(t)
		for i := 1; i <= 5; i++ {
			mustPut(t, s, ref, fmt.Sprintf("e%d.ics", i), "x", store.PutOptions{})
			if got := mustGet(t, s, ref).Revision; got != uint64(i) {
				t.Fatalf("after write %d the revision is %d", i, got)
			}
		}
	})

	t.Run("property changes advance the revision without listing objects", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{}) // 1
		name := "Renamed"
		if _, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &name}); err != nil { // 2
			t.Fatal(err)
		}
		if got := changes(t, s, 1); got != "@2 " {
			t.Errorf("Changes(1) = %q, want the new revision and no objects", got)
		}
	})

	t.Run("a revision from the future is unavailable", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "a.ics", "a", store.PutOptions{})
		_, err := s.Changes(ctx, ref, 99)
		wantErr(t, err, store.ErrChangesUnavailable)
	})

	t.Run("missing collection", func(t *testing.T) {
		s := setup(t)
		_, err := s.Changes(ctx, calRef("alice", "nope"), 0)
		wantErr(t, err, store.ErrNotFound)
	})
}

func testIsolation(t *testing.T, newStore Factory) {
	s := newStore(t)
	ref := calRef("alice", "personal")
	mustCreate(t, s, ref)

	input := []byte("original")
	if _, err := s.PutObject(ctx, ref, "e.ics", input, store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	copy(input, "XXXXXXXX")
	got, _ := s.GetObject(ctx, ref, "e.ics")
	if string(got.Data) != "original" {
		t.Errorf("changing the slice passed to PutObject changed the stored data: %q", got.Data)
	}

	copy(got.Data, "YYYYYYYY")
	again, _ := s.GetObject(ctx, ref, "e.ics")
	if string(again.Data) != "original" {
		t.Errorf("changing a returned slice changed the stored data: %q", again.Data)
	}

	listed, _ := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true})
	copy(listed[0].Data, "ZZZZZZZZ")
	again, _ = s.GetObject(ctx, ref, "e.ics")
	if string(again.Data) != "original" {
		t.Errorf("changing a listed slice changed the stored data: %q", again.Data)
	}

	comps := []string{"VEVENT"}
	col, _ := s.CreateCollection(ctx, calRef("alice", "other"), store.Props{Components: comps})
	comps[0] = "CHANGED"
	col.Props.Components[0] = "CHANGED TOO"
	if stored := mustGet(t, s, calRef("alice", "other")); stored.Props.Components[0] != "VEVENT" {
		t.Errorf("Components were shared with the caller: %v", stored.Props.Components)
	}
}

func testValidation(t *testing.T, newStore Factory) {
	s := newStore(t)
	good := calRef("alice", "personal")
	mustCreate(t, s, good)

	badRefs := []store.CollectionRef{
		{Owner: "", Kind: store.KindCalendar, Name: "x"},
		{Owner: "alice", Kind: "", Name: "x"},
		{Owner: "alice", Kind: "tasks", Name: "x"},
		{Owner: "alice", Kind: store.KindCalendar, Name: ""},
		{Owner: "alice", Kind: store.KindCalendar, Name: "a/b"},
		{Owner: "alice", Kind: store.KindCalendar, Name: ".."},
		{Owner: "a\x00b", Kind: store.KindCalendar, Name: "x"},
	}
	for _, ref := range badRefs {
		if _, err := s.CreateCollection(ctx, ref, store.Props{}); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("CreateCollection(%+v) = %v, want ErrInvalidName", ref, err)
		}
		if _, err := s.GetCollection(ctx, ref); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("GetCollection(%+v) = %v, want ErrInvalidName", ref, err)
		}
		if _, err := s.PutObject(ctx, ref, "x.ics", []byte("x"), store.PutOptions{}); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("PutObject(%+v) = %v, want ErrInvalidName", ref, err)
		}
	}

	for _, name := range []string{"", ".", "..", "a/b", "a\x00b", "a\nb", "a\xffb", strings.Repeat("x", 256)} {
		if _, err := s.PutObject(ctx, good, name, []byte("x"), store.PutOptions{}); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("PutObject(name %q) = %v, want ErrInvalidName", name, err)
		}
		if _, err := s.GetObject(ctx, good, name); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("GetObject(name %q) = %v, want ErrInvalidName", name, err)
		}
		if err := s.DeleteObject(ctx, good, name, store.DeleteOptions{}); !errors.Is(err, store.ErrInvalidName) {
			t.Errorf("DeleteObject(name %q) = %v, want ErrInvalidName", name, err)
		}
	}
	if _, err := s.ListCollections(ctx, "", store.KindCalendar); !errors.Is(err, store.ErrInvalidName) {
		t.Errorf("ListCollections with an empty owner = %v, want ErrInvalidName", err)
	}
	if _, err := s.ListCollections(ctx, "alice", "tasks"); !errors.Is(err, store.ErrInvalidName) {
		t.Errorf("ListCollections with an unknown kind = %v, want ErrInvalidName", err)
	}
}

func testContext(t *testing.T, newStore Factory) {
	s := newStore(t)
	ref := calRef("alice", "personal")
	mustCreate(t, s, ref)
	mustPut(t, s, ref, "e.ics", "x", store.PutOptions{})

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	calls := map[string]func() error{
		"GetCollection": func() error { _, err := s.GetCollection(cancelled, ref); return err },
		"PutObject": func() error {
			_, err := s.PutObject(cancelled, ref, "n.ics", []byte("n"), store.PutOptions{})
			return err
		},
		"GetObject":   func() error { _, err := s.GetObject(cancelled, ref, "e.ics"); return err },
		"ListObjects": func() error { _, err := s.ListObjects(cancelled, ref, store.ListOptions{}); return err },
		"Changes":     func() error { _, err := s.Changes(cancelled, ref, 0); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a cancelled context = %v, want context.Canceled", name, err)
		}
	}
	if _, err := s.GetObject(ctx, ref, "n.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Error("a write with a cancelled context happened anyway")
	}
}

func testConcurrency(t *testing.T, newStore Factory) {
	const workers = 32
	ref := calRef("alice", "personal")
	setup := func(t *testing.T) store.Store {
		s := newStore(t)
		mustCreate(t, s, ref)
		return s
	}
	run := func(n int, fn func(i int)) {
		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go func() { defer wg.Done(); fn(i) }()
		}
		wg.Wait()
	}

	t.Run("exactly one create wins", func(t *testing.T) {
		s := setup(t)
		var mu sync.Mutex
		wins, losses := 0, 0
		run(workers, func(i int) {
			_, err := s.PutObject(ctx, ref, "e.ics", fmt.Append(nil, i), store.PutOptions{IfNoneMatch: true})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, store.ErrPreconditionFailed):
				losses++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
		if wins != 1 || losses != workers-1 {
			t.Errorf("%d winners and %d losers, want 1 and %d", wins, losses, workers-1)
		}
		if rev := mustGet(t, s, ref).Revision; rev != 1 {
			t.Errorf("revision = %d, want 1", rev)
		}
	})

	t.Run("conditional updates lose nothing", func(t *testing.T) {
		s := setup(t)
		mustPut(t, s, ref, "counter.ics", "0", store.PutOptions{})
		run(workers, func(int) {
			for {
				cur, err := s.GetObject(ctx, ref, "counter.ics")
				if err != nil {
					t.Errorf("get: %v", err)
					return
				}
				n, err := strconv.Atoi(string(cur.Data))
				if err != nil {
					t.Errorf("counter holds %q: %v", cur.Data, err)
					return
				}
				_, err = s.PutObject(ctx, ref, "counter.ics", fmt.Append(nil, n+1), store.PutOptions{IfMatch: cur.ETag})
				if err == nil {
					return
				}
				if !errors.Is(err, store.ErrPreconditionFailed) {
					t.Errorf("put: %v", err)
					return
				}
			}
		})
		got, _ := s.GetObject(ctx, ref, "counter.ics")
		if string(got.Data) != fmt.Sprint(workers) {
			t.Errorf("counter = %q, want %d: updates were lost", got.Data, workers)
		}
	})

	t.Run("distinct objects", func(t *testing.T) {
		s := setup(t)
		run(workers, func(i int) {
			if _, err := s.PutObject(ctx, ref, fmt.Sprintf("e%02d.ics", i), []byte("x"), store.PutOptions{UID: fmt.Sprint("u", i)}); err != nil {
				t.Errorf("put %d: %v", i, err)
			}
		})
		if rev := mustGet(t, s, ref).Revision; rev != workers {
			t.Errorf("revision = %d, want %d", rev, workers)
		}
		c, err := s.Changes(ctx, ref, 0)
		if err != nil || len(c.Changes) != workers {
			t.Errorf("Changes(0) = %d changes, %v; want %d", len(c.Changes), err, workers)
		}
	})

	t.Run("one UID, one winner", func(t *testing.T) {
		s := setup(t)
		var mu sync.Mutex
		wins := 0
		run(workers, func(i int) {
			_, err := s.PutObject(ctx, ref, fmt.Sprintf("e%02d.ics", i), []byte("x"), store.PutOptions{UID: "same"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if !errors.Is(err, store.ErrUIDConflict) {
				t.Errorf("unexpected error: %v", err)
			}
		})
		if wins != 1 {
			t.Errorf("%d objects with the same UID were stored, want 1", wins)
		}
	})
}
