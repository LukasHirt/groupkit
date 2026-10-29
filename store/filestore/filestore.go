// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

// Package filestore is a store.Store that keeps its data in plain files.
package filestore

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

const (
	formatVersion     = "1\n"
	defaultMaxJournal = 10000
	formatVersionFile = "format-version"
	ownersDirName     = "owners"
)

var (
	// ErrLocked means another process or Store holds the directory.
	ErrLocked = errors.New("filestore: directory is locked by another store")
	// ErrCorrupt means a collection's files disagree with its log.
	ErrCorrupt = errors.New("filestore: corrupt collection")
)

type options struct {
	maxJournal int
	logger     *slog.Logger
}

// Option configures Open.
type Option func(*options)

// WithMaxJournal sets how many change log entries a collection keeps. Values
// below 1 are ignored. The default is 10000.
func WithMaxJournal(n int) Option {
	return func(o *options) {
		if n >= 1 {
			o.maxJournal = n
		}
	}
}

// WithLogger sets where the store reports problems it works around, such as
// a damaged collection that ListCollections leaves out. A nil logger is
// ignored. By default nothing is logged.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// Store keeps calendars and address books in a directory tree.
type Store struct {
	root string
	opts options

	releaseLock func() error

	// mu guards closed and cols. It is taken before any collection's mutex.
	mu     sync.Mutex
	closed bool
	cols   map[string]*collection // loaded collections by directory

	closeOnce sync.Once
	closeErr  error
}

// Open opens the store in root, creating the directory when it doesn't exist.
// It holds an exclusive lock on the directory until Close, and returns
// ErrLocked when another process or Store holds it.
func Open(root string, opts ...Option) (*Store, error) {
	o := options{maxJournal: defaultMaxJournal, logger: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&o)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, dirMode); err != nil {
		return nil, err
	}
	release, err := lockDir(root)
	if err != nil {
		return nil, err
	}
	if err := initRoot(root); err != nil {
		_ = release()
		return nil, err
	}
	return &Store{root: root, opts: o, releaseLock: release, cols: map[string]*collection{}}, nil
}

// initRoot checks the format version, writing it when the directory is new,
// and makes sure the owners directory exists.
func initRoot(root string) error {
	path := filepath.Join(root, formatVersionFile)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := atomicWriteFile(root, root, formatVersionFile, []byte(formatVersion), time.Time{}); err != nil {
			return fmt.Errorf("filestore: write %s: %w", formatVersionFile, err)
		}
	case err != nil:
		return err
	case string(data) != formatVersion:
		return fmt.Errorf("filestore: unsupported format version %q in %s (this build reads %q)",
			strings.TrimSpace(string(data)), path, strings.TrimSpace(formatVersion))
	}
	return os.MkdirAll(filepath.Join(root, ownersDirName), dirMode)
}

// Close releases the directory lock. Calling it again returns nil.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		var errs []error
		for dir, c := range s.cols {
			c.mu.Lock()
			if c.logFile != nil {
				errs = append(errs, c.logFile.Close())
				c.logFile = nil
			}
			c.mu.Unlock()
			delete(s.cols, dir)
		}
		s.mu.Unlock()
		errs = append(errs, s.releaseLock())
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// OwnerDir returns the directory for owner's data. A program that embeds the
// store can keep its own files there. It only computes the path and creates
// nothing. It returns "" for an owner that store.ValidateOwner rejects.
func (s *Store) OwnerDir(owner string) string {
	if store.ValidateOwner(owner) != nil {
		return ""
	}
	return filepath.Join(s.root, ownersDirName, fileName(owner))
}
