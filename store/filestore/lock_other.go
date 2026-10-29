//go:build !unix

// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import "errors"

func lockDir(string) (func() error, error) {
	return nil, errors.New("filestore: locking is not supported on this platform")
}
