// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/LukasHirt/groupkit"
	"github.com/LukasHirt/groupkit/store/memory"
)

// Serve address books and calendars from an in-memory store. A real program
// would pass the handlers to http.ListenAndServe; the example sends requests
// straight to the mux instead.
func Example() {
	st := memory.New()

	// The server does not authenticate anyone. These stand-ins treat the
	// HTTP Basic user name as the signed-in user, which a real program must
	// check against a password or token first.
	currentUser := func(r *http.Request) (string, error) {
		user, _, ok := r.BasicAuth()
		if !ok {
			return "", groupkit.ErrUnauthenticated
		}
		return user, nil
	}
	authorize := func(r *http.Request, urlUser string) (string, error) {
		user, err := currentUser(r)
		if err != nil {
			return "", err
		}
		if user != urlUser {
			return "", groupkit.ErrForbidden
		}
		return user, nil
	}

	srv, err := groupkit.NewServer(groupkit.Config{
		Store:       st,
		Prefix:      "/dav",
		CurrentUser: currentUser,
		Authorize:   authorize,
		Challenge:   `Basic realm="example"`,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/dav/", srv.Handler())
	mux.Handle("/.well-known/carddav", srv.WellKnown())
	mux.Handle("/.well-known/caldav", srv.WellKnown())

	// Provision the user so that clients find an address book and a calendar.
	if err := srv.EnsureDefaultCollections(context.Background(), "alice"); err != nil {
		fmt.Println(err)
		return
	}

	send := func(method, target, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		if user != "" {
			r.SetBasicAuth(user, "secret")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	w := send("GET", "/.well-known/caldav", "")
	fmt.Println("well-known:", w.Code, w.Header().Get("Location"))
	w = send("PROPFIND", "/dav/calendars/alice/personal/", "alice")
	fmt.Println("alice, her calendar:", w.Code)
	w = send("PROPFIND", "/dav/addressbooks/alice/contacts/", "alice")
	fmt.Println("alice, her address book:", w.Code)
	w = send("PROPFIND", "/dav/calendars/alice/personal/", "bob")
	fmt.Println("bob, alice's calendar:", w.Code)
	w = send("PROPFIND", "/dav/calendars/alice/personal/", "")
	fmt.Println("nobody:", w.Code, w.Header().Get("WWW-Authenticate"))

	// Output:
	// well-known: 301 /dav/
	// alice, her calendar: 207
	// alice, her address book: 207
	// bob, alice's calendar: 403
	// nobody: 401 Basic realm="example"
}
