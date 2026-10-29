// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package carddav serves address books over CardDAV (RFC 6352) from a store.
package carddav

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/LukasHirt/groupkit/access"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// defaultMaxObjectSize is the object size limit when Config leaves it zero.
const defaultMaxObjectSize = 10 << 20

// Config configures a Handler.
type Config struct {
	// Store holds the address books. Required.
	Store store.Store

	// Prefix is the URL path the handler is mounted at, such as "/dav". It is
	// empty or starts with "/", has no trailing slash and consists of plain
	// ASCII segments (letters, digits and "-._~"). Address books live
	// under Prefix + "/addressbooks/".
	Prefix string

	// Resolve maps the user named in a URL (already percent-decoded) to the
	// store owner, or refuses the request with an error wrapping one of the
	// errors of package access. Any other error answers 500. It is called on
	// every request. Required.
	Resolve func(r *http.Request, urlUser string) (owner string, err error)

	// MaxObjectSize is the largest accepted object in bytes. Zero means 10 MiB.
	MaxObjectSize int64

	// Logger receives warnings and server faults, never object data. Nil
	// discards them.
	Logger *slog.Logger
}

// Handler is an http.Handler for the address books under a prefix. It is safe
// for concurrent use.
type Handler struct {
	store   store.Store
	prefix  string
	resolve func(r *http.Request, urlUser string) (string, error)
	maxSize int64
	log     *slog.Logger
}

// New checks cfg, applies defaults and returns a Handler. The Config is
// copied.
func New(cfg Config) (*Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("carddav: Config.Store is nil")
	}
	if cfg.Resolve == nil {
		return nil, errors.New("carddav: Config.Resolve is nil")
	}
	if err := dav.ValidPrefix(cfg.Prefix); err != nil {
		return nil, fmt.Errorf("carddav: invalid Config.Prefix: %w", err)
	}
	if cfg.MaxObjectSize < 0 {
		return nil, fmt.Errorf("carddav: Config.MaxObjectSize %d is negative", cfg.MaxObjectSize)
	}
	h := &Handler{
		store:   cfg.Store,
		prefix:  cfg.Prefix,
		resolve: cfg.Resolve,
		maxSize: cfg.MaxObjectSize,
		log:     cfg.Logger,
	}
	if h.maxSize == 0 {
		h.maxSize = defaultMaxObjectSize
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	return h, nil
}

// HomeHref returns the escaped URL path of a user's address book home,
// Prefix + "/addressbooks/" + user + "/".
func (h *Handler) HomeHref(urlUser string) string {
	return h.prefix + "/addressbooks/" + dav.EscapeHref(urlUser) + "/"
}

// allowed returns the methods valid for a kind of target.
func allowed(k targetKind) []string {
	switch k {
	case kindHome:
		return []string{"OPTIONS", "PROPFIND"}
	case kindCollection:
		return []string{"OPTIONS", "PROPFIND", "PROPPATCH", "MKCOL", "DELETE", "REPORT"}
	default:
		return []string{"OPTIONS", "HEAD", "GET", "PUT", "DELETE", "PROPFIND"}
	}
}

// allowHeader is the Allow header value for a kind of target.
func allowHeader(k targetKind) string { return strings.Join(allowed(k), ", ") }

func isAllowed(k targetKind, method string) bool {
	for _, m := range allowed(k) {
		if m == method {
			return true
		}
	}
	return false
}

// request is what the method handlers work on: the parsed target and the
// store owner Resolve chose for it.
type request struct {
	target
	owner string
}

// davHeader is the DAV compliance header.
var davHeader = dav.ComplianceHeader(dav.CardDAVClasses()...)

// ServeHTTP parses the path, asks Resolve for the store owner and dispatches
// on method and target kind.
//
// A 401 carries no WWW-Authenticate header: the server that composes this
// handler adds the challenge.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions && r.URL.Path == "*" {
		w.Header().Set("DAV", davHeader)
		w.WriteHeader(http.StatusOK)
		return
	}
	t, derr := h.parsePath(r.URL.EscapedPath())
	if derr != nil {
		dav.WriteError(w, derr)
		return
	}
	owner, err := h.resolve(r, t.user)
	if err != nil {
		h.writeResolveError(w, r, err)
		return
	}
	if owner == "" {
		h.log.Error("resolve returned an empty owner", "method", r.Method, "path", r.URL.EscapedPath())
		dav.WriteError(w, &dav.Error{Status: http.StatusInternalServerError, Message: "internal error"})
		return
	}
	if !isAllowed(t.kind, r.Method) {
		methodNotAllowed(w, t.kind)
		return
	}
	req := request{target: t, owner: owner}

	switch r.Method {
	case http.MethodOptions:
		h.options(w, req.target)
	case "PROPFIND":
		h.propfind(w, r, req.target, req.owner)
	case http.MethodGet, http.MethodHead:
		h.getObject(w, r, req.target, req.owner)
	case http.MethodPut:
		h.putObject(w, r, req.target, req.owner)
	case "MKCOL":
		h.mkcol(w, r, req.target, req.owner)
	case "PROPPATCH":
		h.proppatch(w, r, req.target, req.owner)
	case "REPORT":
		h.report(w, r, req.target, req.owner)
	case http.MethodDelete:
		if t.kind == kindCollection {
			h.deleteCollection(w, r, req.target, req.owner)
			return
		}
		h.deleteObject(w, r, req.target, req.owner)
	default:
		// Valid for the kind but not implemented yet.
		methodNotAllowed(w, t.kind)
	}
}

func (h *Handler) writeResolveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, access.ErrUnauthenticated):
		dav.WriteError(w, &dav.Error{Status: http.StatusUnauthorized, Message: "authentication required"})
	case errors.Is(err, access.ErrForbidden):
		dav.WriteError(w, &dav.Error{Status: http.StatusForbidden, Message: "forbidden"})
	case errors.Is(err, access.ErrUserNotFound):
		dav.WriteError(w, dav.NotFound("user not found"))
	default:
		h.log.Error("resolve failed", "method", r.Method, "path", r.URL.EscapedPath(), "error", err)
		dav.WriteError(w, &dav.Error{Status: http.StatusInternalServerError, Message: "internal error"})
	}
}

func (h *Handler) options(w http.ResponseWriter, t target) {
	hd := w.Header()
	hd.Set("DAV", davHeader)
	hd.Set("Allow", allowHeader(t.kind))
	w.WriteHeader(http.StatusOK)
}

func methodNotAllowed(w http.ResponseWriter, k targetKind) {
	w.Header().Set("Allow", allowHeader(k))
	dav.WriteError(w, &dav.Error{Status: http.StatusMethodNotAllowed, Message: "method not allowed"})
}
