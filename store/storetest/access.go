// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package storetest

import (
	"context"
	"fmt"

	"github.com/LukasHirt/groupkit/store"
)

// WithAccess returns a store over inner that reports level for every
// collection and refuses what the level does not allow with
// [store.ErrPermission]: object writes below [store.AccessObjects], and
// creating, changing or deleting a collection below [store.AccessFull]. The
// check comes before anything else, so a refused call reaches neither the
// inner store nor its validation. Reads and [store.Store.Changes] always pass
// through.
//
// It exists to test the engines and programs that act on access levels. A
// store that really serves collections to callers other than their owners
// decides the level per collection and caller, which this wrapper cannot.
func WithAccess(inner store.Store, level store.Access) store.Store {
	return &accessStore{Store: inner, level: level}
}

type accessStore struct {
	store.Store
	level store.Access
}

func (s *accessStore) refuse(op string) error {
	return fmt.Errorf("%w: %s needs more than %s access", store.ErrPermission, op, s.level)
}

func (s *accessStore) report(c store.Collection) store.Collection {
	c.Access = s.level
	return c
}

func (s *accessStore) ListCollections(ctx context.Context, owner string, kind store.Kind) ([]store.Collection, error) {
	cols, err := s.Store.ListCollections(ctx, owner, kind)
	for i := range cols {
		cols[i] = s.report(cols[i])
	}
	return cols, err
}

func (s *accessStore) GetCollection(ctx context.Context, ref store.CollectionRef) (store.Collection, error) {
	c, err := s.Store.GetCollection(ctx, ref)
	return s.report(c), err
}

func (s *accessStore) CreateCollection(ctx context.Context, ref store.CollectionRef, props store.Props) (store.Collection, error) {
	if !s.level.AllowsPropertyWrites() {
		return store.Collection{}, s.refuse("CreateCollection")
	}
	c, err := s.Store.CreateCollection(ctx, ref, props)
	return s.report(c), err
}

func (s *accessStore) UpdateCollection(ctx context.Context, ref store.CollectionRef, update store.PropsUpdate) (store.Collection, error) {
	if !s.level.AllowsPropertyWrites() {
		return store.Collection{}, s.refuse("UpdateCollection")
	}
	c, err := s.Store.UpdateCollection(ctx, ref, update)
	return s.report(c), err
}

func (s *accessStore) DeleteCollection(ctx context.Context, ref store.CollectionRef) error {
	if !s.level.AllowsPropertyWrites() {
		return s.refuse("DeleteCollection")
	}
	return s.Store.DeleteCollection(ctx, ref)
}

func (s *accessStore) PutObject(ctx context.Context, ref store.CollectionRef, name string, data []byte, opts store.PutOptions) (store.Object, error) {
	if !s.level.AllowsObjectWrites() {
		return store.Object{}, s.refuse("PutObject")
	}
	return s.Store.PutObject(ctx, ref, name, data, opts)
}

func (s *accessStore) DeleteObject(ctx context.Context, ref store.CollectionRef, name string, opts store.DeleteOptions) error {
	if !s.level.AllowsObjectWrites() {
		return s.refuse("DeleteObject")
	}
	return s.Store.DeleteObject(ctx, ref, name, opts)
}
