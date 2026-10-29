// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"os"
	"path/filepath"
	"time"
)

const (
	dirMode  = 0o700
	fileMode = 0o600
)

// syncFile is (*os.File).Sync. Tests replace it to make flushing the log fail.
var syncFile = (*os.File).Sync

// atomicWriteFile writes data to dir/name so that readers see either the old
// file or the complete new one. The data goes to a temporary file in tmpDir
// first, which must be on the same file system as dir. When mtime is not the
// zero time it becomes the file's modification time.
func atomicWriteFile(dir, tmpDir, name string, data []byte, mtime time.Time) (err error) {
	f, err := os.CreateTemp(tmpDir, "w-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(fileMode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if !mtime.IsZero() {
		if err = os.Chtimes(tmp, mtime, mtime); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir flushes a directory's entries to disk.
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
