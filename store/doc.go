// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package store defines how calendar and contact data is stored: collections of
// objects kept as the bytes a client sent, plus a change journal that lets the
// protocol engines answer "what changed since this token".
//
// # Model
//
// An owner (an opaque identifier such as an OIDC subject) has collections. A
// collection is a calendar or an address book and is addressed by a
// [CollectionRef]. A collection holds objects, which are named by the client
// (for example "event-1.ics") and carry their data as opaque bytes. Stores never
// parse the data; the protocol engines validate it and tell the store the one
// fact it needs, the object's UID.
//
// # Concurrency and conditions
//
// Writes to one collection are serialized. PutObject and DeleteObject check
// their preconditions under that serialization, so a conditional write is atomic.
// Conditions that fail return [ErrPreconditionFailed]; a store never overwrites
// data on a failed condition.
//
// # ETags
//
// An object's ETag is derived from its content with [ETag], so it is the same in
// every store and survives a restart. ETags are returned without quotes.
//
// # Revisions and changes
//
// Every collection has a revision, a number that only grows. Each successful
// write to an object (create, change or delete) increases it by one and appends a
// record to the collection's journal. Changing the collection's properties also
// increases the revision, without a journal record, so a ctag built from the
// revision changes whenever anything about the collection changes. Writing
// identical content to an object that already holds it changes nothing.
//
// [Store.Changes] reports what changed after a given revision, collapsed per
// object: the final state wins, and an object created and deleted after the
// revision is reported as deleted. Revision 0 means "everything": it lists every
// current object. A store keeps a limited journal; a revision older than what it
// retains, or newer than the collection's current revision, returns
// [ErrChangesUnavailable], and the caller asks the client to resync.
//
// Each collection also has an ID that is unique within the store and never reused,
// so a sync token built from the ID and a revision cannot be confused with one from
// a deleted and recreated collection of the same name.
//
// # Testing a store
//
// The storetest package holds a conformance suite. Every implementation passes it.
package store
