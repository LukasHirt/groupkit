// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package groupkit is a toolkit for calendars, contacts and tasks: CalDAV and
// CardDAV servers, iCalendar and vCard handling, and storage you can embed in
// your own service.
//
// The module is split into packages so a program imports only what it needs:
//
//   - store: the storage interface, with an in-memory store, a file-backed
//     store, and a conformance suite every store must pass
//   - ical and vcard: parsing, validation and query matching
//   - caldav and carddav: protocol engines that serve a store over HTTP
//
// To serve contacts and calendars to standard clients, call NewServer with a
// store, a URL prefix and two functions that tie requests to your own users,
// then mount Server.Handler under the prefix and Server.WellKnown at the
// well-known paths of both protocols:
//
//	// currentUser and authorize are your own functions; see Config.
//	srv, err := groupkit.NewServer(groupkit.Config{
//		Store:       st,
//		Prefix:      "/dav",
//		CurrentUser: currentUser,
//		Authorize:   authorize,
//		Challenge:   `Basic realm="example"`,
//	})
//	mux := http.NewServeMux()
//	mux.Handle("/dav/", srv.Handler())
//	mux.Handle("/.well-known/carddav", srv.WellKnown())
//	mux.Handle("/.well-known/caldav", srv.WellKnown())
//
// The package example is a complete, compiled version of this.
//
// Address books live under Prefix + "/addressbooks/" and calendars under
// Prefix + "/calendars/". Config.DisableAddressBooks and
// Config.DisableCalendars switch either protocol off.
//
// The server does not authenticate anyone. CurrentUser and Authorize report
// who is asking and what they may reach, using the errors ErrUnauthenticated,
// ErrForbidden and ErrUserNotFound. Call EnsureDefaultCollections when you
// provision a user so clients find an address book and a calendar.
//
// The API is not stable yet.
package groupkit

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/LukasHirt/groupkit/access"
	"github.com/LukasHirt/groupkit/caldav"
	"github.com/LukasHirt/groupkit/carddav"
	"github.com/LukasHirt/groupkit/internal/dav"
	"github.com/LukasHirt/groupkit/store"
)

// Errors a Config function returns to refuse a request. They are the errors
// of package access, so a function can return any error that wraps them.
var (
	// ErrUnauthenticated means the request has no valid credentials. It
	// answers 401, with a WWW-Authenticate header when Config.Challenge is set.
	ErrUnauthenticated = access.ErrUnauthenticated
	// ErrForbidden means the user may not reach the resource. It answers 403.
	ErrForbidden = access.ErrForbidden
	// ErrUserNotFound means the user named in the URL does not exist. It
	// answers 404.
	ErrUserNotFound = access.ErrUserNotFound
)

// Config configures a Server.
type Config struct {
	// Store holds the address books and calendars. Required.
	Store store.Store

	// Prefix is the URL path the server is mounted at, such as "/dav". It is
	// empty or starts with "/", has no trailing slash and consists of plain
	// ASCII segments (letters, digits and "-._~"). Handler does not
	// strip it: mount Handler so the full request path reaches it.
	Prefix string

	// CurrentUser names the user making the request, for the root and the
	// principal, which are not tied to a user in the URL. It returns an error
	// wrapping one of the errors above to refuse the request; any other error
	// answers 500. The name must not be empty. Required.
	CurrentUser func(r *http.Request) (string, error)

	// Authorize maps the user named in an address book or calendar URL
	// (already percent-decoded) to the store owner, or refuses the request
	// with an error wrapping one of the errors above. Any other error
	// answers 500. It is called on every address book and calendar request.
	// Required.
	Authorize func(r *http.Request, urlUser string) (owner string, err error)

	// Challenge is the value of the WWW-Authenticate header sent with a 401,
	// such as `Basic realm="example"`. Empty sends no header. Control
	// characters, including carriage return and line feed, are rejected.
	Challenge string

	// MaxObjectSize is the largest accepted object in bytes. Zero means 10 MiB.
	MaxObjectSize int64

	// DisableAddressBooks turns CardDAV off: the paths under
	// Prefix + "/addressbooks/" answer 404, the principal has no
	// addressbook-home-set, and the server's EnsureDefaultCollections method
	// creates no address book. The default is false.
	DisableAddressBooks bool

	// DisableCalendars turns CalDAV off in the same way for
	// Prefix + "/calendars/" and calendar-home-set. The default is false.
	// Disabling both protocols is an error.
	DisableCalendars bool

	// Logger receives server faults: method, kind of resource and error,
	// never credentials, headers or query strings. Nil discards them.
	Logger *slog.Logger
}

// mount is a protocol handler served under Prefix + "/" + segment + "/".
type mount struct {
	segment string
	handler http.Handler
}

// Server serves discovery and the protocol handlers under a prefix. It is
// safe for concurrent use.
type Server struct {
	prefix      string
	currentUser func(r *http.Request) (string, error)
	challenge   string
	log         *slog.Logger

	store                          store.Store
	disableBooks, disableCalendars bool

	// mounts are the protocol handlers by first path segment.
	mounts []mount
	// classes are the DAV classes the mounted handlers add; davHeader is the
	// DAV header built from them.
	classes   []string
	davHeader string
	// homeSets build the home-set properties of a principal, by user.
	homeSets []func(user string) map[xmlName]dav.PropDef
}

// NewServer checks cfg and returns a Server. The Config is copied.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("groupkit: Config.Store is nil")
	}
	if cfg.CurrentUser == nil {
		return nil, errors.New("groupkit: Config.CurrentUser is nil")
	}
	if cfg.Authorize == nil {
		return nil, errors.New("groupkit: Config.Authorize is nil")
	}
	if strings.ContainsFunc(cfg.Challenge, isControl) {
		return nil, errors.New("groupkit: Config.Challenge contains a control character")
	}
	if err := dav.ValidPrefix(cfg.Prefix); err != nil {
		return nil, fmt.Errorf("groupkit: invalid Config.Prefix: %w", err)
	}
	if cfg.DisableAddressBooks && cfg.DisableCalendars {
		return nil, errors.New("groupkit: Config.DisableAddressBooks and Config.DisableCalendars disable everything")
	}
	s := &Server{
		davHeader:        dav.ComplianceHeader(),
		prefix:           cfg.Prefix,
		store:            cfg.Store,
		currentUser:      cfg.CurrentUser,
		challenge:        cfg.Challenge,
		log:              cfg.Logger,
		disableBooks:     cfg.DisableAddressBooks,
		disableCalendars: cfg.DisableCalendars,
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if !cfg.DisableAddressBooks {
		cards, err := carddav.New(carddav.Config{
			Store:         cfg.Store,
			Prefix:        cfg.Prefix,
			Resolve:       cfg.Authorize,
			MaxObjectSize: cfg.MaxObjectSize,
			Logger:        cfg.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("groupkit: %w", err)
		}
		s.mount("addressbooks", cards, dav.CardDAVClasses()...)
		s.homeSets = append(s.homeSets, func(user string) map[xmlName]dav.PropDef {
			return map[xmlName]dav.PropDef{
				dav.AddressbookHomeSet: fixed(dav.HrefProp(dav.AddressbookHomeSet, cards.HomeHref(user))),
			}
		})
	}
	if !cfg.DisableCalendars {
		cals, err := caldav.New(caldav.Config{
			Store:         cfg.Store,
			Prefix:        cfg.Prefix,
			Resolve:       cfg.Authorize,
			MaxObjectSize: cfg.MaxObjectSize,
			Logger:        cfg.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("groupkit: %w", err)
		}
		s.mount("calendars", cals, dav.CalDAVClasses()...)
		s.homeSets = append(s.homeSets, func(user string) map[xmlName]dav.PropDef {
			return map[xmlName]dav.PropDef{
				dav.CalendarHomeSet: fixed(dav.HrefProp(dav.CalendarHomeSet, cals.HomeHref(user))),
			}
		})
	}
	return s, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// mount adds a protocol handler under Prefix + "/" + segment + "/". A 401 it
// writes gets the WWW-Authenticate header when a challenge is configured.
func (s *Server) mount(segment string, h http.Handler, classes ...string) {
	for _, c := range classes {
		if !slices.Contains(s.classes, c) {
			s.classes = append(s.classes, c)
		}
	}
	s.davHeader = dav.ComplianceHeader(s.classes...)
	if s.challenge != "" {
		inner := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(&challengeWriter{ResponseWriter: w, challenge: s.challenge}, r)
		})
	}
	s.mounts = append(s.mounts, mount{segment: segment, handler: h})
}

// Handler returns the handler for everything under Prefix: the root, the
// principals and the protocol handlers. Anything else answers 404.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions && r.URL.Path == "*" {
		w.Header().Set("DAV", s.davHeader)
		w.WriteHeader(http.StatusOK)
		return
	}
	path := r.URL.EscapedPath()
	rest, ok := strings.CutPrefix(path, s.prefix)
	if !ok {
		notFound(w)
		return
	}
	switch {
	case rest == "" || rest == "/":
		s.serveDiscovery(w, r, kindRoot, "")
		return
	case !strings.HasPrefix(rest, "/"):
		notFound(w)
		return
	}
	first, tail, _ := strings.Cut(rest[1:], "/")
	if first == "principals" {
		seg, extra, _ := strings.Cut(tail, "/")
		if seg == "" || extra != "" {
			notFound(w)
			return
		}
		s.serveDiscovery(w, r, kindPrincipal, seg)
		return
	}
	for _, m := range s.mounts {
		if m.segment == first {
			m.handler.ServeHTTP(w, r)
			return
		}
	}
	notFound(w)
}

func notFound(w http.ResponseWriter) { dav.WriteError(w, dav.NotFound("not found")) }

// WellKnown returns a handler for the well-known URLs of CardDAV and CalDAV.
// GET, HEAD and PROPFIND answer 301 to Prefix + "/", without asking who is
// calling. It does not look at the request path: mount it at
// /.well-known/carddav and /.well-known/caldav.
func (s *Server) WellKnown() http.Handler {
	const allow = "OPTIONS, GET, HEAD, PROPFIND"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, "PROPFIND":
			w.Header().Set("Location", s.prefix+"/")
			w.WriteHeader(http.StatusMovedPermanently)
		case http.MethodOptions:
			w.Header().Set("Allow", allow)
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("Allow", allow)
			dav.WriteError(w, &dav.Error{Status: http.StatusMethodNotAllowed, Message: "method not allowed"})
		}
	})
}

// authError answers a refused or failed CurrentUser call.
func (s *Server) authError(w http.ResponseWriter, r *http.Request, kind string, err error) {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		if s.challenge != "" {
			w.Header().Set("WWW-Authenticate", s.challenge)
		}
		dav.WriteError(w, &dav.Error{Status: http.StatusUnauthorized, Message: "authentication required"})
	case errors.Is(err, ErrForbidden):
		forbidden(w)
	case errors.Is(err, ErrUserNotFound):
		dav.WriteError(w, dav.NotFound("user not found"))
	default:
		s.log.Error("current user failed", "method", r.Method, "resource", kind, "error", err)
		internalError(w)
	}
}

func forbidden(w http.ResponseWriter) {
	dav.WriteError(w, &dav.Error{Status: http.StatusForbidden, Message: "forbidden"})
}

func internalError(w http.ResponseWriter) {
	dav.WriteError(w, &dav.Error{Status: http.StatusInternalServerError, Message: "internal error"})
}

// challengeWriter adds WWW-Authenticate to a 401. Everything else passes
// through, including flushing, so streamed responses keep streaming.
type challengeWriter struct {
	http.ResponseWriter
	challenge string
	wrote     bool
}

func (c *challengeWriter) WriteHeader(code int) {
	if code >= 200 {
		if c.wrote {
			return
		}
		c.wrote = true
		if code == http.StatusUnauthorized {
			c.Header().Set("WWW-Authenticate", c.challenge)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *challengeWriter) Write(b []byte) (int, error) {
	c.wrote = true
	return c.ResponseWriter.Write(b)
}

// Flush sends buffered data to the client.
func (c *challengeWriter) Flush() {
	c.wrote = true
	_ = http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer, for http.ResponseController.
func (c *challengeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
