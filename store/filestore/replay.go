// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// modTimeSlack is how far a file's modification time may be from its record
// before the file is hashed. Some file systems keep coarse times.
const modTimeSlack = 2 * time.Second

// replay brings an empty collection up to date with the records of its log,
// leaving the state a live process has after the same writes. The journal
// holds the records above the floor in collection.json, trimmed to limit.
func (c *collection) replay(recs []record, limit int) {
	for _, r := range recs {
		c.applyObject(r)
		if r.Rev > c.meta.Floor {
			c.recent = append(c.recent, r)
		}
	}
	c.trimJournal(limit)
	if n := len(recs); n > 0 && recs[n-1].Rev > c.meta.Revision {
		// collection.json is only rewritten for property changes, so the log is
		// usually ahead of it.
		c.meta.Revision = recs[n-1].Rev
	}
	c.logLines = len(recs)
}

// verifyObjects checks every live object against its file with checkFile. When
// several objects are damaged, the error names the first by name, so the same
// damage always gives the same error.
func (c *collection) verifyObjects() error {
	names := make([]string, 0, len(c.objects))
	for name := range c.objects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		o := c.objects[name]
		got, why, err := checkFile(c.objectPath(name), o.size, o.mtime, o.etag)
		if err != nil {
			return fmt.Errorf("%s: object %q: %w", c.dir, name, err)
		}
		if got != fileMatches {
			// Not wrapped in os.ErrNotExist for a missing file: that error from a
			// load means the collection is gone.
			return c.corrupt(name, why)
		}
	}
	return nil
}

// clearTmp removes what interrupted writes left in tmp/. It is best effort.
func (c *collection) clearTmp() {
	tmp := filepath.Join(c.dir, tmpDirName)
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(tmp, e.Name()))
	}
}
