// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package caldav

import (
	"context"
	"net/http"

	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// syncCollection answers a DAV:sync-collection report (RFC 6578). The
// DAV:limit element is ignored. The token and delta rules live in
// dav.AnswerSync, which both engines share.
func (h *Handler) syncCollection(w http.ResponseWriter, r *http.Request, t target, owner string, root *dav.Node) {
	req, err := dav.ParseSyncRequest(root, propRequestFromNode)
	if err != nil {
		dav.WriteErr(w, err)
		return
	}
	dav.AnswerSync(r.Context(), w, dav.SyncSource{
		Store:   h.store,
		Ref:     h.calendarRef(owner, t.calendar),
		HrefFor: func(name string) string { return h.objectHref(t.user, t.calendar, name) },
		ResponseFor: func(ctx context.Context, o store.Object, req dav.PropfindRequest) dav.Response {
			return h.objectResponse(ctx, owner, t.user, t.calendar, o, req)
		},
		Log: h.log,
	}, req)
}
