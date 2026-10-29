// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/LukasHirt/groupkit/store"
)

// recovery is what a load does to the disk once the collection checks out.
type recovery struct {
	// logLen is the length the log is cut to. It drops a torn last line and a
	// rolled back record.
	logLen int
	// remove is the file of an object the last record deleted, when the
	// process died before removing it.
	remove string
}

// recoverLast decides what to do about the last record of the log. Writes to a
// collection are serialized, so that record is the only one that can have been
// in flight when a process died. A put whose file is in place is kept. A put
// that never reached its file is rolled back: the records returned leave it
// out and the log is cut before it, so revisions continue as if it never
// happened. When the name was deleted before the put, any file there counts as
// the state before it, because the removal after a delete can fail. A
// tombstone is kept, and a file it left behind is removed. A put that fits
// neither the disk nor the state before it is damage and returns an error
// wrapping ErrCorrupt.
//
// The last record's file is always hashed; see fileETag.
//
// recs and validLen come from parseLog(logData). recoverLast only reads the
// disk; loadCollection applies the recovery after every other check passed.
func (c *collection) recoverLast(recs []record, logData []byte, validLen int) ([]record, recovery, error) {
	rec := recovery{logLen: validLen}
	n := len(recs)
	if n == 0 {
		return recs, rec, nil
	}
	last := recs[n-1]
	path := c.objectPath(last.Name)

	if last.Deleted {
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			rec.remove = path
		case !errors.Is(err, fs.ErrNotExist):
			return nil, recovery{}, fmt.Errorf("%s: object %q: %w", c.dir, last.Name, err)
		}
		return recs, rec, nil
	}

	// The size and time shortcut of checkFile is not safe here: an overwrite
	// of the same length within modTimeSlack of the old file would pass as
	// finished while the file still holds the old bytes. So the file is hashed.
	got, etag, err := fileETag(path)
	if err != nil {
		return nil, recovery{}, fmt.Errorf("%s: object %q: %w", c.dir, last.Name, err)
	}
	if got == fileMatches && etag == last.ETag {
		return recs, rec, nil
	}

	// Does the disk still show the state before the record?
	prev := previousRecord(recs[:n-1], last.Name)
	var before bool
	switch {
	case prev == nil: // a create: no file
		before = got == fileMissing
	case prev.Deleted:
		// The name was deleted. A file there is one whose removal failed after
		// the delete was committed; rolling back leaves it alone.
		before = true
	default: // an overwrite: the previous version
		before = got == fileMatches && etag == prev.ETag
	}
	if !before {
		return nil, recovery{}, c.corrupt(last.Name, "the file matches neither the last log record nor the one before it")
	}
	// validLen ends in a newline; the record before starts after the previous one.
	rec.logLen = bytes.LastIndexByte(logData[:validLen-1], '\n') + 1
	return recs[:n-1], rec, nil
}

// previousRecord returns the last record in recs for object name, or nil.
func previousRecord(recs []record, name string) *record {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Name == name {
			return &recs[i]
		}
	}
	return nil
}

// apply carries out a recovery on f, the open log. It is called only for a
// collection that loaded without damage.
func (rec recovery) apply(f *os.File, size int) error {
	if rec.logLen < size {
		// Later appends must not follow a torn line or a rolled back record.
		if err := f.Truncate(int64(rec.logLen)); err != nil {
			return err
		}
		if err := syncFile(f); err != nil {
			return err
		}
	}
	if rec.remove != "" {
		// Best effort, like the removal in DeleteObject: the tombstone already
		// says the object is gone, and the next load tries again.
		_ = os.Remove(rec.remove)
	}
	return nil
}

// fileETag returns the ETag of the regular file at path, with fileMatches.
// It returns fileMissing when there is no file and fileDiffers when the path
// is not a regular file.
func fileETag(path string) (got fileCheck, etag string, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileMissing, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	if !fi.Mode().IsRegular() {
		return fileDiffers, "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileMissing, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	return fileMatches, store.ETag(data), nil
}

// fileCheck is how a file compares with a record.
type fileCheck int

const (
	fileMatches fileCheck = iota
	fileMissing
	fileDiffers
)

// checkFile compares the file at path with a record of the given size,
// modification time and ETag. A file whose time is within modTimeSlack of
// the record and whose size matches is taken as is; one whose time is further
// off is hashed. Anything but a regular file differs, including a symbolic
// link. For a file that differs, why says how.
func checkFile(path string, size int64, mtime time.Time, etag string) (got fileCheck, why string, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileMissing, "file is missing", nil
	}
	if err != nil {
		return 0, "", err
	}
	if !fi.Mode().IsRegular() {
		return fileDiffers, "not a regular file", nil
	}
	if fi.Size() != size {
		return fileDiffers, fmt.Sprintf("size is %d, the log says %d", fi.Size(), size), nil
	}
	if d := fi.ModTime().Sub(mtime); d >= -modTimeSlack && d <= modTimeSlack {
		return fileMatches, "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileMissing, "file is missing", nil
	}
	if err != nil {
		return 0, "", err
	}
	if store.ETag(data) != etag {
		return fileDiffers, "content does not match its record", nil
	}
	return fileMatches, "", nil
}

// corrupt returns an error wrapping ErrCorrupt about object name.
func (c *collection) corrupt(name, why string) error {
	return fmt.Errorf("%w: %s (collection %q): object %q: %s", ErrCorrupt, c.dir, c.meta.Name, name, why)
}
