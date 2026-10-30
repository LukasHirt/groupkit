// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound is returned when a collection or object does not exist.
	ErrNotFound = errors.New("store: not found")

	// ErrExists is returned when creating a collection that already exists.
	ErrExists = errors.New("store: already exists")

	// ErrPreconditionFailed is returned when an If-Match or If-None-Match
	// condition does not hold. The write did not happen.
	ErrPreconditionFailed = errors.New("store: precondition failed")

	// ErrUIDConflict is returned when another object in the collection already
	// has the UID of the object being written. Use errors.As with
	// *ConflictError to find which one.
	ErrUIDConflict = errors.New("store: uid conflict")

	// ErrChangesUnavailable is returned by Changes when the store can't list
	// what changed since the given revision: it is older than the journal
	// retains, or newer than the collection's current revision.
	ErrChangesUnavailable = errors.New("store: changes unavailable")

	// ErrInvalidName is returned for an owner or object or collection name the
	// store does not accept.
	ErrInvalidName = errors.New("store: invalid name")
)

// ConflictError reports which object holds the UID that caused ErrUIDConflict.
type ConflictError struct {
	// UID is the UID that is already in use.
	UID string
	// Name is the object that holds the UID.
	Name string
}

// Error implements error.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("store: uid %q is already used by %q", e.UID, e.Name)
}

// Unwrap lets errors.Is(err, ErrUIDConflict) match.
func (e *ConflictError) Unwrap() error { return ErrUIDConflict }
