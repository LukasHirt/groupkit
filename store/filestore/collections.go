// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// ListCollections returns the owner's collections of one kind, sorted by name.
// A collection that is damaged (its load fails with ErrCorrupt) is left out
// and reported to the logger set with WithLogger; calls on it keep returning
// the error. Any other failure to load a collection fails the listing.
func (s *Store) ListCollections(ctx context.Context, owner string, kind store.Kind) ([]store.Collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.ValidateOwner(owner); err != nil {
		return nil, err
	}
	if !kind.Valid() {
		return nil, store.ValidateRef(store.CollectionRef{Owner: owner, Kind: kind, Name: "x"})
	}
	dir := s.kindDir(owner, kind)

	// Holding s.mu keeps the cleanup from removing a directory that a Create
	// in this process is still staging.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			return []store.Collection{}, nil
		}
		return nil, err
	}
	var cols []*collection
	type damage struct {
		dir string
		err error
	}
	var damaged []damage
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), stagingPrefix) || strings.HasPrefix(e.Name(), trashPrefix) {
			_ = os.RemoveAll(path) // best effort: leftovers of an interrupted create or delete
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue // not ours, and never a collection: fileName never starts with "."
		}
		if !e.IsDir() {
			continue
		}
		c, err := s.loadCached(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if errors.Is(err, ErrCorrupt) {
			// One damaged collection must not hide the others. Calls on it
			// still return the error.
			damaged = append(damaged, damage{path, err})
			continue
		}
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		cols = append(cols, c)
	}
	s.mu.Unlock()
	for _, d := range damaged {
		s.opts.logger.Error("filestore: leaving a damaged collection out of the listing",
			"owner", owner, "kind", string(kind), "collection", collectionNameAt(d.dir), "error", d.err)
	}

	out := make([]store.Collection, 0, len(cols))
	for _, c := range cols {
		c.mu.RLock()
		if !c.deleted {
			out = append(out, c.snapshot())
		}
		c.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Name < out[j].Ref.Name })
	return out, nil
}

// collectionNameAt returns the name in the collection.json of dir, or the
// directory's own name when that cannot be read.
func collectionNameAt(dir string) string {
	var meta collectionMeta
	data, err := os.ReadFile(filepath.Join(dir, collectionFile))
	if err != nil || json.Unmarshal(data, &meta) != nil || meta.Name == "" {
		return filepath.Base(dir)
	}
	return meta.Name
}

// GetCollection returns one collection or store.ErrNotFound.
func (s *Store) GetCollection(ctx context.Context, ref store.CollectionRef) (store.Collection, error) {
	c, unlock, err := s.acquire(ctx, ref, false)
	if err != nil {
		return store.Collection{}, err
	}
	defer unlock()
	return c.snapshot(), nil
}

// CreateCollection makes a collection or returns store.ErrExists.
func (s *Store) CreateCollection(ctx context.Context, ref store.CollectionRef, props store.Props) (store.Collection, error) {
	if err := ctx.Err(); err != nil {
		return store.Collection{}, err
	}
	if err := store.ValidateRef(ref); err != nil {
		return store.Collection{}, err
	}
	id, err := newID()
	if err != nil {
		return store.Collection{}, err
	}
	suffix, err := newID()
	if err != nil {
		return store.Collection{}, err
	}
	meta := collectionMeta{
		ID:      id,
		Owner:   ref.Owner,
		Kind:    string(ref.Kind),
		Name:    ref.Name,
		Created: time.Now().UTC().Round(0),
		Props:   cloneProps(props),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return store.Collection{}, ErrClosed
	}
	kindDir := s.kindDir(ref.Owner, ref.Kind)
	if err := os.MkdirAll(kindDir, dirMode); err != nil {
		return store.Collection{}, err
	}
	staging := filepath.Join(kindDir, stagingPrefix+suffix)
	if err := stageCollection(staging, meta); err != nil {
		_ = os.RemoveAll(staging)
		return store.Collection{}, err
	}
	final := s.collectionDir(ref)
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		if errors.Is(err, fs.ErrExist) { // EEXIST or ENOTEMPTY
			return store.Collection{}, store.ErrExists
		}
		return store.Collection{}, err
	}
	// The rename succeeded, so any cached collection here was deleted.
	delete(s.cols, final)
	// The collection exists now, so a failed flush of the rename is not an
	// error to report: the caller would believe it was never created.
	_ = syncDir(kindDir)
	c := &collection{meta: meta}
	return c.snapshot(), nil
}

// stageCollection writes a complete, empty collection into dir.
func stageCollection(dir string, meta collectionMeta) error {
	if err := os.Mkdir(dir, dirMode); err != nil {
		return err
	}
	for _, sub := range []string{objectsDirName, tmpDirName} {
		if err := os.Mkdir(filepath.Join(dir, sub), dirMode); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	c := &collection{dir: dir}
	if err := c.writeMeta(meta); err != nil {
		return err
	}
	return syncDir(dir)
}

// UpdateCollection changes properties. The revision only increases when a
// property changes.
func (s *Store) UpdateCollection(ctx context.Context, ref store.CollectionRef, u store.PropsUpdate) (store.Collection, error) {
	c, unlock, err := s.acquire(ctx, ref, true)
	if err != nil {
		return store.Collection{}, err
	}
	defer unlock()
	next := c.meta
	next.Props = cloneProps(c.meta.Props)
	p := &next.Props
	if u.DisplayName != nil {
		p.DisplayName = *u.DisplayName
	}
	if u.Description != nil {
		p.Description = *u.Description
	}
	if u.Color != nil {
		p.Color = *u.Color
	}
	if u.Order != nil {
		p.Order = *u.Order
	}
	if u.Timezone != nil {
		p.Timezone = *u.Timezone
	}
	if u.Components != nil {
		p.Components = append([]string{}, *u.Components...)
	}
	old := c.meta.Props
	if old.DisplayName == p.DisplayName && old.Description == p.Description && old.Color == p.Color &&
		old.Order == p.Order && old.Timezone == p.Timezone && slices.Equal(old.Components, p.Components) {
		return c.snapshot(), nil
	}
	next.Revision++
	if err := c.writeMeta(next); err != nil {
		return store.Collection{}, err
	}
	c.meta = next
	return c.snapshot(), nil
}

// DeleteCollection removes a collection and all its objects.
func (s *Store) DeleteCollection(ctx context.Context, ref store.CollectionRef) error {
	suffix, err := newID()
	if err != nil {
		return err
	}
	c, unlock, err := s.acquire(ctx, ref, true)
	if err != nil {
		return err
	}
	if err := func() error {
		defer unlock()
		c.deleted = true
		trash := filepath.Join(filepath.Dir(c.dir), trashPrefix+suffix)
		if err := os.Rename(c.dir, trash); err != nil {
			c.deleted = false
			return err
		}
		if c.logFile != nil {
			_ = c.logFile.Close()
			c.logFile = nil
		}
		_ = syncDir(filepath.Dir(c.dir))
		_ = os.RemoveAll(trash) // a leftover is removed by the next listing
		return nil
	}(); err != nil {
		return err
	}
	s.evict(c)
	return nil
}
