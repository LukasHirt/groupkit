// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store/filestore"
)

const (
	// passwordEnv is the environment variable that holds the password. It is
	// never a flag, so the password stays out of process listings.
	passwordEnv = "GROUPKIT_PASSWORD"

	minPasswordLen = 12
	maxUserLen     = 64
	maxPasswordLen = 256

	// challenge is the WWW-Authenticate value of every 401.
	challenge = `Basic realm="groupkit", charset="UTF-8"`

	// prefix is where the DAV handler is mounted.
	prefix = "/dav"
)

// logOutput is where the server logs go. Tests replace it.
var logOutput io.Writer = os.Stderr

// config is what the example server needs to run.
type config struct {
	// Addr is the listen address, such as 127.0.0.1:8080.
	Addr string
	// DataDir is the directory of the file store. One process may use a
	// directory at a time; a second one fails with a lock error.
	DataDir string
	// User is the one account. It is also the name of the store owner and a
	// segment of the collection URLs.
	User string
	// Password is the account's password.
	Password string
}

// validate refuses configurations that would run with no or weak
// credentials, or with a user name that cannot work.
func (c config) validate() error {
	switch {
	case c.User == "":
		return errors.New("the user name is empty")
	case len(c.User) > maxUserLen:
		return fmt.Errorf("the user name is longer than %d bytes", maxUserLen)
	case !utf8.ValidString(c.User):
		return errors.New("the user name is not valid UTF-8")
	case strings.ContainsAny(c.User, "/:"):
		return errors.New(`the user name must not contain "/" or ":"`)
	case c.User == "." || c.User == "..":
		return errors.New(`the user name must not be "." or ".."`)
	case strings.TrimSpace(c.User) != c.User:
		return errors.New("the user name must not start or end with white space")
	case strings.ContainsFunc(c.User, unicode.IsControl):
		return errors.New("the user name must not contain control characters")
	case c.Password == "":
		return fmt.Errorf("no password: set the environment variable %s", passwordEnv)
	case utf8.RuneCountInString(c.Password) < minPasswordLen:
		return fmt.Errorf("the password in %s must be at least %d characters", passwordEnv, minPasswordLen)
	case len(c.Password) > maxPasswordLen:
		return fmt.Errorf("the password in %s must be at most %d bytes", passwordEnv, maxPasswordLen)
	case c.DataDir == "":
		return errors.New("the data directory is empty")
	}
	if _, port, err := net.SplitHostPort(c.Addr); err != nil || port == "" {
		return fmt.Errorf("the listen address %q is not host:port", c.Addr)
	}
	return nil
}

// newHandler opens the file store in cfg.DataDir and returns the root handler
// and a function that closes the store. Close it after the HTTP server has
// stopped. The default address book and calendar of the user are created.
func newHandler(cfg config) (http.Handler, func() error, error) {
	if err := cfg.validate(); err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewTextHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Open creates the directory (mode 0700) and locks it.
	st, err := filestore.Open(cfg.DataDir, filestore.WithLogger(logger))
	if err != nil {
		return nil, nil, fmt.Errorf("open the data directory: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = st.Close()
		}
	}()

	auth := newAuthenticator(cfg.User, cfg.Password)
	srv, err := groupkit.NewServer(groupkit.Config{
		Store:       st,
		Prefix:      prefix,
		CurrentUser: auth.currentUser,
		Authorize:   auth.authorize,
		Challenge:   challenge,
		Logger:      logger,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := groupkit.EnsureDefaultCollections(context.Background(), st, cfg.User); err != nil {
		return nil, nil, fmt.Errorf("create the default collections: %w", err)
	}

	dav := auth.require(srv.Handler())
	mux := http.NewServeMux()
	mux.Handle(prefix+"/", dav)
	mux.Handle(prefix, dav)
	mux.Handle("/.well-known/caldav", srv.WellKnown())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "groupkit example server\n")
	})
	ok = true
	return mux, st.Close, nil
}

type ctxKey struct{}

// authenticator checks HTTP Basic credentials against the one account.
type authenticator struct {
	user, password string
}

func newAuthenticator(user, password string) *authenticator {
	return &authenticator{user: user, password: password}
}

// check compares both values in constant time. Neither comparison is skipped
// when the other fails.
func (a *authenticator) check(user, password string) bool {
	u := constantTimeEqual(user, a.user)
	p := constantTimeEqual(password, a.password)
	return u && p
}

// compareLen is the size of the buffers constantTimeEqual compares. Config
// validation keeps the user name and the password within it.
const compareLen = 256

// constantTimeEqual reports whether a and b are equal. Both are copied into
// fixed-size zero-padded buffers and the buffers and the lengths are compared
// without branching on their content, so the time taken does not depend on
// where the values differ or how long a guess is. A value longer than the
// buffer is never equal; one dummy comparison keeps the work bounded.
func constantTimeEqual(a, b string) bool {
	var bufA, bufB [compareLen]byte
	if len(a) > compareLen || len(b) > compareLen {
		subtle.ConstantTimeCompare(bufA[:], bufB[:])
		return false
	}
	copy(bufA[:], a)
	copy(bufB[:], b)
	same := subtle.ConstantTimeCompare(bufA[:], bufB[:]) & subtle.ConstantTimeEq(int32(len(a)), int32(len(b)))
	return same == 1
}

// require answers 401 unless the request carries the right credentials. The
// wrapped handler never sees a request that failed.
func (a *authenticator) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || !a.check(user, password) {
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a.user)))
	})
}

func (a *authenticator) currentUser(r *http.Request) (string, error) {
	user, ok := r.Context().Value(ctxKey{}).(string)
	if !ok || user == "" {
		return "", groupkit.ErrUnauthenticated
	}
	return user, nil
}

// authorize allows the signed-in user to reach their own URLs only.
func (a *authenticator) authorize(r *http.Request, urlUser string) (string, error) {
	user, err := a.currentUser(r)
	if err != nil {
		return "", err
	}
	if urlUser != user {
		return "", groupkit.ErrForbidden
	}
	return user, nil
}
