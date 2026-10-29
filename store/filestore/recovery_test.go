// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

var workRef = store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "work"}

// closedStore makes a store with the collection workRef, runs fill on it and
// closes it. It returns the root and the collection's directory.
func closedStore(t *testing.T, fill func(s *Store)) (root, dir string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, workRef, store.Props{})
	fill(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return s.root, s.collectionDir(workRef)
}

func openRoot(t *testing.T, root string, opts ...Option) *Store {
	t.Helper()
	s, err := Open(root, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func appendToFile(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// putRecord returns the record a put of data would append at revision rev.
func putRecord(rev uint64, name, data, uid string, mtime time.Time) record {
	return record{
		Rev:   rev,
		Name:  name,
		ETag:  store.ETag([]byte(data)),
		UID:   uid,
		Size:  int64(len(data)),
		MTime: mtime.UnixNano(),
	}
}

func objPath(dir, name string) string { return filepath.Join(dir, "objects", fileName(name)) }

func wantRevision(t *testing.T, s *Store, ref store.CollectionRef, rev uint64) {
	t.Helper()
	c, err := s.GetCollection(context.Background(), ref)
	if err != nil || c.Revision != rev {
		t.Fatalf("GetCollection = %+v, %v; want revision %d", c, err, rev)
	}
}

func wantAbsent(t *testing.T, s *Store, ref store.CollectionRef, name string) {
	t.Helper()
	if _, err := s.GetObject(context.Background(), ref, name); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetObject(%s) = %v, want ErrNotFound", name, err)
	}
}

func wantCorrupt(t *testing.T, err error, name string) {
	t.Helper()
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), `"`+name+`"`) {
		t.Fatalf("error = %v, want ErrCorrupt naming %q", err, name)
	}
}

func TestRecoverTornLastLine(t *testing.T) {
	root, dir := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "a", "")
		mustPut(t, s, workRef, "b.ics", "b", "")
	})
	logPath := filepath.Join(dir, "log")
	clean := readFile(t, logPath)

	t.Run("torn line alone", func(t *testing.T) {
		appendToFile(t, logPath, []byte(`{"r":3,"n":"c.i`))
		s := openRoot(t, root)
		wantRevision(t, s, workRef, 2)
		if got := readFile(t, logPath); !bytes.Equal(got, clean) {
			t.Fatalf("log = %q, want %q", got, clean)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("torn line after a rolled back record", func(t *testing.T) {
		appendToFile(t, logPath, encodeRecord(putRecord(3, "c.ics", "c", "uid-c", time.Now())))
		appendToFile(t, logPath, []byte(`{"r":4,"n":"d.i`))
		s := openRoot(t, root)
		wantRevision(t, s, workRef, 2)
		wantAbsent(t, s, workRef, "c.ics")
		if got := readFile(t, logPath); !bytes.Equal(got, clean) {
			t.Fatalf("log = %q, want %q", got, clean)
		}
		if o := mustPut(t, s, workRef, "e.ics", "e", "uid-c"); o.UID != "uid-c" {
			t.Fatalf("put = %+v", o)
		}
		wantRevision(t, s, workRef, 3)
	})
}

func TestRecoverRollsBackCreate(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) { mustPut(t, s, workRef, "a.ics", "a", "uid-a") })
	logPath := filepath.Join(dir, "log")
	before := readFile(t, logPath)
	appendToFile(t, logPath, encodeRecord(putRecord(2, "new.ics", "new", "uid-n", time.Now())))

	s := openRoot(t, root)
	wantAbsent(t, s, workRef, "new.ics")
	wantRevision(t, s, workRef, 1)
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Fatalf("log = %q, want the record gone: %q", got, before)
	}
	ch, err := s.Changes(ctx, workRef, 1)
	if err != nil || ch.Revision != 1 || len(ch.Changes) != 0 {
		t.Fatalf("Changes(1) = %+v, %v", ch, err)
	}
	// The UID is free and the next write takes the revision back.
	if o := mustPut(t, s, workRef, "other.ics", "other", "uid-n"); o.UID != "uid-n" {
		t.Fatalf("put = %+v", o)
	}
	wantRevision(t, s, workRef, 2)
}

func TestRecoverRollsBackOverwrite(t *testing.T) {
	ctx := context.Background()
	var old store.Object
	root, dir := closedStore(t, func(s *Store) {
		old = mustPut(t, s, workRef, "a.ics", "old", "uid-1")
		mustPut(t, s, workRef, "b.ics", "b", "")
	})
	logPath := filepath.Join(dir, "log")
	before := readFile(t, logPath)
	appendToFile(t, logPath, encodeRecord(putRecord(3, "a.ics", "new content", "uid-2", time.Now())))

	s := openRoot(t, root)
	got, err := s.GetObject(ctx, workRef, "a.ics")
	if err != nil || got.ETag != old.ETag || got.UID != "uid-1" || string(got.Data) != "old" ||
		!got.ModTime.Equal(old.ModTime) {
		t.Fatalf("GetObject = %+v, %v; want %+v", got, err, old)
	}
	wantRevision(t, s, workRef, 2)
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Fatalf("log = %q, want %q", got, before)
	}
	// uid-1 still belongs to a.ics, uid-2 is free.
	var conflict *store.ConflictError
	if _, err := s.PutObject(ctx, workRef, "c.ics", []byte("c"), store.PutOptions{UID: "uid-1"}); !errors.As(err, &conflict) {
		t.Fatalf("put with uid-1 = %v, want a conflict", err)
	}
	mustPut(t, s, workRef, "c.ics", "c", "uid-2")
	wantRevision(t, s, workRef, 3)
}

func TestRecoverFinishesDelete(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "a", "uid-a")
		mustPut(t, s, workRef, "b.ics", "b", "")
	})
	logPath := filepath.Join(dir, "log")
	appendToFile(t, logPath, encodeRecord(record{Rev: 3, Name: "a.ics", Deleted: true, MTime: time.Now().UnixNano()}))
	withTombstone := readFile(t, logPath)

	s := openRoot(t, root)
	wantAbsent(t, s, workRef, "a.ics")
	wantRevision(t, s, workRef, 3)
	if _, err := os.Lstat(objPath(dir, "a.ics")); !os.IsNotExist(err) {
		t.Fatalf("file of the deleted object: %v, want it removed", err)
	}
	if got := readFile(t, logPath); !bytes.Equal(got, withTombstone) {
		t.Fatalf("log = %q, want the tombstone kept: %q", got, withTombstone)
	}
	ch, err := s.Changes(ctx, workRef, 2)
	if err != nil || !slices.Equal(ch.Changes, []store.Change{{Name: "a.ics", Deleted: true}}) {
		t.Fatalf("Changes(2) = %+v, %v", ch, err)
	}
	mustPut(t, s, workRef, "c.ics", "c", "uid-a")
}

func TestRecoverKeepsFinishedWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("overwrite", func(t *testing.T) {
		root, dir := closedStore(t, func(s *Store) {
			mustPut(t, s, workRef, "a.ics", "old", "uid-1")
		})
		logPath := filepath.Join(dir, "log")
		mtime := time.Now().Add(time.Minute)
		if err := os.WriteFile(objPath(dir, "a.ics"), []byte("new data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(objPath(dir, "a.ics"), mtime, mtime); err != nil {
			t.Fatal(err)
		}
		appendToFile(t, logPath, encodeRecord(putRecord(2, "a.ics", "new data", "uid-2", mtime)))
		before := readFile(t, logPath)

		s := openRoot(t, root)
		got, err := s.GetObject(ctx, workRef, "a.ics")
		if err != nil || string(got.Data) != "new data" || got.UID != "uid-2" || !got.ModTime.Equal(mtime.UTC().Round(0)) {
			t.Fatalf("GetObject = %+v, %v", got, err)
		}
		wantRevision(t, s, workRef, 2)
		if got := readFile(t, logPath); !bytes.Equal(got, before) {
			t.Fatalf("log changed: %q, was %q", got, before)
		}
	})

	t.Run("create", func(t *testing.T) {
		root, dir := closedStore(t, func(s *Store) {})
		logPath := filepath.Join(dir, "log")
		mtime := time.Now()
		if err := os.WriteFile(objPath(dir, "a.ics"), []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(objPath(dir, "a.ics"), mtime, mtime); err != nil {
			t.Fatal(err)
		}
		appendToFile(t, logPath, encodeRecord(putRecord(1, "a.ics", "a", "", mtime)))

		s := openRoot(t, root)
		if got, err := s.GetObject(ctx, workRef, "a.ics"); err != nil || string(got.Data) != "a" {
			t.Fatalf("GetObject = %+v, %v", got, err)
		}
		wantRevision(t, s, workRef, 1)
	})

	t.Run("delete with the file gone", func(t *testing.T) {
		root, dir := closedStore(t, func(s *Store) { mustPut(t, s, workRef, "a.ics", "a", "") })
		logPath := filepath.Join(dir, "log")
		if err := os.Remove(objPath(dir, "a.ics")); err != nil {
			t.Fatal(err)
		}
		appendToFile(t, logPath, encodeRecord(record{Rev: 2, Name: "a.ics", Deleted: true, MTime: time.Now().UnixNano()}))
		before := readFile(t, logPath)

		s := openRoot(t, root)
		wantAbsent(t, s, workRef, "a.ics")
		wantRevision(t, s, workRef, 2)
		if got := readFile(t, logPath); !bytes.Equal(got, before) {
			t.Fatalf("log changed: %q, was %q", got, before)
		}
	})
}

func TestRecoverLastRecordThatFitsNothingRefusesToLoad(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, dir string) record{
		"overwrite with the file missing": func(t *testing.T, dir string) record {
			if err := os.Remove(objPath(dir, "a.ics")); err != nil {
				t.Fatal(err)
			}
			return putRecord(2, "a.ics", "new", "", time.Now())
		},
		"overwrite with other content": func(t *testing.T, dir string) record {
			if err := os.WriteFile(objPath(dir, "a.ics"), []byte("something else"), 0o600); err != nil {
				t.Fatal(err)
			}
			return putRecord(2, "a.ics", "new", "", time.Now())
		},
		"create with other content": func(t *testing.T, dir string) record {
			if err := os.WriteFile(objPath(dir, "n.ics"), []byte("something else"), 0o600); err != nil {
				t.Fatal(err)
			}
			return putRecord(2, "n.ics", "new", "", time.Now())
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			root, dir := closedStore(t, func(s *Store) { mustPut(t, s, workRef, "a.ics", "old", "") })
			logPath := filepath.Join(dir, "log")
			r := breakIt(t, dir)
			appendToFile(t, logPath, encodeRecord(r))
			before := readFile(t, logPath)

			s := openRoot(t, root)
			_, err := s.GetCollection(ctx, workRef)
			wantCorrupt(t, err, r.Name)
			if got := readFile(t, logPath); !bytes.Equal(got, before) {
				t.Fatalf("log changed: %q, was %q", got, before)
			}
		})
	}
}

func TestMissingFileInTheMiddleRefusesToLoad(t *testing.T) {
	ctx := context.Background()
	fine := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "fine"}
	root, dir := closedStore(t, func(s *Store) {
		mustCreate(t, s, fine, store.Props{})
		mustPut(t, s, workRef, "a.ics", "a", "")
		mustPut(t, s, workRef, "gone.ics", "gone", "")
		mustPut(t, s, workRef, "c.ics", "c", "")
		mustPut(t, s, fine, "f.ics", "f", "")
	})
	if err := os.Remove(objPath(dir, "gone.ics")); err != nil {
		t.Fatal(err)
	}
	// A last record that would otherwise be rolled back must stay too: nothing
	// is repaired in a damaged collection.
	logPath := filepath.Join(dir, "log")
	appendToFile(t, logPath, encodeRecord(putRecord(4, "d.ics", "d", "", time.Now())))
	before := readFile(t, logPath)

	s := openRoot(t, root)
	name := "x"
	calls := map[string]func() error{
		"GetCollection": func() error { _, err := s.GetCollection(ctx, workRef); return err },
		"UpdateCollection": func() error {
			_, err := s.UpdateCollection(ctx, workRef, store.PropsUpdate{DisplayName: &name})
			return err
		},
		"GetObject":   func() error { _, err := s.GetObject(ctx, workRef, "a.ics"); return err },
		"ListObjects": func() error { _, err := s.ListObjects(ctx, workRef, store.ListOptions{}); return err },
		"PutObject": func() error {
			_, err := s.PutObject(ctx, workRef, "z.ics", []byte("z"), store.PutOptions{})
			return err
		},
		"DeleteObject": func() error { return s.DeleteObject(ctx, workRef, "a.ics", store.DeleteOptions{}) },
		"Changes":      func() error { _, err := s.Changes(ctx, workRef, 0); return err },
	}
	for what, call := range calls {
		err := call()
		if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), `"gone.ics"`) {
			t.Errorf("%s = %v, want ErrCorrupt naming gone.ics", what, err)
		}
	}
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Errorf("log changed: %q, was %q", got, before)
	}
	if _, err := os.Lstat(objPath(dir, "a.ics")); err != nil {
		t.Errorf("a.ics: %v", err)
	}
	if got, err := s.GetObject(ctx, fine, "f.ics"); err != nil || string(got.Data) != "f" {
		t.Errorf("other collection: %+v, %v", got, err)
	}
	mustPut(t, s, fine, "g.ics", "g", "")
}

func TestModifiedFileRefusesToLoad(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(t *testing.T, path string, put store.Object){
		"same size, other content, time off": func(t *testing.T, path string, put store.Object) {
			if err := os.WriteFile(path, []byte("evil"), 0o600); err != nil {
				t.Fatal(err)
			}
			mt := put.ModTime.Add(time.Hour)
			if err := os.Chtimes(path, mt, mt); err != nil {
				t.Fatal(err)
			}
		},
		"other size, same time": func(t *testing.T, path string, put store.Object) {
			if err := os.WriteFile(path, []byte("longer data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, put.ModTime, put.ModTime); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, path string, _ store.Object) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		},
		"symlink to identical content": func(t *testing.T, path string, put store.Object) {
			target := filepath.Join(t.TempDir(), "copy")
			if err := os.WriteFile(target, []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(target, put.ModTime, put.ModTime); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			var put store.Object
			root, dir := closedStore(t, func(s *Store) {
				put = mustPut(t, s, workRef, "e.ics", "data", "")
				mustPut(t, s, workRef, "last.ics", "last", "")
			})
			breakIt(t, objPath(dir, "e.ics"), put)
			s := openRoot(t, root)
			_, err := s.GetCollection(ctx, workRef)
			wantCorrupt(t, err, "e.ics")
		})
	}
}

// The fast path tested here only applies to records before the last one; the
// last record's file is always hashed.
func TestModTimeSlackBoundary(t *testing.T) {
	ctx := context.Background()
	var put store.Object
	root, dir := closedStore(t, func(s *Store) {
		put = mustPut(t, s, workRef, "e.ics", "data", "")
		mustPut(t, s, workRef, "last.ics", "last", "")
	})
	path := objPath(dir, "e.ics")
	set := func(data string, shift time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := put.ModTime.Add(shift)
		if err := os.Chtimes(path, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	load := func() error {
		t.Helper()
		s, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		_, err = s.GetCollection(ctx, workRef)
		return err
	}

	// Exactly two seconds off: accepted without reading the file, so even
	// other bytes of the same size pass the load.
	set("evil", 2*time.Second)
	if err := load(); err != nil {
		t.Fatalf("2s off, not hashed: %v", err)
	}
	set("data", 3*time.Second)
	if err := load(); err != nil {
		t.Fatalf("3s off, same content: %v", err)
	}
	set("evil", 3*time.Second)
	if err := load(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("3s off, other content: %v, want ErrCorrupt", err)
	}
}

func TestReopenAfterUIDChange(t *testing.T) {
	ctx := context.Background()
	root, _ := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "a1", "uid-1")
		mustPut(t, s, workRef, "a.ics", "a2", "uid-2")
		mustPut(t, s, workRef, "b.ics", "b", "")
	})
	s := openRoot(t, root)
	mustPut(t, s, workRef, "c.ics", "c", "uid-1")
	var conflict *store.ConflictError
	_, err := s.PutObject(ctx, workRef, "d.ics", []byte("d"), store.PutOptions{UID: "uid-2"})
	if !errors.As(err, &conflict) || conflict.Name != "a.ics" {
		t.Fatalf("put with uid-2 = %v, want a conflict with a.ics", err)
	}
}

func TestReopenAfterDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	root, _ := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "first", "uid-1")
		if err := s.DeleteObject(ctx, workRef, "a.ics", store.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		mustPut(t, s, workRef, "a.ics", "second", "uid-2")
	})
	s := openRoot(t, root)
	got, err := s.GetObject(ctx, workRef, "a.ics")
	if err != nil || string(got.Data) != "second" || got.UID != "uid-2" {
		t.Fatalf("GetObject = %+v, %v", got, err)
	}
	wantRevision(t, s, workRef, 3)
	mustPut(t, s, workRef, "b.ics", "b", "uid-1")
	ch, err := s.Changes(ctx, workRef, 1)
	if err != nil || !slices.Equal(ch.Changes, []store.Change{{Name: "a.ics"}, {Name: "b.ics"}}) {
		t.Fatalf("Changes(1) = %+v, %v", ch, err)
	}
}

func TestCorruptMiddleLineRefusesToLoad(t *testing.T) {
	ctx := context.Background()
	fine := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "fine"}
	root, dir := closedStore(t, func(s *Store) {
		mustCreate(t, s, fine, store.Props{})
		mustPut(t, s, workRef, "a.ics", "a", "")
		mustPut(t, s, workRef, "b.ics", "b", "")
		mustPut(t, s, workRef, "c.ics", "c", "")
		mustPut(t, s, fine, "f.ics", "f", "")
	})
	logPath := filepath.Join(dir, "log")
	lines := strings.SplitAfter(string(readFile(t, logPath)), "\n")
	lines[1] = "{not json\n"
	broken := []byte(strings.Join(lines, ""))
	if err := os.WriteFile(logPath, broken, 0o600); err != nil {
		t.Fatal(err)
	}

	s := openRoot(t, root)
	if _, err := s.GetCollection(ctx, workRef); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("GetCollection = %v, want ErrCorrupt", err)
	}
	if _, err := s.GetObject(ctx, workRef, "a.ics"); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("GetObject = %v, want ErrCorrupt", err)
	}
	if got := readFile(t, logPath); !bytes.Equal(got, broken) {
		t.Fatalf("log changed: %q", got)
	}
	if got, err := s.GetObject(ctx, fine, "f.ics"); err != nil || string(got.Data) != "f" {
		t.Fatalf("other collection: %+v, %v", got, err)
	}
}

func TestStrayFilesAreLeftAlone(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) { mustPut(t, s, workRef, "a.ics", "a", "") })
	stray := objPath(dir, "stray.ics")
	if err := os.WriteFile(stray, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := openRoot(t, root)
	objs, err := s.ListObjects(ctx, workRef, store.ListOptions{IncludeData: true})
	if err != nil || len(objs) != 1 || objs[0].Name != "a.ics" {
		t.Fatalf("ListObjects = %+v, %v", objs, err)
	}
	wantAbsent(t, s, workRef, "stray.ics")
	if got := readFile(t, stray); string(got) != "stray" {
		t.Fatalf("stray file = %q", got)
	}
	// A write to another name doesn't touch it either.
	mustPut(t, s, workRef, "b.ics", "b", "")
	s = reopen(t, s)
	wantAbsent(t, s, workRef, "stray.ics")
	if _, err := os.Lstat(stray); err != nil {
		t.Fatalf("stray file: %v", err)
	}
}

func TestLeftoversAreCleaned(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) { mustPut(t, s, workRef, "a.ics", "a", "") })
	kindDir := filepath.Dir(dir)
	tmpFile := filepath.Join(dir, "tmp", "w-123")
	tmpDir := filepath.Join(dir, "tmp", "half")
	staging := filepath.Join(kindDir, ".staging-0123")
	trash := filepath.Join(kindDir, ".trash-4567")
	for _, d := range []string{tmpDir, filepath.Join(staging, "objects"), filepath.Join(trash, "objects")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{tmpFile, filepath.Join(tmpDir, "x"), filepath.Join(staging, "collection.json"), filepath.Join(trash, "log")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := openRoot(t, root)
	cols, err := s.ListCollections(ctx, "alice", store.KindCalendar)
	if err != nil || len(cols) != 1 || cols[0].Ref.Name != "work" {
		t.Fatalf("ListCollections = %+v, %v", cols, err)
	}
	for _, p := range []string{tmpFile, tmpDir, staging, trash} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s: %v, want it removed", p, err)
		}
	}
	if got, err := s.GetObject(ctx, workRef, "a.ics"); err != nil || string(got.Data) != "a" {
		t.Fatalf("GetObject = %+v, %v", got, err)
	}
}

func TestFailedWriteLeavesCollectionReloadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	mustPut(t, s, workRef, "a.ics", "a", "")
	objects := filepath.Join(s.collectionDir(workRef), "objects")
	if err := os.Chmod(objects, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(objects, 0o700) })

	if _, err := s.PutObject(ctx, workRef, "b.ics", []byte("b"), store.PutOptions{UID: "uid-b"}); err == nil {
		t.Fatal("PutObject into a read-only objects directory succeeded")
	}
	wantRevision(t, s, workRef, 1)
	if err := os.Chmod(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	wantAbsent(t, s, workRef, "b.ics")
	wantRevision(t, s, workRef, 1)
	o := mustPut(t, s, workRef, "c.ics", "c", "uid-b")
	if o.UID != "uid-b" {
		t.Fatalf("put = %+v", o)
	}
	wantRevision(t, s, workRef, 2)
	ch, err := s.Changes(ctx, workRef, 1)
	if err != nil || !slices.Equal(ch.Changes, []store.Change{{Name: "c.ics"}}) {
		t.Fatalf("Changes(1) = %+v, %v", ch, err)
	}
	recs, _, err := parseLog(readFile(t, filepath.Join(s.collectionDir(workRef), "log")))
	if err != nil || len(recs) != 2 || recs[1].Name != "c.ics" || recs[1].Rev != 2 {
		t.Fatalf("log = %+v, %v", recs, err)
	}
}

func TestCorruptErrorNamesFirstBadObject(t *testing.T) {
	_, dir := closedStore(t, func(s *Store) {
		for _, n := range []string{"d.ics", "b.ics", "a.ics", "c.ics", "e.ics"} {
			mustPut(t, s, workRef, n, n, "")
		}
		mustPut(t, s, workRef, "z.ics", "z", "")
	})
	for _, n := range []string{"b.ics", "d.ics", "c.ics"} {
		if err := os.Remove(objPath(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		_, err := loadCollection(dir, defaultMaxJournal)
		wantCorrupt(t, err, "b.ics")
	}
}

func TestListCollectionsSkipsCorruptCollection(t *testing.T) {
	ctx := context.Background()
	broken := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "broken"}
	home := store.CollectionRef{Owner: "alice", Kind: store.KindCalendar, Name: "home"}
	root, dir := closedStore(t, func(s *Store) {
		mustCreate(t, s, broken, store.Props{})
		mustCreate(t, s, home, store.Props{})
		mustPut(t, s, broken, "gone.ics", "gone", "")
		mustPut(t, s, broken, "b.ics", "b", "")
	})
	if err := os.Remove(objPath(filepath.Join(filepath.Dir(dir), fileName("broken")), "gone.ics")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := openRoot(t, root, WithLogger(logger))
	cols, err := s.ListCollections(ctx, "alice", store.KindCalendar)
	if err != nil {
		t.Fatalf("ListCollections = %v", err)
	}
	var names []string
	for _, c := range cols {
		names = append(names, c.Ref.Name)
	}
	if !slices.Equal(names, []string{"home", "work"}) {
		t.Fatalf("ListCollections = %v, want home and work", names)
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "collection=broken") ||
		!strings.Contains(out, "gone.ics") {
		t.Fatalf("log output = %q, want an error naming the collection and the object", out)
	}
	_, err = s.GetCollection(ctx, broken)
	wantCorrupt(t, err, "gone.ics")
}

func TestListCollectionsFailsOnReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) {})
	meta := filepath.Join(dir, "collection.json")
	if err := os.Chmod(meta, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(meta, 0o600) })
	s := openRoot(t, root)
	if _, err := s.ListCollections(ctx, "alice", store.KindCalendar); err == nil || errors.Is(err, ErrCorrupt) {
		t.Fatalf("ListCollections = %v, want the read error", err)
	}
}

func TestWithLoggerNilKeepsDefault(t *testing.T) {
	s := openTestStore(t, WithLogger(nil))
	if s.opts.logger == nil {
		t.Fatal("WithLogger(nil) left no logger")
	}
}

func TestRecoverRollsBackSameSizeOverwriteWithinSlack(t *testing.T) {
	ctx := context.Background()
	var old store.Object
	root, dir := closedStore(t, func(s *Store) { old = mustPut(t, s, workRef, "a.ics", "aaaa", "uid-1") })
	logPath := filepath.Join(dir, "log")
	before := readFile(t, logPath)
	// Same size, and a time the old file is within the slack of.
	appendToFile(t, logPath, encodeRecord(putRecord(2, "a.ics", "bbbb", "uid-2", old.ModTime.Add(500*time.Millisecond))))

	s := openRoot(t, root)
	got, err := s.GetObject(ctx, workRef, "a.ics")
	if err != nil || string(got.Data) != "aaaa" || got.ETag != old.ETag || got.UID != "uid-1" {
		t.Fatalf("GetObject = %+v, %v; want %+v", got, err, old)
	}
	wantRevision(t, s, workRef, 1)
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Fatalf("log = %q, want %q", got, before)
	}
}

func TestFailedSameSizeOverwriteIsRolledBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	old := mustPut(t, s, workRef, "a.ics", "aaaa", "uid-1")
	tmp := filepath.Join(s.collectionDir(workRef), "tmp")
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	if _, err := s.PutObject(ctx, workRef, "a.ics", []byte("bbbb"), store.PutOptions{UID: "uid-2"}); err == nil {
		t.Fatal("PutObject with a read-only tmp directory succeeded")
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}

	check := func(s *Store, data, etag, uid string, rev uint64) {
		t.Helper()
		got, err := s.GetObject(ctx, workRef, "a.ics")
		if err != nil || string(got.Data) != data || got.ETag != etag || got.UID != uid {
			t.Fatalf("GetObject = %+v, %v; want %q", got, err, data)
		}
		wantRevision(t, s, workRef, rev)
	}
	check(s, "aaaa", old.ETag, "uid-1", 1)
	put, err := s.PutObject(ctx, workRef, "a.ics", []byte("cccc"), store.PutOptions{UID: "uid-3"})
	if err != nil {
		t.Fatal(err)
	}
	check(s, "cccc", put.ETag, "uid-3", 2)
	s = reopen(t, s)
	check(s, "cccc", put.ETag, "uid-3", 2)
	recs, _, err := parseLog(readFile(t, filepath.Join(s.collectionDir(workRef), "log")))
	if err != nil || len(recs) != 2 || recs[1].Rev != 2 || recs[1].ETag != put.ETag {
		t.Fatalf("log = %+v, %v", recs, err)
	}
}

func TestRecoverKeepsFinishedSameSizeOverwrite(t *testing.T) {
	ctx := context.Background()
	var old store.Object
	root, dir := closedStore(t, func(s *Store) { old = mustPut(t, s, workRef, "a.ics", "aaaa", "uid-1") })
	logPath := filepath.Join(dir, "log")
	mtime := old.ModTime.Add(500 * time.Millisecond)
	path := objPath(dir, "a.ics")
	if err := os.WriteFile(path, []byte("bbbb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	appendToFile(t, logPath, encodeRecord(putRecord(2, "a.ics", "bbbb", "uid-2", mtime)))
	before := readFile(t, logPath)

	s := openRoot(t, root)
	got, err := s.GetObject(ctx, workRef, "a.ics")
	if err != nil || string(got.Data) != "bbbb" || got.UID != "uid-2" {
		t.Fatalf("GetObject = %+v, %v", got, err)
	}
	wantRevision(t, s, workRef, 2)
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Fatalf("log changed: %q, was %q", got, before)
	}
}

// A delete whose file removal failed, followed by other writes and a restart,
// leaves a stray file under the deleted name. A later put of that name that
// fails before its rename must not make the collection refuse to load.
func TestLeftoverOfFailedDeleteDoesNotWedgeCollection(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	mustPut(t, s, workRef, "a.ics", "a", "")
	dir := s.collectionDir(workRef)
	path := objPath(dir, "a.ics")
	// A non-empty directory in place of the file makes the removal fail.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteObject(ctx, workRef, "a.ics", store.DeleteOptions{}); err != nil {
		t.Fatalf("DeleteObject = %v", err)
	}
	mustPut(t, s, workRef, "b.ics", "b", "") // the tombstone is no longer last
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// What a failed removal of a regular file leaves behind.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	s = openRoot(t, s.root)
	wantRevision(t, s, workRef, 3)
	tmp := filepath.Join(dir, "tmp")
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	if _, err := s.PutObject(ctx, workRef, "a.ics", []byte("new"), store.PutOptions{}); err == nil {
		t.Fatal("PutObject with a read-only tmp directory succeeded")
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}

	check := func(s *Store) {
		t.Helper()
		wantRevision(t, s, workRef, 3)
		wantAbsent(t, s, workRef, "a.ics")
		if got := readFile(t, path); string(got) != "a" {
			t.Fatalf("leftover file = %q, want it untouched", got)
		}
	}
	check(s)
	s = reopen(t, s)
	check(s)
	// A successful write replaces the leftover.
	mustPut(t, s, workRef, "a.ics", "fresh", "")
	s = reopen(t, s)
	if got, err := s.GetObject(ctx, workRef, "a.ics"); err != nil || string(got.Data) != "fresh" {
		t.Fatalf("GetObject = %+v, %v", got, err)
	}
	wantRevision(t, s, workRef, 4)
}

func TestRecoverRollsBackPutOverLeftoverOfDelete(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "old", "")
		if err := s.DeleteObject(ctx, workRef, "a.ics", store.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		mustPut(t, s, workRef, "b.ics", "b", "")
	})
	stray := objPath(dir, "a.ics")
	if err := os.WriteFile(stray, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "log")
	before := readFile(t, logPath)
	appendToFile(t, logPath, encodeRecord(putRecord(4, "a.ics", "new", "", time.Now())))

	s := openRoot(t, root)
	wantRevision(t, s, workRef, 3)
	wantAbsent(t, s, workRef, "a.ics")
	if got := readFile(t, stray); string(got) != "old" {
		t.Fatalf("stray file = %q, want it untouched", got)
	}
	if got := readFile(t, logPath); !bytes.Equal(got, before) {
		t.Fatalf("log = %q, want %q", got, before)
	}
}

func TestFailedRemovalReloadsAndFinishesDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	mustPut(t, s, workRef, "a.ics", "a", "")
	objects := filepath.Join(s.collectionDir(workRef), "objects")
	if err := os.Chmod(objects, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(objects, 0o700) })
	if err := s.DeleteObject(ctx, workRef, "a.ics", store.DeleteOptions{}); err != nil {
		t.Fatalf("DeleteObject = %v, want nil", err)
	}
	path := objPath(s.collectionDir(workRef), "a.ics")
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the removal should have failed: %v", err)
	}
	if err := os.Chmod(objects, 0o700); err != nil {
		t.Fatal(err)
	}

	wantRevision(t, s, workRef, 2) // the next call reloads the collection
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("leftover file: %v, want it removed by the reload", err)
	}
	wantAbsent(t, s, workRef, "a.ics")
}
