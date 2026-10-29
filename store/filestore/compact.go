// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// compactAfterWrite compacts the log of c after a write that succeeded. The
// write stands whatever happens here, so a failure is only logged; the next
// write tries again. The caller holds c.mu for writing.
func (s *Store) compactAfterWrite(c *collection) {
	if err := c.maybeCompact(s.opts.maxJournal); err != nil {
		s.opts.logger.Warn("filestore: could not compact the change log",
			"collection", c.meta.Name, "dir", c.dir, "err", err)
	}
}

// maybeCompact rewrites the log when it has more than 2*limit lines plus one
// per live object. The new log holds one record per live object whose latest
// record is at or below the floor, so its metadata survives, followed by the
// journal: the newest limit records, tombstones included. Tombstones at or
// below the floor are dropped.
//
// The floor goes to collection.json first, then the new log replaces the old
// one with a rename. A crash between the two leaves a higher floor with the
// old log, which loads fine: every record is still replayed, and only those
// above the floor enter the journal.
//
// The last record of the log stays the same, byte for byte. Recovery treats it
// as possibly in flight and checks it against the disk.
//
// On an error the old log is still in place and valid, unless the new one
// replaced it and the store could not switch to it; then c is marked stale and
// the next use loads it again. The caller holds c.mu for writing.
func (c *collection) maybeCompact(limit int) error {
	if c.logLines <= 2*limit+len(c.objects) || len(c.recent) == 0 {
		return nil
	}
	// The journal is trimmed after every write, so c.recent holds exactly the
	// records above c.meta.Floor, at most limit of them.
	floor := c.meta.Floor
	state := make([]record, 0, len(c.objects))
	for name, o := range c.objects {
		if o.rev <= floor {
			state = append(state, record{
				Rev:   o.rev,
				Name:  name,
				ETag:  o.etag,
				UID:   o.uid,
				Size:  o.size,
				MTime: o.mtime.UnixNano(),
			})
		}
	}
	sort.Slice(state, func(i, j int) bool { return state[i].Rev < state[j].Rev })

	var buf bytes.Buffer
	for _, r := range state {
		buf.Write(encodeRecord(r))
	}
	for _, r := range c.recent {
		buf.Write(encodeRecord(r))
	}

	if err := c.writeMeta(c.meta); err != nil {
		return fmt.Errorf("write floor: %w", err)
	}
	f, err := c.writeLog(buf.Bytes())
	if err != nil {
		return err
	}
	old := c.logFile
	c.logFile = f
	c.logLines = len(state) + len(c.recent)
	closeErr := old.Close()
	if err := syncDir(c.dir); err != nil {
		// The new log is in place, but the rename may not be on disk, and
		// later appends would be lost with it.
		c.markStale()
		return fmt.Errorf("flush log rename: %w", err)
	}
	return closeErr
}

// renameFile is os.Rename. Tests replace it to make the log rename fail.
var renameFile = os.Rename

// writeLog writes data to a new file in tmp/, flushes it and renames it over
// the log. It returns the new file, open for appending. When it returns an
// error the old log is in place.
func (c *collection) writeLog(data []byte) (*os.File, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	tmp := filepath.Join(c.dir, tmpDirName, "log-"+id)
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL|os.O_APPEND, fileMode)
	if err != nil {
		return nil, fmt.Errorf("write log: %w", err)
	}
	// The file is closed and removed here on failure, through f itself: a
	// named result would already be nil when a deferred cleanup ran.
	fail := func(err error) (*os.File, error) {
		return nil, errors.Join(fmt.Errorf("write log: %w", err), f.Close(), os.Remove(tmp))
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := renameFile(tmp, filepath.Join(c.dir, logFileName)); err != nil {
		return fail(err)
	}
	return f, nil
}
