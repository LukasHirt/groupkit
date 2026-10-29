// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package filestore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicWriteFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "dir")
	tmp := filepath.Join(base, "tmp")
	for _, d := range []string{dir, tmp} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := atomicWriteFile(dir, tmp, "f", []byte("new data"), mtime); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(got) != "new data" {
		t.Fatalf("content = %q, err = %v", got, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Fatalf("mtime = %v, want %v", fi.ModTime(), mtime)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	left, err := os.ReadDir(tmp)
	if err != nil || len(left) != 0 {
		t.Fatalf("leftovers in tmp: %v, err = %v", left, err)
	}

	// A zero mtime leaves the time at "now".
	if err := atomicWriteFile(dir, tmp, "g", []byte("x"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(filepath.Join(dir, "g"))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(fi.ModTime()) > time.Minute {
		t.Fatalf("mtime = %v, want about now", fi.ModTime())
	}
}
