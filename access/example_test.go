// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package access_test

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/LukasHirt/groupkit/access"
)

// A function that tells the handlers who is asking returns one of these
// errors to refuse a request. Wrapping them with %w keeps the reason in the
// message and still lets the handlers choose the HTTP status.
func Example() {
	authorize := func(r *http.Request, urlUser string) (string, error) {
		if r.Header.Get("Authorization") == "" {
			return "", access.ErrUnauthenticated
		}
		return "", fmt.Errorf("user %q: %w", urlUser, access.ErrUserNotFound)
	}

	_, err := authorize(&http.Request{Header: http.Header{}}, "alice")
	fmt.Println(errors.Is(err, access.ErrUnauthenticated))

	r := &http.Request{Header: http.Header{"Authorization": {"Bearer x"}}}
	_, err = authorize(r, "alice")
	fmt.Println(errors.Is(err, access.ErrUserNotFound), err)
	// Output:
	// true
	// true user "alice": access: user not found
}
