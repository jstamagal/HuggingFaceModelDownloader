// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package hfdownloader

import (
	"context"
	"errors"
)

// Mount needs Linux FUSE; views (symlinks) work everywhere.
func (c *HFCache) Mount(ctx context.Context, opts MountOptions) error {
	return errors.New("mount is only supported on Linux; use `hfdownloader views`")
}
