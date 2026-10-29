// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// logLineCount returns the number of lines in the log of the collection in dir.
func logLineCount(t *testing.T, dir string) int {
	t.Helper()
	return bytes.Count(readFile(t, filepath.Join(dir, logFileName)), []byte{'\n'})
}

// lastLogLine returns the last line of the log of the collection in dir.
func lastLogLine(t *testing.T, dir string) []byte {
	t.Helper()
	data := readFile(t, filepath.Join(dir, logFileName))
	if len(data) == 0 {
		t.Fatal("log is empty")
	}
	start := bytes.LastIndexByte(data[:len(data)-1], '\n') + 1
	return data[start:]
}

func readMetaFile(t *testing.T, dir string) collectionMeta {
	t.Helper()
	var m collectionMeta
	if err := json.Unmarshal(readFile(t, filepath.Join(dir, collectionFile)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func writeMetaFile(t *testing.T, dir string, m collectionMeta) {
	t.Helper()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, collectionFile), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// churn runs n writes over names objects: puts with a UID per name, with every
// seventh write that finds its object deleting it instead, so objects are
// deleted and created again.
func churn(t *testing.T, s *Store, ref store.CollectionRef, n, names int, each func()) {
	t.Helper()
	ctx := context.Background()
	live := map[string]bool{}
	for i := range n {
		name := fmt.Sprintf("n%02d.ics", i%names)
		if live[name] && i%7 == 0 {
			if err := s.DeleteObject(ctx, ref, name, store.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			live[name] = false
		} else {
			mustPut(t, s, ref, name, fmt.Sprintf("data %d", i), "uid-"+name)
			live[name] = true
		}
		if each != nil {
			each()
		}
	}
}

// stateOf is everything a client can see of a collection.
type stateOf struct {
	revision uint64
	objects  []store.Object
	all      store.Changes
	recent   map[uint64]store.Changes
}

func captureState(t *testing.T, s *Store, ref store.CollectionRef, since []uint64) stateOf {
	t.Helper()
	ctx := context.Background()
	col, err := s.GetCollection(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := s.ListObjects(ctx, ref, store.ListOptions{IncludeData: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		got, err := s.GetObject(ctx, ref, o.Name)
		if err != nil || !sameObjects([]store.Object{got}, []store.Object{o}) {
			t.Fatalf("GetObject(%s) = %+v, %v; want %+v", o.Name, got, err, o)
		}
	}
	all, err := s.Changes(ctx, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	st := stateOf{revision: col.Revision, objects: objs, all: all, recent: map[uint64]store.Changes{}}
	for _, r := range since {
		ch, err := s.Changes(ctx, ref, r)
		if err != nil {
			t.Fatalf("Changes(%d) = %v", r, err)
		}
		st.recent[r] = ch
	}
	return st
}

func sameState(t *testing.T, got, want stateOf) {
	t.Helper()
	if got.revision != want.revision {
		t.Errorf("revision = %d, want %d", got.revision, want.revision)
	}
	if !sameObjects(got.objects, want.objects) {
		t.Errorf("objects = %+v, want %+v", got.objects, want.objects)
	}
	if !reflect.DeepEqual(got.all, want.all) {
		t.Errorf("Changes(0) = %+v, want %+v", got.all, want.all)
	}
	if !reflect.DeepEqual(got.recent, want.recent) {
		t.Errorf("recent changes = %+v, want %+v", got.recent, want.recent)
	}
}

func lastRevisions(rev uint64, n int) []uint64 {
	var out []uint64
	for i := 1; i <= n; i++ {
		out = append(out, rev-uint64(i))
	}
	return out
}

func TestLogStaysBounded(t *testing.T) {
	const limit, names = 10, 20
	s := openTestStore(t, WithMaxJournal(limit))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	most := 0
	churn(t, s, workRef, 500, names, func() {
		most = max(most, logLineCount(t, dir))
	})
	if most >= 3*(limit+names) {
		t.Fatalf("log grew to %d lines, want fewer than %d", most, 3*(limit+names))
	}
	before := captureState(t, s, workRef, lastRevisions(500, limit))
	s = reopen(t, s, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, lastRevisions(500, limit)), before)
}

func TestCompactionKeepsState(t *testing.T) {
	ctx := context.Background()
	const limit = 10
	s := openTestStore(t, WithMaxJournal(limit))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	const writes = 200
	churn(t, s, workRef, writes, 15, nil)
	since := lastRevisions(writes, limit)
	before := captureState(t, s, workRef, since)

	if n := logLineCount(t, dir); n >= writes {
		t.Fatalf("log has %d lines after %d writes, want it compacted", n, writes)
	}
	floor := readMetaFile(t, dir).Floor
	if floor == 0 || floor > writes-limit {
		t.Fatalf("persisted floor = %d, want between 1 and %d", floor, writes-limit)
	}

	s = reopen(t, s, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, since), before)
	if _, err := s.Changes(ctx, workRef, floor-1); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Fatalf("Changes(%d) = %v, want ErrChangesUnavailable", floor-1, err)
	}
	// Every live object keeps its UID, so another name cannot take it.
	for _, o := range before.objects {
		_, err := s.PutObject(ctx, workRef, "other.ics", []byte("x"), store.PutOptions{UID: o.UID})
		var conflict *store.ConflictError
		if !errors.As(err, &conflict) || conflict.Name != o.Name {
			t.Fatalf("PutObject with UID %q = %v, want a conflict with %s", o.UID, err, o.Name)
		}
	}
}

func TestCompactionCrashBetweenFloorAndLog(t *testing.T) {
	ctx := context.Background()
	const limit = 10
	s := openTestStore(t, WithMaxJournal(limit))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	// 25 writes over 20 names stay below the compaction threshold.
	churn(t, s, workRef, 25, 20, nil)
	lines := logLineCount(t, dir)
	if lines != 25 {
		t.Fatalf("log has %d lines, want 25 (no compaction yet)", lines)
	}
	const floor = 18
	before := captureState(t, s, workRef, []uint64{floor, 20, 24})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The process died after writing the new floor and before replacing the log.
	m := readMetaFile(t, dir)
	m.Floor = floor
	writeMetaFile(t, dir, m)

	s = openRoot(t, s.root, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, []uint64{floor, 20, 24}), before)
	if _, err := s.Changes(ctx, workRef, floor-1); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Fatalf("Changes(%d) = %v, want ErrChangesUnavailable", floor-1, err)
	}
	if n := logLineCount(t, dir); n != lines {
		t.Fatalf("log has %d lines after the reopen, want %d", n, lines)
	}
}

func TestCompactionKeepsLastRecordIntact(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	churn(t, s, workRef, 60, 5, nil)
	mustPut(t, s, workRef, "a.ics", "last write", "uid-a")
	last := lastLogLine(t, dir)
	before := captureState(t, s, workRef, nil)

	const limit = 3
	s = reopen(t, s, WithMaxJournal(limit))
	c, unlock, err := s.acquire(ctx, workRef, true)
	if err != nil {
		t.Fatal(err)
	}
	err = c.maybeCompact(limit)
	// A live object whose latest record is older than the journal: after
	// compaction, its record in the log is a state record.
	var old string
	for name, o := range c.objects {
		if o.rev <= c.meta.Floor {
			old = name
		}
	}
	unlock()
	if old == "" {
		t.Fatal("no live object older than the journal")
	}
	if err != nil {
		t.Fatalf("maybeCompact = %v", err)
	}
	if n := logLineCount(t, dir); n > limit+len(before.objects) {
		t.Fatalf("log has %d lines after compaction, want at most %d", n, limit+len(before.objects))
	}
	if got := lastLogLine(t, dir); !bytes.Equal(got, last) {
		t.Fatalf("last log line = %q, want %q", got, last)
	}
	sameState(t, captureState(t, s, workRef, nil), before)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Two writes in flight when the process died, one at a time: a create and an
	// overwrite of an object whose only record is now a state record.
	compacted := readFile(t, filepath.Join(dir, logFileName))
	for _, name := range []string{"ghost.ics", old} {
		appendToFile(t, filepath.Join(dir, logFileName),
			encodeRecord(putRecord(before.revision+1, name, "never written", "", time.Now())))
		s = openRoot(t, s.root, WithMaxJournal(limit))
		sameState(t, captureState(t, s, workRef, nil), before)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(dir, logFileName)); !bytes.Equal(got, compacted) {
			t.Fatalf("log after rolling back %s = %q, want %q", name, got, compacted)
		}
	}
}

func TestCompactionFailureIsNonFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not stop root")
	}
	ctx := context.Background()
	const limit = 3
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := openTestStore(t, WithMaxJournal(limit), WithLogger(logger))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)

	// Two live objects: compaction starts above 2*3+2 = 8 lines.
	mustPut(t, s, workRef, "b.ics", "b", "uid-b")
	for i := range 7 {
		mustPut(t, s, workRef, "a.ics", fmt.Sprintf("a %d", i), "uid-a")
	}
	if n := logLineCount(t, dir); n != 8 {
		t.Fatalf("log has %d lines, want 8", n)
	}

	// The delete leaves one live object and nine lines, so it compacts, and
	// compaction cannot write to tmp/. The delete itself does not need tmp/.
	tmp := filepath.Join(dir, tmpDirName)
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o700) })
	if err := s.DeleteObject(ctx, workRef, "b.ics", store.DeleteOptions{}); err != nil {
		t.Fatalf("DeleteObject = %v, want nil although compaction failed", err)
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "compact") {
		t.Fatalf("log output = %q, want a warning about compaction", out)
	}
	if n := logLineCount(t, dir); n != 9 {
		t.Fatalf("log has %d lines after the failed compaction, want 9", n)
	}
	wantAbsent(t, s, workRef, "b.ics")
	before := captureState(t, s, workRef, []uint64{6, 8})
	if before.revision != 9 || len(before.objects) != 1 {
		t.Fatalf("state = %+v, want revision 9 and one object", before)
	}

	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, workRef, "c.ics", "c", "uid-c")
	if n := logLineCount(t, dir); n > limit+2 {
		t.Fatalf("log has %d lines after the next write, want at most %d", n, limit+2)
	}
	after := captureState(t, s, workRef, []uint64{8})
	s = reopen(t, s, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, []uint64{8}), after)
}

func TestPersistedFloorIsUsedOnLoad(t *testing.T) {
	ctx := context.Background()
	const limit = 5
	s := openTestStore(t, WithMaxJournal(limit))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	churn(t, s, workRef, 50, 4, nil)
	floor := readMetaFile(t, dir).Floor
	if floor == 0 {
		t.Fatal("no floor was persisted")
	}

	// A much larger journal limit cannot bring back what compaction dropped.
	s = reopen(t, s)
	if _, err := s.Changes(ctx, workRef, floor-1); !errors.Is(err, store.ErrChangesUnavailable) {
		t.Fatalf("Changes(%d) = %v, want ErrChangesUnavailable", floor-1, err)
	}
	ch, err := s.Changes(ctx, workRef, floor)
	if err != nil || ch.Revision != 50 || len(ch.Changes) == 0 {
		t.Fatalf("Changes(%d) = %+v, %v; want the changes up to 50", floor, ch, err)
	}
}

// openFDs returns the number of open file descriptors of the process, or -1
// when the system does not list them.
func openFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		d, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, err := d.Readdirnames(-1)
		_ = d.Close()
		if err == nil {
			return len(names)
		}
	}
	return -1
}

func TestCompactionRenameFailureClosesTempLog(t *testing.T) {
	ctx := context.Background()
	const limit, attempts = 3, 20
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	s := openTestStore(t, WithMaxJournal(limit), WithLogger(logger))
	mustCreate(t, s, workRef, store.Props{})
	dir := s.collectionDir(workRef)
	// 2*3+1 lines with one live object: every later write compacts.
	for i := range 7 {
		mustPut(t, s, workRef, "a.ics", fmt.Sprintf("a %d", i), "uid-a")
	}

	renameFile = func(string, string) error { return errors.New("injected rename failure") }
	t.Cleanup(func() { renameFile = os.Rename })
	before := openFDs(t)
	for i := range attempts {
		if _, err := s.PutObject(ctx, workRef, "a.ics", fmt.Appendf(nil, "b %d", i), store.PutOptions{UID: "uid-a"}); err != nil {
			t.Fatalf("PutObject = %v, want nil although compaction failed", err)
		}
	}
	after := openFDs(t)
	t.Logf("descriptors: %d before, %d after", before, after)
	if before >= 0 && after-before >= attempts/2 {
		t.Fatalf("open descriptors grew from %d to %d over %d failed compactions", before, after, attempts)
	}
	if out := buf.String(); strings.Count(out, "level=WARN") != attempts ||
		!strings.Contains(out, "injected rename failure") || strings.Contains(out, "invalid argument") {
		t.Fatalf("log output = %q, want %d warnings naming only the rename failure", out, attempts)
	}
	entries, err := os.ReadDir(filepath.Join(dir, tmpDirName))
	if err != nil || len(entries) != 0 {
		t.Fatalf("tmp/ holds %v (%v), want nothing", entries, err)
	}
	if n := logLineCount(t, dir); n != 7+attempts {
		t.Fatalf("log has %d lines, want %d (not compacted)", n, 7+attempts)
	}

	renameFile = os.Rename
	mustPut(t, s, workRef, "c.ics", "c", "uid-c")
	if n := logLineCount(t, dir); n > limit+2 {
		t.Fatalf("log has %d lines after compaction, want at most %d", n, limit+2)
	}
	rev := uint64(7 + attempts + 1)
	want := captureState(t, s, workRef, lastRevisions(rev, limit))
	s = reopen(t, s, WithMaxJournal(limit))
	sameState(t, captureState(t, s, workRef, lastRevisions(rev, limit)), want)
}
