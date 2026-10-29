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
	"testing"

	"github.com/LukasHirt/groupkit/store"
)

// The log is written before the data. When the log cannot be written, the
// object's file must still hold the old bytes, or the next load would find a
// file that disagrees with an older record and refuse the collection.
func TestFailedLogAppendLeavesDataUntouched(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	mustPut(t, s, workRef, "a.ics", "old", "uid-a")
	mustPut(t, s, workRef, "b.ics", "b", "uid-b")
	dir := s.collectionDir(workRef)

	c, unlock, err := s.acquire(ctx, workRef, true)
	if err != nil {
		t.Fatal(err)
	}
	// The next append fails: the log file is closed behind the store's back.
	if err := c.logFile.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := s.PutObject(ctx, workRef, "a.ics", []byte("new and longer"), store.PutOptions{UID: "uid-a"}); err == nil {
		t.Fatal("PutObject with a closed log succeeded")
	}
	if got := readFile(t, objPath(dir, "a.ics")); string(got) != "old" {
		t.Fatalf("a.ics holds %q after a failed log append, want %q", got, "old")
	}

	s = reopen(t, s)
	got, err := s.GetObject(ctx, workRef, "a.ics")
	if err != nil || string(got.Data) != "old" {
		t.Fatalf("GetObject(a.ics) = %q, %v; want %q", got.Data, err, "old")
	}
	wantRevision(t, s, workRef, 2)
}

// Compaction writes the new floor before it replaces the log, so a failure in
// between leaves a higher floor with the old log, which loads fine.
func TestCompactionWritesFloorBeforeLog(t *testing.T) {
	const limit = 3
	s := openTestStore(t, WithMaxJournal(limit))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	// 2*3+1 lines with one live object: the next write compacts.
	for i := range 7 {
		mustPut(t, s, workRef, "a.ics", fmt.Sprintf("a %d", i), "uid-a")
	}
	oldLog := readFile(t, filepath.Join(dir, logFileName))

	renameFile = func(string, string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { renameFile = os.Rename })
	mustPut(t, s, workRef, "a.ics", "a 7", "uid-a")
	renameFile = os.Rename

	if floor := readMetaFile(t, dir).Floor; floor != 8-limit {
		t.Fatalf("persisted floor = %d after the log rename failed, want %d", floor, 8-limit)
	}
	if got := readFile(t, filepath.Join(dir, logFileName)); !bytes.HasPrefix(got, oldLog) {
		t.Fatal("the old log was replaced although the rename failed")
	}
	want := captureState(t, s, workRef, lastRevisions(8, limit))
	s = reopen(t, s, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, lastRevisions(8, limit)), want)
}

// A put is only acknowledged once its log record is flushed. When the flush
// fails, the put fails, and the next load rolls the record back.
func TestFailedLogFlushFailsTheWrite(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	mustPut(t, s, workRef, "a.ics", "a", "uid-a")

	syncFile = func(*os.File) error { return errors.New("injected flush failure") }
	t.Cleanup(func() { syncFile = (*os.File).Sync })
	if _, err := s.PutObject(ctx, workRef, "b.ics", []byte("b"), store.PutOptions{UID: "uid-b"}); err == nil {
		t.Fatal("PutObject succeeded although the log could not be flushed")
	}
	syncFile = (*os.File).Sync

	// The record reached the log without its file, so the next load rolls it
	// back.
	s = reopen(t, s)
	wantAbsent(t, s, workRef, "b.ics")
	wantRevision(t, s, workRef, 1)
}

// Cutting a torn line off the log is flushed before the collection is used.
// When that flush fails, the load fails instead of appending after a cut that
// may not be on disk.
func TestFailedFlushOfTruncatedLogFailsTheLoad(t *testing.T) {
	ctx := context.Background()
	root, dir := closedStore(t, func(s *Store) {
		mustPut(t, s, workRef, "a.ics", "a", "")
	})
	logPath := filepath.Join(dir, logFileName)
	clean := readFile(t, logPath)
	appendToFile(t, logPath, []byte(`{"r":2,"n":"b.i`))

	syncFile = func(*os.File) error { return errors.New("injected flush failure") }
	t.Cleanup(func() { syncFile = (*os.File).Sync })
	s := openRoot(t, root)
	if _, err := s.GetCollection(ctx, workRef); err == nil {
		t.Fatal("GetCollection succeeded although the truncated log could not be flushed")
	}
	syncFile = (*os.File).Sync

	wantRevision(t, s, workRef, 1)
	if got := readFile(t, logPath); !bytes.Equal(got, clean) {
		t.Fatalf("log = %q, want %q", got, clean)
	}
}
