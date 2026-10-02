// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"fmt"
	"os"
	"sync"
)

// GGUF headers are re-read on every view/catalog rebuild; the memo keys on
// size+mtime so a changed file is read again.
var ggufMemo sync.Map

type ggufMemoEnt struct {
	key  string
	meta *GGUFMeta
	err  error
}

func readGGUFMetaCached(p string) (*GGUFMeta, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	k := fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	if v, ok := ggufMemo.Load(p); ok {
		if e := v.(ggufMemoEnt); e.key == k {
			return e.meta, e.err
		}
	}
	m, err := ReadGGUFMeta(p)
	ggufMemo.Store(p, ggufMemoEnt{k, m, err})
	return m, err
}
