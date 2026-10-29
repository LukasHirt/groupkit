// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/storetest"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

var _ store.Store = (*Store)(nil)

func TestConformance(t *testing.T) { storetest.Run(t, newTestStore) }

func TestJournalLimit(t *testing.T) {
	storetest.RunJournalLimit(t, func(t *testing.T, maxJournal int) store.Store {
		s, err := Open(t.TempDir(), WithMaxJournal(maxJournal))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestOpenCreatesLayout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	got, err := os.ReadFile(filepath.Join(root, "format-version"))
	if err != nil || string(got) != "1\n" {
		t.Fatalf("format-version = %q, err = %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "lock")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "owners"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("owners: %v, %v", fi, err)
	}
	if fi, _ := os.Stat(root); fi.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %v", fi.Mode().Perm())
	}
}

func TestOpenSecondStoreIsRefused(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open err = %v, want ErrLocked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(root)
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	_ = s2.Close()
}

func TestOpenRejectsUnknownFormat(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "format-version"), []byte("99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(root)
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("err = %v, want descriptive error mentioning 99", err)
	}
	// The failed Open must not keep the lock.
	if err := os.WriteFile(filepath.Join(root, "format-version"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open after failed Open: %v", err)
	}
	_ = s.Close()
}

func TestOpenRejectsNonDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("Open on a regular file succeeded")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestOwnerDirStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	owners := filepath.Join(root, "owners") + string(filepath.Separator)
	for _, owner := range []string{"..", "../..", "a/../../b", "%2e%2e", strings.Repeat("x", 1024), "."} {
		d := s.OwnerDir(owner)
		if !strings.HasPrefix(d, owners) || len(d) == len(owners) {
			t.Errorf("OwnerDir(%.20q) = %q, outside %q", owner, d, owners)
		}
		if slices.Contains(strings.Split(d, string(filepath.Separator)), "..") {
			t.Errorf("OwnerDir(%.20q) = %q contains ..", owner, d)
		}
		if strings.Contains(strings.TrimPrefix(d, owners), string(filepath.Separator)) {
			t.Errorf("OwnerDir(%.20q) = %q is nested", owner, d)
		}
	}
	if _, err := os.Stat(s.OwnerDir("alice")); !os.IsNotExist(err) {
		t.Fatalf("OwnerDir created something: %v", err)
	}
}

func TestWithMaxJournal(t *testing.T) {
	var o options
	o.maxJournal = 10000
	WithMaxJournal(0)(&o)
	WithMaxJournal(-5)(&o)
	if o.maxJournal != 10000 {
		t.Fatalf("maxJournal = %d", o.maxJournal)
	}
	WithMaxJournal(3)(&o)
	if o.maxJournal != 3 {
		t.Fatalf("maxJournal = %d", o.maxJournal)
	}
}

func TestOwnerDirRejectsInvalidOwners(t *testing.T) {
	s := newTestStore(t).(*Store)
	for _, owner := range []string{"", "a\x00b"} {
		if d := s.OwnerDir(owner); d != "" {
			t.Errorf("OwnerDir(%q) = %q, want an empty string", owner, d)
		}
	}
}

func TestCreateCollectionIsAtomic(t *testing.T) {
	s := newTestStore(t).(*Store)
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(context.Background(), ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	kindDir := filepath.Join(s.OwnerDir("alice"), "calendars")
	leftover := filepath.Join(kindDir, ".staging-x")
	if err := os.MkdirAll(filepath.Join(leftover, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListCollections(context.Background(), "alice", store.KindCalendar)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref.Name != "work" {
		t.Fatalf("ListCollections = %+v, want only work", got)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("leftover staging directory still there: %v", err)
	}
}

func TestCollectionLayoutAndReopen(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice/x", Kind: store.KindAddressBook, Name: "Friends"}
	created, err := s.CreateCollection(ctx, ref, store.Props{DisplayName: "Friends", Components: []string{"VEVENT"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.OwnerDir(ref.Owner), "addressbooks", fileName(ref.Name))
	for _, p := range []string{"collection.json", "log"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", p, fi, err)
		}
	}
	for _, p := range []string{"objects", "tmp", "."} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v", p, fi, err)
		}
	}
	name := "New"
	if _, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	got, err := s2.GetCollection(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Ref != ref || got.Props.DisplayName != "New" || got.Revision != 1 {
		t.Fatalf("after reopen: %+v (created %+v)", got, created)
	}
	if !got.Created.Equal(created.Created) {
		t.Fatalf("Created = %v, want %v", got.Created, created.Created)
	}
}

func TestDeleteCollectionLeavesNothingBehind(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCollection(ctx, ref); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.OwnerDir("alice"), "calendars"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, err = %v; want none", entries, err)
	}
}

func TestUseAfterClose(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	x := "x"
	calls := map[string]func() error{
		"ListCollections":  func() error { _, err := s.ListCollections(ctx, "alice", store.KindCalendar); return err },
		"GetCollection":    func() error { _, err := s.GetCollection(ctx, ref); return err },
		"CreateCollection": func() error { _, err := s.CreateCollection(ctx, ref, store.Props{}); return err },
		"UpdateCollection": func() error {
			_, err := s.UpdateCollection(ctx, ref, store.PropsUpdate{DisplayName: &x})
			return err
		},
		"DeleteCollection": func() error { return s.DeleteCollection(ctx, ref) },
		"GetObject":        func() error { _, err := s.GetObject(ctx, ref, "a.ics"); return err },
		"ListObjects":      func() error { _, err := s.ListObjects(ctx, ref, store.ListOptions{}); return err },
		"PutObject": func() error {
			_, err := s.PutObject(ctx, ref, "a.ics", []byte("x"), store.PutOptions{})
			return err
		},
		"DeleteObject": func() error { return s.DeleteObject(ctx, ref, "a.ics", store.DeleteOptions{}) },
		"Changes":      func() error { _, err := s.Changes(ctx, ref, 0); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrClosed) {
			t.Errorf("%s after Close = %v, want ErrClosed", name, err)
		}
	}
}

func TestListCollectionsKeepsForeignHiddenEntries(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	kindDir := filepath.Join(s.OwnerDir("alice"), "calendars")
	foreign := []string{".DS_Store", ".backup"}
	for _, name := range foreign {
		if err := os.WriteFile(filepath.Join(kindDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ours := []string{".staging-abc", ".trash-def"}
	for _, name := range ours {
		if err := os.Mkdir(filepath.Join(kindDir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListCollections(ctx, "alice", store.KindCalendar)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref.Name != "work" {
		t.Fatalf("ListCollections = %+v, want only work", got)
	}
	for _, name := range foreign {
		if _, err := os.Stat(filepath.Join(kindDir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	for _, name := range ours {
		if _, err := os.Stat(filepath.Join(kindDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
}

func TestObjectFilesOnDisk(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	put, err := s.PutObject(ctx, ref, "Event 1.ics", []byte("data"), store.PutOptions{UID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	dir := s.collectionDir(ref)
	path := filepath.Join(dir, "objects", fileName("Event 1.ics"))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Size() != 4 {
		t.Errorf("object file: mode %v, size %d", fi.Mode().Perm(), fi.Size())
	}
	if d := fi.ModTime().Sub(put.ModTime); d < -2*time.Second || d > 2*time.Second {
		t.Errorf("file mtime %v, object ModTime %v", fi.ModTime(), put.ModTime)
	}
	logData, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	recs, _, err := parseLog(logData)
	if err != nil || len(recs) != 1 {
		t.Fatalf("log: %v records, %v", len(recs), err)
	}
	want := record{Rev: 1, Name: "Event 1.ics", ETag: put.ETag, UID: "u1", Size: 4, MTime: put.ModTime.UnixNano()}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}

	if err := s.DeleteObject(ctx, ref, "Event 1.ics", store.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still there after delete: %v", err)
	}
	logData, _ = os.ReadFile(filepath.Join(dir, "log"))
	recs, _, _ = parseLog(logData)
	if len(recs) != 2 || recs[1].Rev != 2 || !recs[1].Deleted || recs[1].Name != "Event 1.ics" {
		t.Errorf("tombstone = %+v", recs)
	}
}

func TestGetObjectDetectsChangedFile(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ctx, ref, "e.ics", []byte("data"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.collectionDir(ref), "objects", fileName("e.ics"))
	if err := os.WriteFile(path, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetObject(ctx, ref, "e.ics"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("GetObject = %v, want ErrCorrupt", err)
	}
	if _, err := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ListObjects with data = %v, want ErrCorrupt", err)
	}
	if _, err := s.ListObjects(ctx, ref, store.ListOptions{}); err != nil {
		t.Fatalf("ListObjects without data = %v, want no error (it reads no files)", err)
	}
}

func TestPutObjectFailedDataWriteChangesNothing(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ctx, ref, "a.ics", []byte("a"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(s.collectionDir(ref), "tmp")
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ctx, ref, "b.ics", []byte("b"), store.PutOptions{}); err == nil {
		t.Fatal("PutObject without a tmp directory succeeded")
	}
	s.mu.Lock()
	_, cached := s.cols[s.collectionDir(ref)]
	s.mu.Unlock()
	if cached {
		t.Error("the collection stayed cached after a failed data write")
	}
}

func TestJournalTrimRaisesFloorInMemory(t *testing.T) {
	s, err := Open(t.TempDir(), WithMaxJournal(3))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if _, err := s.PutObject(ctx, ref, "e.ics", []byte{byte(i)}, store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := s.lookup(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.recent) != 3 || c.meta.Floor != 2 || c.meta.Revision != 5 || c.logLines != 5 {
		t.Errorf("recent %d, floor %d, revision %d, log lines %d; want 3, 2, 5, 5",
			len(c.recent), c.meta.Floor, c.meta.Revision, c.logLines)
	}
}

func TestDeleteObjectWithUnremovableFileStillDeletes(t *testing.T) {
	s := newTestStore(t).(*Store)
	ctx := context.Background()
	ref := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}
	if _, err := s.CreateCollection(ctx, ref, store.Props{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutObject(ctx, ref, "e.ics", []byte("data"), store.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetCollection(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty directory in place of the file makes the removal fail.
	path := filepath.Join(s.collectionDir(ref), "objects", fileName("e.ics"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteObject(ctx, ref, "e.ics", store.DeleteOptions{}); err != nil {
		t.Fatalf("DeleteObject = %v, want nil", err)
	}
	if _, err := s.GetObject(ctx, ref, "e.ics"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetObject = %v, want ErrNotFound", err)
	}
	list, err := s.ListObjects(ctx, ref, store.ListOptions{})
	if err != nil || len(list) != 0 {
		t.Errorf("ListObjects = %+v, %v; want nothing", list, err)
	}
	after, err := s.GetCollection(ctx, ref)
	if err != nil || after.Revision != before.Revision+1 {
		t.Errorf("revision = %d, %v; want %d", after.Revision, err, before.Revision+1)
	}
	logData, err := os.ReadFile(filepath.Join(s.collectionDir(ref), "log"))
	if err != nil {
		t.Fatal(err)
	}
	recs, _, err := parseLog(logData)
	if err != nil || len(recs) == 0 {
		t.Fatalf("log: %d records, %v", len(recs), err)
	}
	if last := recs[len(recs)-1]; !last.Deleted || last.Name != "e.ics" || last.Rev != after.Revision {
		t.Errorf("last record = %+v, want the tombstone for e.ics at %d", last, after.Revision)
	}
}
