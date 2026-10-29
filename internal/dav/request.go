// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit/store"
)

// maxSegmentBytes is the longest decoded path segment CleanSegment accepts.
const maxSegmentBytes = 255

// ReadBody reads the request body, at most limit bytes. A limit of zero or less
// means MaxXMLBytes. A body over the limit gives a 413 *Error, any other read
// failure a 400 *Error, and a request without a body an empty slice. The 413
// has a plain-text message and no precondition: a PUT engine should replace it
// with TooLarge(CalMaxResourceSize) or TooLarge(CRMaxResourceSize), which is
// what CalDAV and CardDAV clients expect.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = MaxXMLBytes
	}
	if r.Body == nil {
		return []byte{}, nil
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, &Error{Status: http.StatusRequestEntityTooLarge, Message: "request body too large"}
		}
		return nil, BadRequest("cannot read request body")
	}
	return b, nil
}

// Depth is the value of a Depth header, reduced to what the engines serve.
type Depth int

// Depth values. "infinity" is served as Depth1.
const (
	Depth0 Depth = 0
	Depth1 Depth = 1
)

// ParseDepth parses a Depth header. An empty header gives def. "0" and "1"
// are taken as given and "infinity" (any case) is treated as 1. Anything
// else is a 400 *Error.
func ParseDepth(h string, def Depth) (Depth, error) {
	switch strings.ToLower(strings.TrimSpace(h)) {
	case "":
		return def, nil
	case "0":
		return Depth0, nil
	case "1", "infinity":
		return Depth1, nil
	}
	return def, BadRequest("invalid Depth header")
}

// QuoteETag returns etag as a quoted entity tag. Quote characters and
// control characters are dropped, since a stored value never needs them.
func QuoteETag(etag string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range etag {
		if r == '"' || r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// Cond is a parsed If-Match or If-None-Match header.
type Cond struct {
	// Star is set for "*".
	Star bool
	// Present is set when the header had any content besides whitespace,
	// whether or not it could be parsed.
	Present bool
	// ETags are the listed entity tags, unquoted and without any W/ prefix.
	ETags []string

	weak []bool // weak[i] reports whether ETags[i] came with W/
}

// ParseCond parses an If-Match or If-None-Match value: "*", or one or more
// comma-separated entity tags, each quoted and optionally prefixed with W/.
// A comma inside quotes belongs to the tag. A single unquoted value is
// accepted, since some clients send one. An empty header gives a zero Cond
// and unparseable tokens are ignored.
//
// A header that is present but yields nothing (see Malformed) must not be
// treated as absent: a caller guarding on IsSet alone would skip the
// precondition and overwrite unconditionally.
func ParseCond(h string) Cond {
	var c Cond
	c.Present = strings.TrimSpace(h) != ""
	var bare []string
	bareWeak := false
	for i := 0; i < len(h); {
		switch h[i] {
		case ' ', '\t', ',':
			i++
			continue
		}
		weak := false
		if strings.HasPrefix(h[i:], "W/") {
			weak = true
			i += 2
			if i >= len(h) || h[i] == ' ' || h[i] == '\t' || h[i] == ',' {
				break // W/ must be followed directly by the tag
			}
		}
		if i < len(h) && h[i] == '"' {
			end := strings.IndexByte(h[i+1:], '"')
			if end < 0 {
				break // unterminated, nothing more can be trusted
			}
			c.ETags = append(c.ETags, h[i+1:i+1+end])
			c.weak = append(c.weak, weak)
			i += end + 2
			continue
		}
		start := i
		for i < len(h) && h[i] != ',' && h[i] != ' ' && h[i] != '\t' {
			i++
		}
		tok := h[start:i]
		switch {
		case tok == "*" && !weak:
			c.Star = true
		case tok != "":
			if len(bare) < 2 { // a second token already rules out a lone bare tag
				bare = append(bare, tok)
			}
			bareWeak = weak
		}
	}
	if !c.Star && len(c.ETags) == 0 && len(bare) == 1 {
		c.ETags = bare
		c.weak = []bool{bareWeak}
	}
	return c
}

// IsSet reports whether the header named "*" or at least one entity tag.
func (c Cond) IsSet() bool { return c.Star || len(c.ETags) > 0 }

// Malformed reports a header that was sent but contained no usable "*" or
// entity tag. Callers MUST answer a malformed If-Match with 412, since a
// precondition that cannot be evaluated fails, and treat a malformed
// If-None-Match as an unmet precondition too (412 for methods other than
// GET and HEAD).
func (c Cond) Malformed() bool { return c.Present && !c.IsSet() }

// Matches reports whether etag, the stored value without quotes, satisfies
// the condition under strong comparison, as If-Match requires: weak tags
// never match. "*" matches any non-empty etag; whether the resource exists
// is for the caller to check.
func (c Cond) Matches(etag string) bool {
	if c.Star && etag != "" {
		return true
	}
	for i, t := range c.ETags {
		if t == etag && !c.weak[i] {
			return true
		}
	}
	return false
}

// MatchesWeak is like Matches under weak comparison, as If-None-Match
// requires: W/"x" matches "x".
func (c Cond) MatchesWeak(etag string) bool {
	if c.Star && etag != "" {
		return true
	}
	for _, t := range c.ETags {
		if t == etag {
			return true
		}
	}
	return false
}

// HTTPDate formats t as an HTTP date in GMT.
func HTTPDate(t time.Time) string { return t.UTC().Format(http.TimeFormat) }

// EscapeHref percent-encodes each segment and joins them with "/". A slash
// inside a segment is encoded as %2F, so it cannot add a level. Invalid
// UTF-8 is replaced with U+FFFD first. No trailing slash is added; pass a
// final empty segment to get one. An empty first segment yields a leading
// slash: EscapeHref("", "dav", "u s") is "/dav/u%20s".
func EscapeHref(segments ...string) string {
	out := make([]string, len(segments))
	for i, s := range segments {
		out[i] = url.PathEscape(strings.ToValidUTF8(s, "�"))
	}
	return strings.Join(out, "/")
}

// CleanSegment percent-decodes one path segment, once, and rejects values
// that are unsafe as a name: empty, "." or "..", containing "/", NUL or any
// control character, invalid UTF-8, or longer than 255 bytes. The error is
// a 400 *Error.
func CleanSegment(raw string) (string, error) {
	s, err := url.PathUnescape(raw)
	if err != nil {
		return "", BadRequest("invalid path segment")
	}
	if s == "" || s == "." || s == ".." || len(s) > maxSegmentBytes || !utf8.ValidString(s) {
		return "", BadRequest("invalid path segment")
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '/' || c < 0x20 || c == 0x7f {
			return "", BadRequest("invalid path segment")
		}
	}
	return s, nil
}

// CheckPreconditions evaluates If-Match and If-None-Match for a PUT or DELETE
// as RFC 9110 sections 13.1.1 and 13.1.2 describe. current is the stored
// resource's etag without quotes and exists reports whether the resource is
// there. It returns a 412 *Error when a header is malformed, when If-Match is
// set and the resource is absent or its etag does not match under strong
// comparison (weak tags never match), or when If-None-Match is set and
// names "*" for an existing resource or matches current under weak
// comparison. It returns nil when every header that is set passes.
//
// The check is advisory: a concurrent writer can still change the resource
// after it. Engines then pass current as store.PutOptions.IfMatch (or
// IfNoneMatch set to true when the resource does not exist) so the store
// repeats the check atomically, and map its ErrPreconditionFailed with
// StoreError.
func CheckPreconditions(ifMatch, ifNoneMatch Cond, current string, exists bool) *Error {
	fail := PreconditionFailed("precondition failed")
	if ifMatch.Malformed() || ifNoneMatch.Malformed() {
		return fail
	}
	if ifMatch.IsSet() {
		switch {
		case !exists:
			return fail
		case ifMatch.Star:
		case !ifMatch.Matches(current):
			return fail
		}
	}
	if ifNoneMatch.IsSet() && exists {
		if ifNoneMatch.Star || ifNoneMatch.MatchesWeak(current) {
			return fail
		}
	}
	return nil
}

// WriteErr sends err as an error response. An *Error anywhere in the chain
// (found with errors.As) is sent as it is; any other error, including nil,
// goes through StoreError, so the client never sees internal detail. Prefer
// it over calling WriteError with a StoreError result.
func WriteErr(w http.ResponseWriter, err error) {
	var de *Error
	if errors.As(err, &de) {
		WriteError(w, de)
		return
	}
	WriteError(w, StoreError(err))
}

// StoreError maps an error from the store to an *Error. A nil err gives nil.
// Unrecognized errors become a 500 with no detail. A uid conflict gives a 409
// with no Precondition: the caller sets CalNoUIDConflict or CRNoUIDConflict
// and the Hrefs of the object holding the UID, which a *store.ConflictError
// names:
//
//	e := dav.StoreError(err)
//	if e.Status == http.StatusConflict {
//		e.Precondition = dav.CalNoUIDConflict
//		e.Hrefs = []string{dav.EscapeHref("", "dav", "cal", name)}
//	}
//
// Beware the typed-nil trap: in a function returning error, "return
// dav.StoreError(err)" returns a non-nil error interface holding a nil
// *Error when err is nil. Test the result against nil first, or use WriteErr,
// which takes an error and handles it.
func StoreError(err error) *Error {
	var ce *store.ConflictError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return NotFound("not found")
	case errors.Is(err, store.ErrPreconditionFailed):
		return PreconditionFailed("precondition failed")
	case errors.Is(err, store.ErrExists):
		return &Error{Status: http.StatusMethodNotAllowed, Message: "already exists"}
	case errors.As(err, &ce), errors.Is(err, store.ErrUIDConflict):
		return &Error{Status: http.StatusConflict, Message: "uid conflict"}
	case errors.Is(err, store.ErrInvalidName):
		return BadRequest("invalid name")
	case errors.Is(err, store.ErrChangesUnavailable):
		return Forbidden(ValidSyncToken)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &Error{Status: http.StatusServiceUnavailable, Message: "request cancelled"}
	}
	return &Error{Status: http.StatusInternalServerError, Message: "internal error"}
}
