// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LukasHirt/groupkit/store"
	"github.com/LukasHirt/groupkit/store/memory"
)

func syncRoot(t *testing.T, inner string) *Node {
	t.Helper()
	root, err := ParseXML([]byte(`<D:sync-collection xmlns:D="DAV:">` + inner + `</D:sync-collection>`))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func propsOf(*Node) (PropfindRequest, error) {
	return PropfindRequest{Props: nil}, nil
}

func TestParseSyncRequest(t *testing.T) {
	tests := []struct {
		name, body string
		wantTok    string
		wantStatus int
	}{
		{"initial", `<D:sync-token/><D:sync-level>1</D:sync-level><D:prop><D:getetag/></D:prop>`, "", 0},
		{"no level", `<D:prop><D:getetag/></D:prop>`, "", 0},
		{"padded token", "<D:sync-token>\n  urn:x \t</D:sync-token><D:prop/>", "urn:x", 0},
		{"infinity", `<D:sync-level>infinity</D:sync-level><D:prop/>`, "", http.StatusBadRequest},
		{"level 2", `<D:sync-level>2</D:sync-level><D:prop/>`, "", http.StatusBadRequest},
		{"no prop", `<D:sync-level>1</D:sync-level>`, "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := ParseSyncRequest(syncRoot(t, tt.body), propsOf)
			if tt.wantStatus != 0 {
				var de *Error
				if !errors.As(err, &de) || de.Status != tt.wantStatus {
					t.Fatalf("err = %v, want %d", err, tt.wantStatus)
				}
				return
			}
			if err != nil || req.Token != tt.wantTok {
				t.Fatalf("token %q err %v, want %q", req.Token, err, tt.wantTok)
			}
		})
	}
}

func TestParseSyncRequestPropsError(t *testing.T) {
	want := BadRequest("bad prop")
	_, err := ParseSyncRequest(syncRoot(t, `<D:prop/>`), func(*Node) (PropfindRequest, error) { return PropfindRequest{}, want })
	if err != error(want) {
		t.Errorf("err = %v", err)
	}
}

func TestSetSyncCapForTestingRestores(t *testing.T) {
	old := maxSyncChanges
	restore := SetSyncCapForTesting(2)
	if maxSyncChanges != 2 {
		t.Fatalf("cap = %d", maxSyncChanges)
	}
	restore()
	if maxSyncChanges != old {
		t.Errorf("cap = %d, want %d", maxSyncChanges, old)
	}
}

func TestAnswerSyncMissingCollection(t *testing.T) {
	// The caller checks the collection exists; if it does not, the answer is
	// the store's 404.
	w := httptest.NewRecorder()
	AnswerSync(t.Context(), w, testSource(emptyStore(t), syncRef), SyncRequest{})
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "multistatus") {
		t.Errorf("status %d body %q", w.Code, w.Body)
	}
}

// fakeChanges wraps a store and replaces what Changes reports, to reach the
// paths that a correct store never triggers.
type fakeChanges struct {
	store.Store
	changes func(since uint64) store.Changes
}

func (f fakeChanges) Changes(_ context.Context, _ store.CollectionRef, since uint64) (store.Changes, error) {
	return f.changes(since), nil
}

func syncFixture(t *testing.T, names ...string) (store.Store, store.CollectionRef, store.Collection) {
	t.Helper()
	st := memory.New()
	ref := store.CollectionRef{Owner: "u", Kind: store.KindCalendar, Name: "c"}
	_, err := st.CreateCollection(t.Context(), ref, store.Props{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := st.PutObject(t.Context(), ref, n, []byte("x"), store.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	col, err := st.GetCollection(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return st, ref, col
}

func testSource(st store.Store, ref store.CollectionRef) SyncSource {
	return SyncSource{
		Store:   st,
		Ref:     ref,
		HrefFor: func(name string) string { return "/c/" + name },
		ResponseFor: func(_ context.Context, o store.Object, _ PropfindRequest) Response {
			return Response{Href: "/c/" + o.Name, Status: http.StatusOK}
		},
	}
}

func TestAnswerSyncInitialSkipsTombstones(t *testing.T) {
	st, ref, col := syncFixture(t, "a")
	fake := fakeChanges{Store: st, changes: func(uint64) store.Changes {
		return store.Changes{Revision: col.Revision, Changes: []store.Change{{Name: "a"}, {Name: "gone", Deleted: true}}}
	}}
	w := httptest.NewRecorder()
	AnswerSync(t.Context(), w, testSource(fake, ref), SyncRequest{})
	body := w.Body.String()
	if w.Code != http.StatusMultiStatus || !strings.Contains(body, "/c/a") {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	if strings.Contains(body, "gone") {
		t.Errorf("initial sync lists a tombstone: %s", body)
	}
}

func TestAnswerSyncFutureTokenRefusedWhateverTheStoreSays(t *testing.T) {
	st, ref, col := syncFixture(t, "a")
	fake := fakeChanges{Store: st, changes: func(uint64) store.Changes {
		return store.Changes{Revision: col.Revision, Changes: []store.Change{{Name: "a"}}}
	}}
	future := col
	future.Revision += 5
	w := httptest.NewRecorder()
	AnswerSync(t.Context(), w, testSource(fake, ref), SyncRequest{Token: FormatSyncToken(future)})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "valid-sync-token") {
		t.Errorf("status %d body %s", w.Code, w.Body)
	}
}

func TestAnswerSyncCancelledStreamHasNoToken(t *testing.T) {
	st, ref, _ := syncFixture(t, "a", "b", "c")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := testSource(st, ref)
	src.ResponseFor = func(_ context.Context, o store.Object, _ PropfindRequest) Response {
		cancel() // the deadline strikes while the answer streams
		return Response{Href: "/c/" + o.Name, Status: http.StatusOK}
	}
	w := httptest.NewRecorder()
	AnswerSync(ctx, w, src, SyncRequest{})
	body := w.Body.String()
	if strings.Contains(body, "sync-token") {
		t.Errorf("a cut stream carries a token: %s", body)
	}
	if strings.Contains(body, "/c/b") || strings.Contains(body, "/c/c") {
		t.Errorf("the stream went on after the cancel: %s", body)
	}
}

func TestAnswerSyncCancelledBeforeStreamingHasNoToken(t *testing.T) {
	st, ref, _ := syncFixture(t, "a")
	ctx, cancel := context.WithCancel(t.Context())
	src := testSource(st, ref)
	src.Store = cancelAfterChanges{Store: st, cancel: cancel}
	w := httptest.NewRecorder()
	AnswerSync(ctx, w, src, SyncRequest{})
	if strings.Contains(w.Body.String(), "sync-token") {
		t.Errorf("token on a cancelled answer: %s", w.Body)
	}
}

type cancelAfterChanges struct {
	store.Store
	cancel func()
}

func (c cancelAfterChanges) Changes(ctx context.Context, ref store.CollectionRef, since uint64) (store.Changes, error) {
	ch, err := c.Store.Changes(ctx, ref, since)
	c.cancel()
	return ch, err
}

func TestAnswerSyncIncompleteSourceIs500(t *testing.T) {
	st, ref, _ := syncFixture(t, "a")
	var logs strings.Builder
	good := testSource(st, ref)
	good.Log = slog.New(slog.NewTextHandler(&logs, nil))
	for name, mutate := range map[string]func(*SyncSource){
		"no HrefFor":     func(s *SyncSource) { s.HrefFor = nil },
		"no ResponseFor": func(s *SyncSource) { s.ResponseFor = nil },
	} {
		t.Run(name, func(t *testing.T) {
			logs.Reset()
			src := good
			mutate(&src)
			w := httptest.NewRecorder()
			AnswerSync(t.Context(), w, src, SyncRequest{})
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status %d, want 500", w.Code)
			}
			if logs.Len() == 0 {
				t.Error("nothing logged")
			}
		})
	}
}
