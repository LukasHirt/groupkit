// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"net/http"
	"strings"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// maxSyncChanges is the most changes one delta (a request with a token)
// answers. A larger delta is refused with valid-sync-token, which sends the
// client to an initial sync. An initial sync is never cut: every live object
// is streamed, because a client that is told it is done never asks again.
// It is a variable so tests can lower it.
var maxSyncChanges = 100_000

// syncCollection answers a DAV:sync-collection report (RFC 6578). The
// DAV:limit element is ignored.
func (h *Handler) syncCollection(w http.ResponseWriter, r *http.Request, t target, owner string, root *dav.Node) {
	if lvl := root.Child(dav.SyncLevel); lvl != nil && strings.TrimSpace(lvl.Text) != "1" {
		dav.WriteErr(w, dav.BadRequest("unsupported sync-level"))
		return
	}
	if root.Child(dav.PropEl) == nil {
		dav.WriteErr(w, dav.BadRequest("missing prop"))
		return
	}
	req, err := propRequestFromNode(root)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	var since uint64
	var tok string
	if n := root.Child(dav.SyncToken); n != nil {
		tok = strings.TrimSpace(n.Text)
	}

	ctx := r.Context()
	ref := h.bookRef(owner, t.book)
	col, err := h.store.GetCollection(ctx, ref)
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	if tok != "" {
		id, rev, perr := parseSyncToken(tok)
		if perr != nil || id != col.ID || rev > col.Revision {
			dav.WriteError(w, dav.Forbidden(dav.ValidSyncToken))
			return
		}
		since = rev
	}

	// The new token comes from the changes answer, not from col, so a write
	// between the two calls is either in the answer or in the next sync.
	ch, err := h.store.Changes(ctx, ref, since)
	if err != nil {
		h.fail(w, r, t, err)
		return
	}
	// The book may have been deleted and recreated since it was read. The
	// answer then belongs to another collection than the token was checked
	// against.
	if again, err := h.store.GetCollection(ctx, ref); err != nil {
		h.fail(w, r, t, err)
		return
	} else if again.ID != col.ID {
		dav.WriteError(w, dav.Forbidden(dav.ValidSyncToken))
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
		dav.WriteError(w, dav.Forbidden(dav.ValidSyncToken))
		return
	}

	var index map[string]store.Object
	for _, c := range changes {
		if !c.Deleted {
			objs, err := h.store.ListObjects(ctx, ref, store.ListOptions{})
			if err != nil {
				h.fail(w, r, t, err)
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
	responses := func(yield func(dav.Response) bool) {
		for _, c := range changes {
			if ctx.Err() != nil {
				return
			}
			href := h.objectHref(t.user, t.book, c.Name)
			o, ok := index[c.Name]
			var resp dav.Response
			if c.Deleted || !ok {
				resp = status(href, http.StatusNotFound)
			} else {
				resp = h.objectResponse(ctx, owner, t.user, t.book, o, req)
			}
			if !yield(resp) {
				return
			}
		}
	}
	h.writeSync(w, t, responses, syncToken(next))
}

func (h *Handler) writeSync(w http.ResponseWriter, t target, responses func(func(dav.Response) bool), token string) {
	if err := dav.WriteMultiStatus(w, responses, token); err != nil {
		h.log.Warn("sync response cut short", "collection", t.book, "error", err)
	}
}
