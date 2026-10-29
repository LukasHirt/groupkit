// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

const (
	collectionFile = "collection.json"
	logFileName    = "log"
	objectsDirName = "objects"
	tmpDirName     = "tmp"

	// Hidden directories the store makes next to collections. Only these are
	// cleaned up; other hidden entries belong to someone else.
	stagingPrefix = ".staging-"
	trashPrefix   = ".trash-"
)

// collectionMeta is the content of a collection's collection.json. The owner,
// kind and name are stored here, so nothing ever parses directory names.
type collectionMeta struct {
	ID       string
	Owner    string
	Kind     string
	Name     string
	Created  time.Time
	Props    store.Props
	Revision uint64
	// Floor is the lowest revision the change log can still answer for.
	Floor uint64
}

// objState is what the store remembers about one object in memory.
type objState struct {
	etag, uid string
	size      int64
	mtime     time.Time
	rev       uint64 // revision of the record that wrote it
}

// collection is a loaded collection. Its mutex guards every field below it.
type collection struct {
	dir string

	mu       sync.RWMutex
	meta     collectionMeta
	objects  map[string]*objState // object name to state
	uids     map[string]string    // UID to object name
	recent   []record
	logFile  *os.File
	logLines int // lines in the log file
	deleted  bool
	// stale means a write failed and memory may not match the disk. The
	// collection has left the cache, and calls that find it look it up again.
	stale bool
}

func (c *collection) snapshot() store.Collection {
	return store.Collection{
		Ref: store.CollectionRef{
			Owner: c.meta.Owner,
			Kind:  store.Kind(c.meta.Kind),
			Name:  c.meta.Name,
		},
		ID:       c.meta.ID,
		Props:    cloneProps(c.meta.Props),
		Revision: c.meta.Revision,
		Created:  c.meta.Created,
	}
}

func cloneProps(p store.Props) store.Props {
	if p.Components != nil {
		p.Components = append([]string{}, p.Components...)
	}
	return p
}

// loadCollection reads the collection in dir, replays its log and checks every
// live object against its file. It recovers from a crash: a torn last line of
// the log is cut off, and the last record is kept, rolled back or finished as
// recoverLast decides. Nothing is changed on disk unless the whole collection
// checks out. A missing collection.json returns an error wrapping
// os.ErrNotExist, and files that disagree with the log return an error wrapping
// ErrCorrupt. limit is the journal limit.
func loadCollection(dir string, limit int) (*collection, error) {
	data, err := os.ReadFile(filepath.Join(dir, collectionFile))
	if err != nil {
		return nil, err
	}
	c := &collection{
		dir:     dir,
		objects: map[string]*objState{},
		uids:    map[string]string{},
	}
	if err := json.Unmarshal(data, &c.meta); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, filepath.Join(dir, collectionFile), err)
	}
	f, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_RDWR|os.O_CREATE|os.O_APPEND, fileMode)
	if err != nil {
		return nil, err
	}
	logData, err := os.ReadFile(f.Name())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	recs, validLen, err := parseLog(logData)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%s: %w", f.Name(), err)
	}
	recs, rec, err := c.recoverLast(recs, logData, validLen)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	c.replay(recs, limit)
	if err := c.verifyObjects(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := rec.apply(f, len(logData)); err != nil {
		_ = f.Close()
		return nil, err
	}
	c.clearTmp()
	c.logFile = f
	return c, nil
}

func (c *collection) writeMeta(m collectionMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(c.dir, filepath.Join(c.dir, tmpDirName), collectionFile, append(data, '\n'), time.Time{})
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func kindDirName(k store.Kind) string {
	if k == store.KindAddressBook {
		return "addressbooks"
	}
	return "calendars"
}

func (s *Store) kindDir(owner string, kind store.Kind) string {
	return filepath.Join(s.OwnerDir(owner), kindDirName(kind))
}

func (s *Store) collectionDir(ref store.CollectionRef) string {
	return filepath.Join(s.kindDir(ref.Owner, ref.Kind), fileName(ref.Name))
}

// ErrClosed is returned by every call on a store after Close.
var ErrClosed = errors.New("filestore: store is closed")

// loadCached returns the collection in dir, loading it when it isn't cached.
// The caller holds s.mu.
func (s *Store) loadCached(dir string) (*collection, error) {
	if c, ok := s.cols[dir]; ok {
		return c, nil
	}
	c, err := loadCollection(dir, s.opts.maxJournal)
	if err != nil {
		return nil, err
	}
	s.cols[dir] = c
	return c, nil
}

// lookup returns the collection ref names, or store.ErrNotFound.
func (s *Store) lookup(ctx context.Context, ref store.CollectionRef) (*collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.ValidateRef(ref); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	c, err := s.loadCached(s.collectionDir(ref))
	if errors.Is(err, os.ErrNotExist) {
		return nil, store.ErrNotFound
	}
	return c, err
}

// evict drops c from the cache if it is still the cached entry for its
// directory.
func (s *Store) evict(c *collection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cols[c.dir] == c {
		delete(s.cols, c.dir)
	}
}
