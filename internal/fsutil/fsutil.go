// Package fsutil holds the filesystem helpers shared by the packages that publish files while
// something else is reading them.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic replaces path in one step: a reader either sees the previous contents or the
// new ones, never the empty file that an O_TRUNC write leaves behind while it is in progress.
//
// Both callers publish into a live read path. Summaries are rewritten while GetSummaries walks
// them, and a reader landing in the truncation window logs the file as malformed and drops that
// day from the charts. charts.json is rewritten daily by the same process that serves it, and a
// request landing in that window gets a 200 with a truncated body.
//
// The temporary file is created in the target's own directory, so the rename stays inside one
// filesystem and is therefore atomic, and it is removed on every failure path: a failed write
// leaves the previous contents in place, with nothing beside them.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once the rename succeeded

	// CreateTemp makes the file 0600; set the mode explicitly so it does not depend on that.
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
