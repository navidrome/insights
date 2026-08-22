// Package fsutil holds filesystem helpers for publishing files that are read concurrently.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path in one step: a reader sees the old contents or the new ones,
// never the empty file an O_TRUNC write leaves behind. The temp file goes in the target's own
// directory so the rename stays on one filesystem, and every failure path removes it.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename succeeded

	// CreateTemp makes the file 0600; do not depend on that.
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
