// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"io"
	"os"
	"path/filepath"
)

// WriteAtomic writes r to path through a temporary file of its own in the
// same directory and renames it into place. Several processes share the
// download caches (test VMs in parallel slots, each with its own package
// proxy): a reader sees the old file or the complete new one, never a
// partial one, and two writers of the same file cannot rename each
// other's half-written temporary file.
func WriteAtomic(path string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// CreateTemp makes the file 0600; the caches were 0644 before
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
