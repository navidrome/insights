//go:build !linux

package store

import "syscall"

// blockSize returns the unit that a Statfs_t's block counts are expressed in. Only Linux
// carries a separate Frsize field; elsewhere — darwin, the development platform — Bsize is the
// fragment size. Its type differs by platform (uint32 on darwin, int64 on Linux), so the
// conversion is needed either way.
func blockSize(st *syscall.Statfs_t) uint64 {
	return uint64(st.Bsize)
}
