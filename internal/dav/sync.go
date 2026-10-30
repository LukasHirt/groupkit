// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LukasHirt/groupkit/store"
)

// maxSyncChanges is the most changes one delta (a request with a token)
// answers. A larger delta is refused with valid-sync-token, which sends the
// client to an initial sync. An initial sync is never cut: every live object
// is streamed, because a client that is told it is done never asks again.
var maxSyncChanges = 100_000

// SetSyncCapForTesting lowers the delta cap to n and returns a function that
// restores the previous value. It exists for tests of the engines and must
// not be called from production code or concurrently with a sync.
func SetSyncCapForTesting(n int) (restore func()) {
	old := maxSyncChanges
	maxSyncChanges = n
	return func() { maxSyncChanges = old }
}

// SyncRequest is a parsed DAV:sync-collection body (RFC 6578). DAV:limit is
// ignored.
type SyncRequest struct {
	// Token is the client's sync token with surrounding space removed. Empty
	// asks for an initial sync.
	Token string
	// Props selects the properties of each changed object.
	Props PropfindRequest
}

// ParseSyncRequest reads a sync-collection body. Only sync-level 1 (or none)
// is supported, and DAV:prop is required. propsFrom reads the property
// selection from the root, as the engine does for its other reports. Every
// error is a 400 *Error, or whatever propsFrom returns.
func ParseSyncRequest(root *Node, propsFrom func(*Node) (PropfindRequest, error)) (SyncRequest, error) {
	if lvl := root.Child(SyncLevel); lvl != nil && strings.TrimSpace(lvl.Text) != "1" {
		return SyncRequest{}, BadRequest("unsupported sync-level")
	}
	if root.Child(PropEl) == nil {
		return SyncRequest{}, BadRequest("missing prop")
	}
	props, err := propsFrom(root)
	if err != nil {
		return SyncRequest{}, err
	}
	req := SyncRequest{Props: props}
	if n := root.Child(SyncToken); n != nil {
		req.Token = strings.TrimSpace(n.Text)
	}
	return req, nil
}

// SyncSource tells AnswerSync where the collection is and how an engine
// renders its objects.
type SyncSource struct {
	Store store.Store
	Ref   store.CollectionRef
	// HrefFor returns the escaped URL of the object called name. It is
	// required.
	HrefFor func(name string) string
	// ResponseFor is required. It builds the response for a live changed
	// object. o carries no data; the engine reads it only when the selected properties need it.
	ResponseFor func(ctx context.Context, o store.Object, req PropfindRequest) Response
	// Log receives server faults and cut-short streams. Nil discards them.
	Log *slog.Logger
}

// AnswerSync answers a DAV:sync-collection report for the collection
// src.Ref names, which must exist. It writes the whole response: a 207 with
// one response per changed object and the new token, or an error. The
// failures it answers are logged to src.Log; there is nothing left for the
// caller to do.
//
// If ctx ends while the answer streams, the streaming stops and the response
// is closed without a DAV:sync-token. The status line has been sent by then,
// so the client sees a well-formed 207 that lacks the token every complete
// answer carries; it must not treat the list as complete and syncs again.
// SyncSource.HrefFor and SyncSource.ResponseFor must be set; AnswerSync
// answers 500 and logs the fault when either is missing.
//
// An empty token is an initial sync, which lists every live object and is
// never truncated. A token must name this collection and not lie in its
// future. A delta of more than the cap is refused, like any token the store
// cannot serve, with 403 valid-sync-token, which sends the client to an
// initial sync.
func AnswerSync(ctx context.Context, w http.ResponseWriter, src SyncSource, req SyncRequest) {
	log := src.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if src.HrefFor == nil || src.ResponseFor == nil {
		log.Error("sync source incomplete: HrefFor and ResponseFor are required", "collection", src.Ref.Name)
		WriteError(w, &Error{Status: http.StatusInternalServerError, Message: "internal error"})
		return
	}
	fail := func(err error) {
		if StoreError(err).Status >= http.StatusInternalServerError {
			log.Error("store failure", "collection", src.Ref.Name, "error", err)
		}
		WriteErr(w, err)
	}

	col, err := src.Store.GetCollection(ctx, src.Ref)
	if err != nil {
		fail(err)
		return
	}
	var since uint64
	if req.Token != "" {
		id, rev, perr := ParseSyncToken(req.Token)
		if perr != nil || id != col.ID || rev > col.Revision {
			WriteError(w, Forbidden(ValidSyncToken))
			return
		}
		since = rev
	}

	// The new token comes from the changes answer, not from col, so a write
	// between the two calls is either in the answer or in the next sync.
	ch, err := src.Store.Changes(ctx, src.Ref, since)
	if err != nil {
		fail(err)
		return
	}
	// The collection may have been deleted and recreated since it was read.
	// The answer then belongs to another collection than the token was
	// checked against.
	if again, err := src.Store.GetCollection(ctx, src.Ref); err != nil {
		fail(err)
		return
	} else if again.ID != col.ID {
		WriteError(w, Forbidden(ValidSyncToken))
		return
	}
	changes := ch.Changes
	// The stores report no tombstones for revision 0; dropping them here is
	// defence in depth, as is the future-revision check above.
	if since == 0 {
		live := make([]store.Change, 0, len(changes))
		for _, c := range changes {
			if !c.Deleted {
				live = append(live, c)
			}
		}
		changes = live
	}
	if since != 0 && len(changes) > maxSyncChanges {
		WriteError(w, Forbidden(ValidSyncToken))
		return
	}

	var index map[string]store.Object
	for _, c := range changes {
		if !c.Deleted {
			objs, err := src.Store.ListObjects(ctx, src.Ref, store.ListOptions{})
			if err != nil {
				fail(err)
				return
			}
			index = make(map[string]store.Object, len(objs))
			for _, o := range objs {
				index[o.Name] = o
			}
			break
		}
	}

	next := col
	next.Revision = ch.Revision
	responses := func(yield func(Response) bool) {
		for _, c := range changes {
			if ctx.Err() != nil {
				return
			}
			o, ok := index[c.Name]
			var resp Response
			if c.Deleted || !ok {
				resp = Response{Href: src.HrefFor(c.Name), Status: http.StatusNotFound}
			} else {
				resp = src.ResponseFor(ctx, o, req.Props)
			}
			if !yield(resp) {
				return
			}
		}
	}
	// The token vouches for a complete list, so a stream that the context cut
	// short does not get one.
	token := func() string {
		if ctx.Err() != nil {
			return ""
		}
		return FormatSyncToken(next)
	}
	if err := writeMultiStatus(w, responses, token); err != nil {
		log.Warn("sync response cut short", "collection", src.Ref.Name, "error", err)
	}
}
