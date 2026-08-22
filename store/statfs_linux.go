//go:build linux

package store

import "syscall"

// blockSize returns the unit a Statfs_t's block counts are expressed in. Frsize, not Bsize:
// on Docker Desktop's virtiofs mount Bsize is 1 MiB against a 4 KiB Frsize, so Bsize would
// report 256x the real free space. Frsize is 0 where it is not reported, hence the fallback.
func blockSize(st *syscall.Statfs_t) uint64 {
	if st.Frsize > 0 {
		return uint64(st.Frsize)
	}
	return uint64(st.Bsize)
}
