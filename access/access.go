// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package access holds the errors that tell the protocol handlers why a
// request may not go on.
//
// The functions an embedding program supplies (the resolver of a handler
// config, the authorization hooks of a server) return one of these errors,
// bare or wrapped with fmt.Errorf("...: %w", err), when they refuse a
// request. The handlers test for them with errors.Is and answer with the
// matching HTTP status. Any other error is a server fault: the handler logs
// it and answers 500 without detail.
package access

import "errors"

var (
	// ErrUnauthenticated means the request carries no valid credentials. The
	// handlers answer 401. They set no WWW-Authenticate header; the server
	// that composes them adds the challenge.
	ErrUnauthenticated = errors.New("access: unauthenticated")

	// ErrForbidden means the authenticated user may not use the addressed
	// user's data. The handlers answer 403.
	ErrForbidden = errors.New("access: forbidden")

	// ErrUserNotFound means the user named in the URL does not exist. The
	// handlers answer 404.
	ErrUserNotFound = errors.New("access: user not found")
)
