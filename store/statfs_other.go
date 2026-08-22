//go:build !linux

package store

import "syscall"

// blockSize returns the unit a Statfs_t's block counts are expressed in. Only Linux has a
// separate Frsize; elsewhere Bsize is the fragment size.
func blockSize(st *syscall.Statfs_t) uint64 {
	return uint64(st.Bsize)
}
