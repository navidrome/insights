//go:build linux

package store

import "syscall"

// blockSize returns the unit that a Statfs_t's block counts are expressed in.
//
// Linux reports two sizes. Bsize is the optimal transfer size; Frsize is the fragment size the
// block counts actually mean, and it is what df computes with. On ext4 and overlay — the
// production volume and the container root — they are equal and the distinction does not
// matter. On Docker Desktop's virtiofs bind mount, which is what DATA_FOLDER is under
// `make dev`, Bsize is 1 MiB against a Frsize of 4 KiB: using Bsize there reports 256 times
// the real free space, and a purge driven by it would simply never fire.
//
// Frsize is 0 on filesystems that do not report it, hence the fallback.
func blockSize(st *syscall.Statfs_t) uint64 {
	if st.Frsize > 0 {
		return uint64(st.Frsize)
	}
	return uint64(st.Bsize)
}
