// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind says what a collection holds.
type Kind string

const (
	// KindCalendar is a collection of iCalendar objects.
	KindCalendar Kind = "calendar"
	// KindAddressBook is a collection of vCard objects.
	KindAddressBook Kind = "addressbook"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { return k == KindCalendar || k == KindAddressBook }

// CollectionRef addresses a collection. A calendar and an address book of the
// same owner may share a name.
type CollectionRef struct {
	Owner string
	Kind  Kind
	Name  string
}

// Props are the properties of a collection that clients can set.
type Props struct {
	DisplayName string
	Description string
	// Color is a CSS-style colour such as "#ff0000ff", as Apple clients send it.
	Color string
	// Order is the position clients show the collection in.
	Order int
	// Timezone is the VTIMEZONE text of a calendar's default time zone.
	Timezone string
	// Components lists the component types a calendar accepts, such as
	// "VEVENT" and "VTODO". Empty means the engine's default.
	Components []string
}

// PropsUpdate changes some properties and leaves nil fields as they are.
type PropsUpdate struct {
	DisplayName *string
	Description *string
	Color       *string
	Order       *int
	Timezone    *string
	Components  *[]string
}

// Collection is a collection and its current state.
type Collection struct {
	Ref CollectionRef
	// ID is unique within the store and never reused, even if a collection of
	// the same name is deleted and created again.
	ID    string
	Props Props
	// Revision is the collection's current revision.
	Revision uint64
	Created  time.Time
}

// Object is a calendar or contact object.
type Object struct {
	// Name is the object's name in its collection, chosen by the client.
	Name string
	// Data is the object exactly as written. It is empty in listings made
	// without data.
	Data []byte
	// ETag identifies the content; see [ETag].
	ETag    string
	ModTime time.Time
	// UID is the UID the engine reported when the object was written.
	UID string
}

// PutOptions are the conditions and facts that go with a write.
type PutOptions struct {
	// IfMatch, when set, requires the object to exist with this ETag.
	IfMatch string
	// IfNoneMatch requires that no object with this name exists yet.
	IfNoneMatch bool
	// UID is the UID found in the data. When set, no other object in the
	// collection may have the same UID.
	UID string
}

// DeleteOptions are the conditions of a delete.
type DeleteOptions struct {
	// IfMatch, when set, requires the object to exist with this ETag.
	IfMatch string
}

// ListOptions controls ListObjects.
type ListOptions struct {
	// IncludeData fills in each object's Data. Without it listings are cheap.
	IncludeData bool
}

// Change is one object that differs from a given revision.
type Change struct {
	Name    string
	Deleted bool
}

// Changes is the result of asking what changed since a revision.
type Changes struct {
	// Revision is the collection's revision this answer is current to. Callers
	// hand it back on the next request.
	Revision uint64
	// Changes lists each affected object once, sorted by name.
	Changes []Change
}

// Store keeps calendar and contact data. Implementations are safe for
// concurrent use and return copies of data, never references into their own
// state. They return the errors in this package, wrapped if they like, so callers
// can use errors.Is.
type Store interface {
	// ListCollections returns the owner's collections of one kind, sorted by name.
	// An owner with none gets an empty slice, not an error.
	ListCollections(ctx context.Context, owner string, kind Kind) ([]Collection, error)

	// GetCollection returns one collection or ErrNotFound.
	GetCollection(ctx context.Context, ref CollectionRef) (Collection, error)

	// CreateCollection makes a collection or returns ErrExists.
	CreateCollection(ctx context.Context, ref CollectionRef, props Props) (Collection, error)

	// UpdateCollection changes properties and increases the revision.
	UpdateCollection(ctx context.Context, ref CollectionRef, update PropsUpdate) (Collection, error)

	// DeleteCollection removes a collection and all its objects.
	DeleteCollection(ctx context.Context, ref CollectionRef) error

	// GetObject returns one object with its data, or ErrNotFound.
	GetObject(ctx context.Context, ref CollectionRef, name string) (Object, error)

	// ListObjects returns the collection's objects sorted by name.
	ListObjects(ctx context.Context, ref CollectionRef, opts ListOptions) ([]Object, error)

	// PutObject creates or replaces an object. It returns the stored object.
	PutObject(ctx context.Context, ref CollectionRef, name string, data []byte, opts PutOptions) (Object, error)

	// DeleteObject removes an object or returns ErrNotFound.
	DeleteObject(ctx context.Context, ref CollectionRef, name string, opts DeleteOptions) error

	// Changes lists what changed after the given revision. See the package
	// documentation for the rules.
	Changes(ctx context.Context, ref CollectionRef, since uint64) (Changes, error)
}

// ETag returns the ETag for content: 32 hex characters, no quotes.
func ETag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

const (
	maxNameBytes  = 255
	maxOwnerBytes = 1024
)

// ValidateName checks an object or collection name. Names are chosen by clients
// and must be usable as one URL path segment: non-empty, valid UTF-8, at most 255
// bytes, no "/", no control characters, and not "." or "..". A store that needs a
// different form on disk encodes names itself.
func ValidateName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	case len(name) > maxNameBytes:
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidName, maxNameBytes)
	case !utf8.ValidString(name):
		return fmt.Errorf("%w: not valid UTF-8", ErrInvalidName)
	case strings.ContainsRune(name, '/'):
		return fmt.Errorf("%w: contains a slash", ErrInvalidName)
	case hasControl(name):
		return fmt.Errorf("%w: contains a control character", ErrInvalidName)
	}
	return nil
}

// ValidateOwner checks an owner identifier: non-empty, valid UTF-8, at most 1024
// bytes, no control characters. Owners are opaque, so slashes and leading dots
// are fine.
func ValidateOwner(owner string) error {
	switch {
	case owner == "":
		return fmt.Errorf("%w: empty owner", ErrInvalidName)
	case len(owner) > maxOwnerBytes:
		return fmt.Errorf("%w: owner longer than %d bytes", ErrInvalidName, maxOwnerBytes)
	case !utf8.ValidString(owner):
		return fmt.Errorf("%w: owner is not valid UTF-8", ErrInvalidName)
	case hasControl(owner):
		return fmt.Errorf("%w: owner contains a control character", ErrInvalidName)
	}
	return nil
}

// ValidateRef checks every part of a collection reference.
func ValidateRef(ref CollectionRef) error {
	if err := ValidateOwner(ref.Owner); err != nil {
		return err
	}
	if !ref.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidName, ref.Kind)
	}
	return ValidateName(ref.Name)
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
