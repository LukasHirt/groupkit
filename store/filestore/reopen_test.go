// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// reopen closes s and opens its root again with opts.
func reopen(t *testing.T, s *Store, opts ...Option) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.root, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	return s2
}

func openTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustCreate(t *testing.T, s *Store, ref store.CollectionRef, props store.Props) store.Collection {
	t.Helper()
	c, err := s.CreateCollection(context.Background(), ref, props)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustPut(t *testing.T, s *Store, ref store.CollectionRef, name, data, uid string) store.Object {
	t.Helper()
	o, err := s.PutObject(context.Background(), ref, name, []byte(data), store.PutOptions{UID: uid})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestReopenKeepsEverything(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	cal := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	book := store.CollectionRef{Owner: "alice", Kind: store.KindAddressBook, Name: "work"}
	bobCal := store.CollectionRef{Owner: "bob", Kind: store.KindCalendar, Name: "home"}
	calCreated := mustCreate(t, s, cal, store.Props{DisplayName: "Work", Components: []string{"VEVENT", "VTODO"}})
	bookCreated := mustCreate(t, s, book, store.Props{DisplayName: "Contacts"})
	bobCreated := mustCreate(t, s, bobCal, store.Props{Color: "#ff0000ff"})

	mustPut(t, s, cal, "a.ics", "event a", "uid-a") // 1
	mustPut(t, s, cal, "b.ics", "event b", "uid-b") // 2
	mid, err := s.GetCollection(ctx, cal)           // revision 2
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, cal, "a.ics", "event a, changed", "uid-a")                         // 3
	if err := s.DeleteObject(ctx, cal, "b.ics", store.DeleteOptions{}); err != nil { // 4
		t.Fatal(err)
	}
	mustPut(t, s, cal, "c.ics", "event c", "uid-b") // 5: b's UID is free again
	desc := "Office"
	if _, err := s.UpdateCollection(ctx, cal, store.PropsUpdate{Description: &desc}); err != nil { // 6
		t.Fatal(err)
	}
	mustPut(t, s, book, "x.vcf", "card x", "uid-x")
	mustPut(t, s, bobCal, "e.ics", "bob's event", "uid-a")

	wantCols := map[store.CollectionRef]store.Collection{}
	wantObjs := map[store.CollectionRef][]store.Object{}
	wantChanges := map[store.CollectionRef]store.Changes{}
	for _, ref := range []store.CollectionRef{cal, book, bobCal} {
		c, err := s.GetCollection(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		wantCols[ref] = c
		objs, err := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true})
		if err != nil {
			t.Fatal(err)
		}
		wantObjs[ref] = objs
		ch, err := s.Changes(ctx, ref, 0)
		if err != nil {
			t.Fatal(err)
		}
		wantChanges[ref] = ch
	}
	wantSince, err := s.Changes(ctx, cal, mid.Revision)
	if err != nil {
		t.Fatal(err)
	}

	s = reopen(t, s)

	for owner, kind := range map[string]store.Kind{"alice": store.KindCalendar, "bob": store.KindCalendar} {
		cols, err := s.ListCollections(ctx, owner, kind)
		if err != nil || len(cols) != 1 {
			t.Fatalf("ListCollections(%s) = %+v, %v", owner, cols, err)
		}
	}
	books, err := s.ListCollections(ctx, "alice", store.KindAddressBook)
	if err != nil || len(books) != 1 || books[0].ID != bookCreated.ID {
		t.Fatalf("address books = %+v, %v", books, err)
	}
	for ref, want := range wantCols {
		got, err := s.GetCollection(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != want.ID || got.Revision != want.Revision || !got.Created.Equal(want.Created) ||
			got.Props.DisplayName != want.Props.DisplayName || got.Props.Description != want.Props.Description ||
			got.Props.Color != want.Props.Color || !slices.Equal(got.Props.Components, want.Props.Components) {
			t.Errorf("%v after reopen = %+v, want %+v", ref, got, want)
		}
	}
	if got := wantCols[cal]; got.ID != calCreated.ID || got.Revision != 6 || got.Props.Description != "Office" {
		t.Errorf("calendar before reopen = %+v", got)
	}
	if wantCols[bobCal].ID != bobCreated.ID {
		t.Errorf("bob's calendar ID changed")
	}
	for ref, want := range wantObjs {
		got, err := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true})
		if err != nil {
			t.Fatal(err)
		}
		if !sameObjects(got, want) {
			t.Errorf("%v objects after reopen = %+v, want %+v", ref, got, want)
		}
		ch, err := s.Changes(ctx, ref, 0)
		if err != nil || ch.Revision != wantChanges[ref].Revision || !slices.Equal(ch.Changes, wantChanges[ref].Changes) {
			t.Errorf("%v Changes(0) = %+v, %v; want %+v", ref, ch, err, wantChanges[ref])
		}
	}
	gotSince, err := s.Changes(ctx, cal, mid.Revision)
	if err != nil || gotSince.Revision != wantSince.Revision || !slices.Equal(gotSince.Changes, wantSince.Changes) {
		t.Errorf("Changes(%d) = %+v, %v; want %+v", mid.Revision, gotSince, err, wantSince)
	}
	wantList := []store.Change{{Name: "a.ics"}, {Name: "b.ics", Deleted: true}, {Name: "c.ics"}}
	if !slices.Equal(gotSince.Changes, wantList) {
		t.Errorf("Changes(%d) = %+v, want %+v", mid.Revision, gotSince.Changes, wantList)
	}

	var conflict *store.ConflictError
	_, err = s.PutObject(ctx, cal, "d.ics", []byte("event d"), store.PutOptions{UID: "uid-a"})
	if !errors.As(err, &conflict) || conflict.Name != "a.ics" {
		t.Errorf("PutObject with a taken UID = %v, want a conflict with a.ics", err)
	}
	_, err = s.PutObject(ctx, cal, "d.ics", []byte("event d"), store.PutOptions{UID: "uid-b"})
	if !errors.As(err, &conflict) || conflict.Name != "c.ics" {
		t.Errorf("PutObject with b's old UID = %v, want a conflict with c.ics", err)
	}
	if _, err := s.GetObject(ctx, cal, "b.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleted object: %v, want ErrNotFound", err)
	}
}

func sameObjects(a, b []store.Object) bool {
	return slices.EqualFunc(a, b, func(x, y store.Object) bool {
		return x.Name == y.Name && bytes.Equal(x.Data, y.Data) && x.ETag == y.ETag &&
			x.UID == y.UID && x.ModTime.Equal(y.ModTime)
	})
}

func TestReopenContinuesRevisions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	mustPut(t, s, ref, "a.ics", "a", "")
	mustPut(t, s, ref, "b.ics", "b", "")
	name := "Renamed"
	if _, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, ref, "c.ics", "c", "")
	before, err := s.GetCollection(ctx, ref)
	if err != nil || before.Revision != 4 {
		t.Fatalf("before reopen: %+v, %v", before, err)
	}

	s = reopen(t, s)
	if got, err := s.GetCollection(ctx, ref); err != nil || got.Revision != 4 {
		t.Fatalf("after reopen: %+v, %v; want revision 4", got, err)
	}
	mustPut(t, s, ref, "d.ics", "d", "")
	got, err := s.GetCollection(ctx, ref)
	if err != nil || got.Revision != 5 {
		t.Fatalf("after a write: %+v, %v; want revision 5", got, err)
	}
	ch, err := s.Changes(ctx, ref, 4)
	if err != nil || ch.Revision != 5 || !slices.Equal(ch.Changes, []store.Change{{Name: "d.ics"}}) {
		t.Errorf("Changes(4) = %+v, %v", ch, err)
	}

	s = reopen(t, s)
	data, err := os.ReadFile(filepath.Join(s.collectionDir(ref), "log"))
	if err != nil {
		t.Fatal(err)
	}
	recs, validLen, err := parseLog(data)
	if err != nil || validLen != len(data) || len(recs) != 4 || recs[3].Rev != 5 {
		t.Fatalf("log: %+v, valid %d of %d, %v", recs, validLen, len(data), err)
	}
	if got, err := s.GetCollection(ctx, ref); err != nil || got.Revision != 5 {
		t.Fatalf("after second reopen: %+v, %v; want revision 5", got, err)
	}
	if objs, err := s.ListObjects(ctx, ref, store.ListOptions{}); err != nil || len(objs) != 4 {
		t.Fatalf("objects after second reopen: %+v, %v", objs, err)
	}
}

func TestReopenDeleteStaysDeleted(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	mustPut(t, s, ref, "a.ics", "a", "uid-a")
	mustPut(t, s, ref, "b.ics", "b", "uid-b")
	if err := s.DeleteObject(ctx, ref, "a.ics", store.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}

	s = reopen(t, s)
	if _, err := s.GetObject(ctx, ref, "a.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetObject = %v, want ErrNotFound", err)
	}
	objs, err := s.ListObjects(ctx, ref, store.ListOptions{})
	if err != nil || len(objs) != 1 || objs[0].Name != "b.ics" {
		t.Errorf("ListObjects = %+v, %v", objs, err)
	}
	ch, err := s.Changes(ctx, ref, 2)
	if err != nil || !slices.Equal(ch.Changes, []store.Change{{Name: "a.ics", Deleted: true}}) {
		t.Errorf("Changes(2) = %+v, %v", ch, err)
	}
	// The deleted object's UID is free again.
	mustPut(t, s, ref, "c.ics", "c", "uid-a")
}

func TestLoadIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 3000 objects")
	}
	ctx := context.Background()
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "big"}
	mustCreate(t, s, ref, store.Props{})
	dir := s.collectionDir(ref)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Writing through the store would fsync 6000 times, so the files a store
	// would leave behind are written directly.
	body := strings.Repeat("x", 1000)
	const n = 3000
	var log []byte
	mtime := time.Unix(0, time.Now().UnixNano()).UTC()
	for i := range n {
		name := fmt.Sprintf("event-%04d.ics", i)
		data := []byte(body + name)
		path := filepath.Join(dir, "objects", fileName(name))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		log = append(log, encodeRecord(record{
			Rev: uint64(i + 1), Name: name, ETag: store.ETag(data), UID: "uid-" + name,
			Size: int64(len(data)), MTime: mtime.UnixNano(),
		})...)
	}
	if err := os.WriteFile(filepath.Join(dir, "log"), log, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	s2, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	objs, err := s2.ListObjects(ctx, ref, store.ListOptions{})
	elapsed := time.Since(start)
	if err != nil || len(objs) != n {
		t.Fatalf("ListObjects: %d objects, %v", len(objs), err)
	}
	if elapsed >= time.Second {
		t.Errorf("reopening and listing %d objects took %v", n, elapsed)
	}
	t.Logf("reopened and listed %d objects in %v", n, elapsed)
	if c, err := s2.GetCollection(ctx, ref); err != nil || c.Revision != n {
		t.Errorf("GetCollection = %+v, %v; want revision %d", c, err, n)
	}
}

func TestModTimeWithinTwoSecondsIsAccepted(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	put := mustPut(t, s, ref, "e.ics", "data", "uid-e")
	dir := s.collectionDir(ref)
	path := filepath.Join(dir, "objects", fileName("e.ics"))
	logPath := filepath.Join(dir, "log")
	logBefore, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	shift := func(d time.Duration) {
		t.Helper()
		mt := put.ModTime.Add(d)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	check := func(what string) {
		t.Helper()
		s2, err := Open(s.root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s2.Close() }()
		got, err := s2.GetObject(ctx, ref, "e.ics")
		if err != nil || string(got.Data) != "data" || got.ETag != put.ETag || !got.ModTime.Equal(put.ModTime) {
			t.Fatalf("%s: GetObject = %+v, %v", what, got, err)
		}
		logAfter, err := os.ReadFile(logPath)
		if err != nil || !bytes.Equal(logAfter, logBefore) {
			t.Fatalf("%s: the log changed:\n%s\nwas:\n%s", what, logAfter, logBefore)
		}
	}

	shift(time.Second)
	check("mtime off by 1s")
	shift(-time.Second)
	check("mtime off by -1s")
	shift(10 * time.Second)
	check("mtime off by 10s, same content")

	if err := os.WriteFile(path, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	shift(10 * time.Second)
	s2, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	_, err = s2.GetCollection(ctx, ref)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), `"e.ics"`) {
		t.Fatalf("GetCollection with changed content = %v, want ErrCorrupt naming e.ics", err)
	}
	if _, err := s2.ListObjects(ctx, ref, store.ListOptions{}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ListObjects with changed content = %v, want ErrCorrupt", err)
	}
}

func TestJournalLimitSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t, WithMaxJournal(5))
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	for i := range 12 {
		mustPut(t, s, ref, fmt.Sprintf("e%d.ics", i%4), fmt.Sprint(i), "")
	}
	s = reopen(t, s, WithMaxJournal(5))

	ch, err := s.Changes(ctx, ref, 7)
	if err != nil || ch.Revision != 12 {
		t.Fatalf("Changes(7) = %+v, %v", ch, err)
	}
	want := []store.Change{{Name: "e0.ics"}, {Name: "e1.ics"}, {Name: "e2.ics"}, {Name: "e3.ics"}}
	if !slices.Equal(ch.Changes, want) {
		t.Errorf("Changes(7) = %+v, want %+v", ch.Changes, want)
	}
	if _, err := s.Changes(ctx, ref, 6); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Errorf("Changes(6) = %v, want ErrChangesUnavailable", err)
	}
}

func TestMissingObjectFileRefusesToLoad(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	broken := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "broken"}
	fine := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "fine"}
	mustCreate(t, s, broken, store.Props{})
	mustCreate(t, s, fine, store.Props{})
	mustPut(t, s, broken, "a.ics", "a", "")
	mustPut(t, s, broken, "gone.ics", "gone", "")
	// A later write keeps gone.ics out of the last record, which recovery may
	// roll back.
	mustPut(t, s, broken, "c.ics", "c", "")
	mustPut(t, s, fine, "f.ics", "f", "")
	path := filepath.Join(s.collectionDir(broken), "objects", fileName("gone.ics"))

	s = reopen(t, s)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err := s.GetCollection(ctx, broken)
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), `"gone.ics"`) ||
		!strings.Contains(err.Error(), "broken") {
		t.Fatalf("GetCollection = %v, want ErrCorrupt naming gone.ics and the collection", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Errorf("a corrupt collection reads as not found: %v", err)
	}
	if _, err := s.GetObject(ctx, broken, "a.ics"); !errors.Is(err, ErrCorrupt) {
		t.Errorf("GetObject = %v, want ErrCorrupt", err)
	}
	got, err := s.GetObject(ctx, fine, "f.ics")
	if err != nil || string(got.Data) != "f" {
		t.Errorf("other collection: %+v, %v", got, err)
	}
	mustPut(t, s, fine, "g.ics", "g", "")
}

func TestLoadLeavesUnknownFilesAndClearsTmp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	mustPut(t, s, ref, "a.ics", "a", "")
	dir := s.collectionDir(ref)
	stray := filepath.Join(dir, "objects", fileName("stray.ics"))
	leftover := filepath.Join(dir, "tmp", "w-123")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stray, leftover} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s2, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	objs, err := s2.ListObjects(ctx, ref, store.ListOptions{})
	if err != nil || len(objs) != 1 || objs[0].Name != "a.ics" {
		t.Fatalf("ListObjects = %+v, %v", objs, err)
	}
	if _, err := s2.GetObject(ctx, ref, "stray.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetObject(stray) = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("unknown file in objects/: %v, want it left alone", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("file in tmp/: %v, want it removed", err)
	}
}

func TestLoadTruncatesTornTail(t *testing.T) {
	s := openTestStore(t)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	mustCreate(t, s, ref, store.Props{})
	mustPut(t, s, ref, "a.ics", "a", "")
	logPath := filepath.Join(s.collectionDir(ref), "log")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"r":2,"n":"b.i`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(s.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	mustPut(t, s2, ref, "c.ics", "c", "")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	recs, validLen, err := parseLog(data)
	if err != nil || validLen != len(data) || len(recs) != 2 || recs[1].Name != "c.ics" || recs[1].Rev != 2 {
		t.Fatalf("log after torn tail and a write: %+v, valid %d of %d, %v\n%s", recs, validLen, len(data), err, data)
	}
}
