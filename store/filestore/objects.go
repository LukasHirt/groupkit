// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// acquire returns the collection ref names, locked for writing or for reading,
// and the function that unlocks it. When a failed write dropped the collection
// from the cache while this call waited for its lock, it looks the collection up
// again, so the call works on what is on disk.
func (s *Store) acquire(ctx context.Context, ref store.CollectionRef, write bool) (*collection, func(), error) {
	for {
		c, err := s.lookup(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		unlock := c.mu.RUnlock
		if write {
			c.mu.Lock()
			unlock = c.mu.Unlock
		} else {
			c.mu.RLock()
		}
		switch {
		case c.stale:
			unlock()
			s.evict(c) // the failed writer may not have done so yet
			continue
		case c.deleted:
			unlock()
			return nil, nil, fmt.Errorf("%w: collection %q", store.ErrNotFound, ref.Name)
		case c.logFile == nil:
			unlock()
			return nil, nil, ErrClosed
		}
		return c, unlock, nil
	}
}

// GetObject returns one object with its data, or store.ErrNotFound.
func (s *Store) GetObject(ctx context.Context, ref store.CollectionRef, name string) (store.Object, error) {
	if err := store.ValidateName(name); err != nil {
		return store.Object{}, err
	}
	c, unlock, err := s.acquire(ctx, ref, false)
	if err != nil {
		return store.Object{}, err
	}
	defer unlock()
	o, ok := c.objects[name]
	if !ok {
		return store.Object{}, fmt.Errorf("%w: object %q", store.ErrNotFound, name)
	}
	data, err := c.readObject(name, o)
	if err != nil {
		return store.Object{}, err
	}
	return o.object(name, data), nil
}

// ListObjects returns the collection's objects sorted by name. Without
// IncludeData it reads no files.
func (s *Store) ListObjects(ctx context.Context, ref store.CollectionRef, opts store.ListOptions) ([]store.Object, error) {
	c, unlock, err := s.acquire(ctx, ref, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	names := make([]string, 0, len(c.objects))
	for name := range c.objects {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]store.Object, 0, len(names))
	for _, name := range names {
		o := c.objects[name]
		var data []byte
		if opts.IncludeData {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if data, err = c.readObject(name, o); err != nil {
				return nil, err
			}
		}
		out = append(out, o.object(name, data))
	}
	return out, nil
}

// PutObject creates or replaces an object and returns the stored object.
func (s *Store) PutObject(ctx context.Context, ref store.CollectionRef, name string, data []byte, opts store.PutOptions) (store.Object, error) {
	if err := store.ValidateName(name); err != nil {
		return store.Object{}, err
	}
	c, unlock, err := s.acquire(ctx, ref, true)
	if err != nil {
		return store.Object{}, err
	}
	obj, err := c.put(name, data, opts, s.opts.maxJournal)
	if err == nil {
		s.compactAfterWrite(c)
	}
	stale := c.stale
	unlock()
	if stale {
		s.evict(c)
	}
	return obj, err
}

// DeleteObject removes an object or returns store.ErrNotFound.
func (s *Store) DeleteObject(ctx context.Context, ref store.CollectionRef, name string, opts store.DeleteOptions) error {
	if err := store.ValidateName(name); err != nil {
		return err
	}
	c, unlock, err := s.acquire(ctx, ref, true)
	if err != nil {
		return err
	}
	err = c.remove(name, opts, s.opts.maxJournal)
	if err == nil && !c.stale { // a stale collection is reloaded, not compacted
		s.compactAfterWrite(c)
	}
	stale := c.stale
	unlock()
	if stale {
		s.evict(c)
	}
	return err
}

// Changes lists what changed after the revision since, collapsed per object.
func (s *Store) Changes(ctx context.Context, ref store.CollectionRef, since uint64) (store.Changes, error) {
	c, unlock, err := s.acquire(ctx, ref, false)
	if err != nil {
		return store.Changes{}, err
	}
	defer unlock()
	out := store.Changes{Revision: c.meta.Revision}
	if since == 0 {
		for name := range c.objects {
			out.Changes = append(out.Changes, store.Change{Name: name})
		}
	} else {
		if since > c.meta.Revision || since < c.meta.Floor {
			return store.Changes{}, fmt.Errorf("%w: revision %d (collection is at %d, oldest answerable is %d)",
				store.ErrChangesUnavailable, since, c.meta.Revision, c.meta.Floor)
		}
		final := map[string]bool{}
		for _, r := range c.recent {
			if r.Rev > since {
				final[r.Name] = r.Deleted
			}
		}
		for name, deleted := range final {
			out.Changes = append(out.Changes, store.Change{Name: name, Deleted: deleted})
		}
	}
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].Name < out.Changes[j].Name })
	return out, nil
}

// object returns the object o describes, with data as its Data.
func (o *objState) object(name string, data []byte) store.Object {
	return store.Object{Name: name, Data: data, ETag: o.etag, Size: o.size, ModTime: o.mtime, UID: o.uid}
}

func (c *collection) objectPath(name string) string {
	return filepath.Join(c.dir, objectsDirName, fileName(name))
}

// readObject reads the file of object name and checks it against its record.
// The caller holds c.mu.
func (c *collection) readObject(name string, o *objState) ([]byte, error) {
	data, err := os.ReadFile(c.objectPath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s: object %q: file is missing", ErrCorrupt, c.dir, name)
	}
	if err != nil {
		return nil, err
	}
	if store.ETag(data) != o.etag {
		return nil, fmt.Errorf("%w: %s: object %q: content does not match its record", ErrCorrupt, c.dir, name)
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// put writes an object. The caller holds c.mu for writing. If the log or the
// data could not be written, put marks c stale and the caller evicts it.
func (c *collection) put(name string, data []byte, opts store.PutOptions, limit int) (store.Object, error) {
	old, exists := c.objects[name]
	if opts.IfNoneMatch && exists {
		return store.Object{}, fmt.Errorf("%w: %q already exists", store.ErrPreconditionFailed, name)
	}
	if opts.IfMatch != "" && (!exists || old.etag != opts.IfMatch) {
		return store.Object{}, fmt.Errorf("%w: %q does not match", store.ErrPreconditionFailed, name)
	}
	etag := store.ETag(data)
	if exists && old.etag == etag && old.uid == opts.UID {
		return old.object(name, append([]byte{}, data...)), nil
	}
	if opts.UID != "" {
		if other, taken := c.uids[opts.UID]; taken && other != name {
			return store.Object{}, &store.ConflictError{UID: opts.UID, Name: other}
		}
	}

	mtime := time.Unix(0, time.Now().UnixNano()).UTC()
	r := record{
		Rev:   c.meta.Revision + 1,
		Name:  name,
		ETag:  etag,
		UID:   opts.UID,
		Size:  int64(len(data)),
		MTime: mtime.UnixNano(),
	}
	if err := c.appendLog(r); err != nil {
		c.markStale()
		return store.Object{}, err
	}
	objDir := filepath.Join(c.dir, objectsDirName)
	if err := atomicWriteFile(objDir, filepath.Join(c.dir, tmpDirName), fileName(name), data, mtime); err != nil {
		c.markStale()
		return store.Object{}, err
	}
	c.apply(r, limit)
	return c.objects[name].object(name, append([]byte{}, data...)), nil
}

// remove deletes an object. The caller holds c.mu for writing. Like put, it
// marks c stale when the disk could not be brought up to date.
func (c *collection) remove(name string, opts store.DeleteOptions, limit int) error {
	o, ok := c.objects[name]
	if !ok {
		return fmt.Errorf("%w: object %q", store.ErrNotFound, name)
	}
	if opts.IfMatch != "" && o.etag != opts.IfMatch {
		return fmt.Errorf("%w: %q does not match", store.ErrPreconditionFailed, name)
	}
	r := record{
		Rev:     c.meta.Revision + 1,
		Name:    name,
		Deleted: true,
		MTime:   time.Now().UnixNano(),
	}
	if err := c.appendLog(r); err != nil {
		c.markStale()
		return err
	}
	// The tombstone is on disk, so the delete is committed. Removing the file is
	// best effort. When it fails, c is given up so the next call reloads it
	// while the tombstone is still the last record, and recovery removes the
	// file then.
	err := os.Remove(c.objectPath(name))
	c.apply(r, limit)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.markStale()
	}
	return nil
}

// appendLog appends r to the log and flushes it to disk.
func (c *collection) appendLog(r record) error {
	if _, err := c.logFile.Write(encodeRecord(r)); err != nil {
		return err
	}
	if err := syncFile(c.logFile); err != nil {
		return err
	}
	c.logLines++
	return nil
}

// markStale gives up on c after a failed write: its memory may no longer match
// the disk. Calls waiting for its lock look the collection up again, and the
// next load reads the disk.
func (c *collection) markStale() {
	c.stale = true
	if c.logFile != nil {
		_ = c.logFile.Close()
		c.logFile = nil
	}
}

// apply brings memory up to date with a record that is on disk, and trims the
// change journal to limit records.
func (c *collection) apply(r record, limit int) {
	c.applyObject(r)
	c.meta.Revision = r.Rev
	c.recent = append(c.recent, r)
	c.trimJournal(limit)
}

// applyObject updates the object and the UID index for one record.
func (c *collection) applyObject(r record) {
	if old, ok := c.objects[r.Name]; ok && old.uid != "" && c.uids[old.uid] == r.Name {
		delete(c.uids, old.uid)
	}
	if r.Deleted {
		delete(c.objects, r.Name)
	} else {
		c.objects[r.Name] = &objState{
			etag:  r.ETag,
			uid:   r.UID,
			size:  r.Size,
			mtime: time.Unix(0, r.MTime).UTC(),
			rev:   r.Rev,
		}
		if r.UID != "" {
			c.uids[r.UID] = r.Name
		}
	}
}

// trimJournal drops the oldest records of the change journal beyond limit and
// raises the floor to the highest revision it drops.
func (c *collection) trimJournal(limit int) {
	if n := len(c.recent) - limit; n > 0 {
		// Only memory is trimmed here; compaction persists the floor.
		c.meta.Floor = c.recent[n-1].Rev
		c.recent = c.recent[n:]
	}
}
