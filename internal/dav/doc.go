// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package dav is the protocol-neutral HTTP and XML core shared by the CardDAV
// and CalDAV engines.
//
// # Pieces
//
//   - Node and ParseXML read a request body into a bounded tree: size, depth,
//     element and attribute counts are capped, DOCTYPE declarations are
//     refused and namespaces are resolved strictly.
//   - Writer, WriteMultiStatus and WriteError produce responses. Writer
//     prefixes names, escapes text and records the first error.
//   - ParsePropfind, ParsePropPatch, ParseMkcol and ParseMkcalendar turn
//     request bodies into plain values. Every error they return is a 400
//     *Error.
//   - PropFind evaluates a PROPFIND request against a Resource and returns a
//     Response for WriteMultiStatus.
//   - ParseSyncRequest and AnswerSync implement DAV:sync-collection for both
//     engines: token checks, the delta cap, the response stream and the new
//     token. FormatSyncToken and ParseSyncToken make and read the tokens.
//   - The request helpers are ReadBody, ParseDepth, ParseCond, CheckPreconditions,
//     CleanSegment, EscapeHref, HTTPDate and StoreError.
//
// # Conventions
//
// Names are xml.Name variables (see names.go). A variable is the element's
// Go-style local name. An El suffix marks DAV: names whose plain identifier
// is a Go type here; a Cal or CR prefix marks CalDAV and CardDAV query and
// filter elements and local names that exist in more than one namespace.
//
// Hrefs are always passed already percent-encoded; the Writer only escapes
// them for XML. Build them with EscapeHref and read path segments with
// CleanSegment.
//
// Property values come in two stages. PropDef.Get runs eagerly, before
// anything is sent, and may do I/O; its error becomes a 500 propstat. The
// Prop it returns carries a lazy, pure Write that runs while the 207 is being
// streamed, after the status line is committed.
//
// For that reason the Writer's errors are sticky: after the 207 is committed
// there is no way to report a failure to the client, so the first error is
// kept, further writes are dropped, the document is closed and
// WriteMultiStatus returns the error to the caller to log. A malformed
// Response is skipped without writing any of it and reported at the end.
//
// Functions that return the error interface (ParseXML, the Parse* functions,
// ReadBody, CleanSegment, ParseDepth) hold a *Error in it; use errors.As to
// get it. StoreError returns *Error directly, so "return StoreError(err)" from
// a function returning error is a non-nil interface when err is nil. Prefer
// WriteErr, which takes any error, for sending failures.
//
// # Writing to a resource
//
// For PUT and DELETE, an engine reads the stored etag, calls
// CheckPreconditions with the parsed If-Match and If-None-Match headers, and
// answers a non-nil result with WriteErr. It then passes the etag it saw to
// the store (PutOptions.IfMatch, or IfNoneMatch for a new resource) so the
// store repeats the check atomically, and maps a store failure with
// StoreError.
package dav
