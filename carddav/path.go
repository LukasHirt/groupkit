// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package carddav

import (
	"strings"

	"github.com/LukasHirt/groupkit/internal/dav"
)

type targetKind int

const (
	kindHome       targetKind = iota // {prefix}/addressbooks/{user}/
	kindCollection                   // {prefix}/addressbooks/{user}/{book}/
	kindObject                       // {prefix}/addressbooks/{user}/{book}/{name}
)

// target is a parsed request path. The fields are percent-decoded.
type target struct {
	kind       targetKind
	user, book string
	name       string
}

// parsePath parses an escaped request path. It answers 404 for a path outside
// {prefix}/addressbooks/, one deeper than an object, or an object with a
// trailing slash, and 400 for an empty or unsafe segment. A collection URL
// without the trailing slash is accepted, as some clients omit it.
func (h *Handler) parsePath(p string) (target, *dav.Error) {
	var t target
	root := h.prefix + "/addressbooks/"
	rest, ok := strings.CutPrefix(p, root)
	if !ok || rest == "" {
		return t, dav.NotFound("not found")
	}
	trailing := strings.HasSuffix(rest, "/")
	rest = strings.TrimSuffix(rest, "/")
	raw := strings.Split(rest, "/")
	if len(raw) > 3 {
		return t, dav.NotFound("not found")
	}
	segs := make([]string, len(raw))
	for i, s := range raw {
		c, err := dav.CleanSegment(s)
		if err != nil {
			return t, dav.BadRequest("invalid path segment")
		}
		segs[i] = c
	}
	t.kind = targetKind(len(segs) - 1)
	t.user = segs[0]
	if len(segs) > 1 {
		t.book = segs[1]
	}
	if len(segs) > 2 {
		if trailing {
			return target{}, dav.NotFound("not found")
		}
		t.name = segs[2]
	}
	return t, nil
}
