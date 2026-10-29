// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package store defines how calendar and contact data is stored: collections of
// objects kept as the bytes a client sent, plus a change journal that lets
// the protocol engines answer "what changed since this token".
//
// Implementations live in subpackages: memory for tests and examples,
// filestore for a file-backed store, and storetest, a conformance suite any
// implementation has to pass.
package store
