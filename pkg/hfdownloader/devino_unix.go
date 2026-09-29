// SPDX-License-Identifier: Apache-2.0

//go:build unix

package hfdownloader

import (
	"os"
	"syscall"
)

func fileDevIno(fi os.FileInfo) (uint64, uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), st.Ino
	}
	return 0, 0
}

func fileLinkCount(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
