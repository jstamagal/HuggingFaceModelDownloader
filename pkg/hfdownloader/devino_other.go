// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package hfdownloader

import "os"

// No inode on this platform: key on size+mtime only (path-independent).
func fileDevIno(fi os.FileInfo) (uint64, uint64) { return 0, 0 }

func fileLinkCount(fi os.FileInfo) uint64 { return 1 }
