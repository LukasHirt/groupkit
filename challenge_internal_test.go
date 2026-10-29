// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChallengeWriterPassesFlushAndUnwrap(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &challengeWriter{ResponseWriter: rec, challenge: "Basic"}

	var rw http.ResponseWriter = w
	f, ok := rw.(http.Flusher)
	if !ok {
		t.Fatal("challengeWriter is not an http.Flusher")
	}
	_, _ = w.Write([]byte("x"))
	f.Flush()
	if !rec.Flushed {
		t.Error("Flush did not reach the underlying writer")
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("challenge set on a 200")
	}
	if got := w.Unwrap(); got != http.ResponseWriter(rec) {
		t.Error("Unwrap does not return the wrapped writer")
	}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Errorf("ResponseController.Flush: %v", err)
	}
}

func TestChallengeWriterOnly401(t *testing.T) {
	for code, want := range map[int]string{200: "", 207: "", 401: "Basic", 403: ""} {
		rec := httptest.NewRecorder()
		w := &challengeWriter{ResponseWriter: rec, challenge: "Basic"}
		w.WriteHeader(code)
		if got := rec.Header().Get("WWW-Authenticate"); got != want {
			t.Errorf("%d: header = %q, want %q", code, got, want)
		}
	}
	// A first Write is an implicit 200 and never adds the header later.
	rec := httptest.NewRecorder()
	w := &challengeWriter{ResponseWriter: rec, challenge: "Basic"}
	_, _ = w.Write([]byte("x"))
	w.WriteHeader(401)
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Error("header added after the body started")
	}
}
