// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package memory is an in-memory [store.Store] for tests and examples. Nothing
// survives the process.
package memory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// DefaultMaxJournal is how many object changes each collection's journal keeps.
const DefaultMaxJournal = 10000

// Store is an in-memory store. It is safe for concurrent use.
type Store struct {
	mu          sync.Mutex
	maxJournal  int
	nextID      uint64
	collections map[key]*collection
}

var _ store.Store = (*Store)(nil)

type key struct {
	owner string
	kind  store.Kind
	name  string
}

type collection struct {
	id       string
	props    store.Props
	revision uint64
	created  time.Time
	objects  map[string]*object
	uids     map[string]string // UID -> object name
	journal  []record
	floor    uint64 // highest revision dropped from the journal
}

type object struct {
	data    []byte
	etag    string
	modTime time.Time
	uid     string
}

type record struct {
	revision uint64
	name     string
	deleted  bool
}

// Option configures a Store.
type Option func(*Store)

// WithMaxJournal sets how many object changes each collection's journal keeps.
// Older revisions then report store.ErrChangesUnavailable. Values below 1 are
// ignored.
func WithMaxJournal(n int) Option {
	return func(s *Store) {
		if n >= 1 {
			s.maxJournal = n
		}
	}
}

// New returns an empty store.
func New(opts ...Option) *Store {
	s := &Store{maxJournal: DefaultMaxJournal, collections: map[key]*collection{}}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func refKey(ref store.CollectionRef) key {
	return key{owner: ref.Owner, kind: ref.Kind, name: ref.Name}
}

func (s *Store) lookup(ctx context.Context, ref store.CollectionRef) (*collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.ValidateRef(ref); err != nil {
		return nil, err
	}
	c, ok := s.collections[refKey(ref)]
	if !ok {
		return nil, fmt.Errorf("%w: collection %q", store.ErrNotFound, ref.Name)
	}
	return c, nil
}

func (c *collection) snapshot(ref store.CollectionRef) store.Collection {
	props := c.props
	props.Components = slices.Clone(props.Components)
	return store.Collection{Ref: ref, ID: c.id, Props: props, Revision: c.revision, Created: c.created}
}

func (o *object) snapshot(name string, withData bool) store.Object {
	out := store.Object{Name: name, ETag: o.etag, ModTime: o.modTime, UID: o.uid}
	if withData {
		out.Data = slices.Clone(o.data)
		if out.Data == nil {
			out.Data = []byte{}
		}
	}
	return out
}

// ListCollections implements store.Store.
func (s *Store) ListCollections(ctx context.Context, owner string, kind store.Kind) ([]store.Collection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := store.ValidateOwner(owner); err != nil {
		return nil, err
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: unknown kind %q", store.ErrInvalidName, kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []store.Collection{}
	for k, c := range s.collections {
		if k.owner == owner && k.kind == kind {
			out = append(out, c.snapshot(store.CollectionRef{Owner: owner, Kind: kind, Name: k.name}))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Name < out[j].Ref.Name })
	return out, nil
}

// GetCollection implements store.Store.
func (s *Store) GetCollection(ctx context.Context, ref store.CollectionRef) (store.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return store.Collection{}, err
	}
	return c.snapshot(ref), nil
}

// CreateCollection implements store.Store.
func (s *Store) CreateCollection(ctx context.Context, ref store.CollectionRef, props store.Props) (store.Collection, error) {
	if err := ctx.Err(); err != nil {
		return store.Collection{}, err
	}
	if err := store.ValidateRef(ref); err != nil {
		return store.Collection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.collections[refKey(ref)]; ok {
		return store.Collection{}, fmt.Errorf("%w: collection %q", store.ErrExists, ref.Name)
	}
	s.nextID++
	props.Components = slices.Clone(props.Components)
	c := &collection{
		id:      fmt.Sprintf("mem-%d", s.nextID),
		props:   props,
		created: time.Now().UTC(),
		objects: map[string]*object{},
		uids:    map[string]string{},
	}
	s.collections[refKey(ref)] = c
	return c.snapshot(ref), nil
}

// UpdateCollection implements store.Store.
func (s *Store) UpdateCollection(ctx context.Context, ref store.CollectionRef, u store.PropsUpdate) (store.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return store.Collection{}, err
	}
	next := c.props
	if u.DisplayName != nil {
		next.DisplayName = *u.DisplayName
	}
	if u.Description != nil {
		next.Description = *u.Description
	}
	if u.Color != nil {
		next.Color = *u.Color
	}
	if u.Order != nil {
		next.Order = *u.Order
	}
	if u.Timezone != nil {
		next.Timezone = *u.Timezone
	}
	if u.Components != nil {
		next.Components = slices.Clone(*u.Components)
	}
	if !samePropsValue(c.props, next) {
		c.props = next
		c.revision++
	}
	return c.snapshot(ref), nil
}

func samePropsValue(a, b store.Props) bool {
	return a.DisplayName == b.DisplayName && a.Description == b.Description && a.Color == b.Color &&
		a.Order == b.Order && a.Timezone == b.Timezone && slices.Equal(a.Components, b.Components)
}

// DeleteCollection implements store.Store.
func (s *Store) DeleteCollection(ctx context.Context, ref store.CollectionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lookup(ctx, ref); err != nil {
		return err
	}
	delete(s.collections, refKey(ref))
	return nil
}

// GetObject implements store.Store.
func (s *Store) GetObject(ctx context.Context, ref store.CollectionRef, name string) (store.Object, error) {
	if err := store.ValidateName(name); err != nil {
		return store.Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return store.Object{}, err
	}
	o, ok := c.objects[name]
	if !ok {
		return store.Object{}, fmt.Errorf("%w: object %q", store.ErrNotFound, name)
	}
	return o.snapshot(name, true), nil
}

// ListObjects implements store.Store.
func (s *Store) ListObjects(ctx context.Context, ref store.CollectionRef, opts store.ListOptions) ([]store.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]store.Object, 0, len(c.objects))
	for name, o := range c.objects {
		out = append(out, o.snapshot(name, opts.IncludeData))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PutObject implements store.Store.
func (s *Store) PutObject(ctx context.Context, ref store.CollectionRef, name string, data []byte, opts store.PutOptions) (store.Object, error) {
	if err := ctx.Err(); err != nil {
		return store.Object{}, err
	}
	if err := store.ValidateName(name); err != nil {
		return store.Object{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return store.Object{}, err
	}

	old, exists := c.objects[name]
	if opts.IfNoneMatch && exists {
		return store.Object{}, fmt.Errorf("%w: %q already exists", store.ErrPreconditionFailed, name)
	}
	if opts.IfMatch != "" && (!exists || old.etag != opts.IfMatch) {
		return store.Object{}, fmt.Errorf("%w: %q does not match", store.ErrPreconditionFailed, name)
	}

	etag := store.ETag(data)
	if exists && old.etag == etag && old.uid == opts.UID {
		return old.snapshot(name, true), nil
	}
	if opts.UID != "" {
		if other, taken := c.uids[opts.UID]; taken && other != name {
			return store.Object{}, &store.ConflictError{UID: opts.UID, Name: other}
		}
	}

	if exists && old.uid != "" {
		delete(c.uids, old.uid)
	}
	o := &object{data: slices.Clone(data), etag: etag, modTime: time.Now().UTC(), uid: opts.UID}
	c.objects[name] = o
	if opts.UID != "" {
		c.uids[opts.UID] = name
	}
	s.record(c, name, false)
	return o.snapshot(name, true), nil
}

// DeleteObject implements store.Store.
func (s *Store) DeleteObject(ctx context.Context, ref store.CollectionRef, name string, opts store.DeleteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return err
	}
	o, ok := c.objects[name]
	if !ok {
		return fmt.Errorf("%w: object %q", store.ErrNotFound, name)
	}
	if opts.IfMatch != "" && o.etag != opts.IfMatch {
		return fmt.Errorf("%w: %q does not match", store.ErrPreconditionFailed, name)
	}
	if o.uid != "" {
		delete(c.uids, o.uid)
	}
	delete(c.objects, name)
	s.record(c, name, true)
	return nil
}

// record advances the revision for an object write and appends it to the journal,
// dropping the oldest records beyond the limit.
func (s *Store) record(c *collection, name string, deleted bool) {
	c.revision++
	c.journal = append(c.journal, record{revision: c.revision, name: name, deleted: deleted})
	for len(c.journal) > s.maxJournal {
		c.floor = c.journal[0].revision
		c.journal = c.journal[1:]
	}
}

// Changes implements store.Store.
func (s *Store) Changes(ctx context.Context, ref store.CollectionRef, since uint64) (store.Changes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.lookup(ctx, ref)
	if err != nil {
		return store.Changes{}, err
	}
	out := store.Changes{Revision: c.revision}

	if since == 0 {
		for name := range c.objects {
			out.Changes = append(out.Changes, store.Change{Name: name})
		}
	} else {
		if since > c.revision || since < c.floor {
			return store.Changes{}, fmt.Errorf("%w: revision %d (collection is at %d)", store.ErrChangesUnavailable, since, c.revision)
		}
		final := map[string]bool{}
		for _, r := range c.journal {
			if r.revision > since {
				final[r.name] = r.deleted
			}
		}
		for name, deleted := range final {
			out.Changes = append(out.Changes, store.Change{Name: name, Deleted: deleted})
		}
	}
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].Name < out.Changes[j].Name })
	return out, nil
}
