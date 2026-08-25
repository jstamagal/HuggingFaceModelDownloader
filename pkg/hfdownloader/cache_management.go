// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CachedRepo describes the disk space occupied by one Hub repository cache.
// Size is physical data in blobs/, so snapshot symlinks are not double-counted.
type CachedRepo struct {
	Repo            string    `json:"repo"`
	Type            RepoType  `json:"type"`
	Path            string    `json:"path"`
	FriendlyPath    string    `json:"friendly_path,omitempty"`
	Size            int64     `json:"size"`
	FileCount       int       `json:"file_count"`
	IncompleteSize  int64     `json:"incomplete_size,omitempty"`
	IncompleteFiles int       `json:"incomplete_files,omitempty"`
	SnapshotCount   int       `json:"snapshot_count"`
	LastModified    time.Time `json:"last_modified,omitempty"`
	Active          bool      `json:"active,omitempty"`
	ScanError       string    `json:"scan_error,omitempty"`
}

// CacheInventory is a point-in-time summary of the repositories in a cache.
type CacheInventory struct {
	Root           string       `json:"root"`
	HubDir         string       `json:"hub_dir"`
	Repos          []CachedRepo `json:"repos"`
	TotalSize      int64        `json:"total_size"`
	IncompleteSize int64        `json:"incomplete_size"`
	TotalFiles     int          `json:"total_files"`
}

// CacheDeleteResult reports what was removed by DeleteCachedRepo.
type CacheDeleteResult struct {
	Repo         string   `json:"repo"`
	Type         RepoType `json:"type"`
	BytesRemoved int64    `json:"bytes_removed"`
}

// Scan inventories model, dataset, and Space repositories in the Hub cache.
func (c *HFCache) Scan() (*CacheInventory, error) {
	result := &CacheInventory{Root: c.Root, HubDir: c.HubDir()}
	entries, err := os.ReadDir(result.HubDir)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hub cache: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		repoType, owner, name, ok := parseRepoDirName(entry.Name())
		if !ok {
			continue
		}
		rd, err := c.Repo(owner+"/"+name, repoType)
		if err != nil {
			continue
		}
		cached := scanCachedRepo(rd, c.StaleTimeout)
		result.Repos = append(result.Repos, cached)
		result.TotalSize += cached.Size
		result.IncompleteSize += cached.IncompleteSize
		result.TotalFiles += cached.FileCount
	}
	return result, nil
}

func scanCachedRepo(rd *RepoDir, staleTimeout time.Duration) CachedRepo {
	result := CachedRepo{
		Repo:         rd.RepoID(),
		Type:         rd.Type(),
		Path:         rd.Path(),
		FriendlyPath: rd.FriendlyPath(),
	}

	if snapshots, err := rd.ListSnapshots(); err == nil {
		result.SnapshotCount = len(snapshots)
	}

	err := filepath.WalkDir(rd.BlobsDir(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.HasSuffix(path, ".meta") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		result.Size += info.Size()
		result.FileCount++
		if info.ModTime().After(result.LastModified) {
			result.LastModified = info.ModTime()
		}
		if strings.HasSuffix(path, ".incomplete") {
			result.IncompleteSize += info.Size()
			result.IncompleteFiles++
			if incompleteDownloadActive(path, info, staleTimeout) {
				result.Active = true
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		result.ScanError = err.Error()
	}
	return result
}

func incompleteDownloadActive(path string, info fs.FileInfo, staleTimeout time.Duration) bool {
	if time.Since(info.ModTime()) >= staleTimeout {
		return false
	}
	data, err := os.ReadFile(path + ".meta")
	if err != nil {
		return false
	}
	var meta IncompleteMeta
	return json.Unmarshal(data, &meta) == nil && isProcessAlive(meta.PID)
}

// DeleteCachedRepo safely removes one repository, its friendly view, and its
// lock directory. Active downloads are refused unless force is true.
func (c *HFCache) DeleteCachedRepo(repoID string, repoType RepoType, force bool) (*CacheDeleteResult, error) {
	if err := validateRepoID(repoID); err != nil {
		return nil, err
	}
	rd, err := c.Repo(repoID, repoType)
	if err != nil {
		return nil, err
	}

	pathInfo, err := os.Lstat(rd.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("repository %s (%s) is not in the cache", repoID, repoType)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect repository cache: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.IsDir() {
		return nil, fmt.Errorf("refusing to delete non-directory or symlink %q", rd.Path())
	}

	info := scanCachedRepo(rd, c.StaleTimeout)
	if info.Active && !force {
		return nil, fmt.Errorf("refusing to delete %s: a download is active (use force to override)", repoID)
	}
	if err := removeContainedDirectory(c.HubDir(), rd.Path()); err != nil {
		return nil, fmt.Errorf("delete hub cache: %w", err)
	}
	if friendly := rd.FriendlyPath(); friendly != "" {
		if err := removeContainedDirectory(c.Root, friendly); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("delete friendly view: %w", err)
		}
		pruneEmptyParents(filepath.Dir(friendly), c.Root)
	}

	lockPath := filepath.Join(c.HubDir(), ".locks", rd.dirName())
	if err := removeContainedDirectory(c.HubDir(), lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("delete cache locks: %w", err)
	}
	return &CacheDeleteResult{Repo: repoID, Type: repoType, BytesRemoved: info.Size}, nil
}

func validateRepoID(repoID string) error {
	if strings.Contains(repoID, "\\") || strings.Contains(repoID, "..") || strings.Contains(repoID, "//") {
		return fmt.Errorf("invalid repository ID %q", repoID)
	}
	parts := strings.Split(repoID, "/")
	if len(parts) != 2 || !validRepoComponent(parts[0]) || !validRepoComponent(parts[1]) {
		return fmt.Errorf("invalid repository ID %q (expected owner/name)", repoID)
	}
	return nil
}

func validRepoComponent(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func removeContainedDirectory(parent, target string) error {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absParent, absTarget)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q is outside %q", absTarget, absParent)
	}
	info, err := os.Lstat(absTarget)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing to remove non-directory or symlink %q", absTarget)
	}
	// Resolve both sides to catch a symlink in any intermediate component
	// (for example cache/models/owner -> /outside).
	realParent, err := filepath.EvalSymlinks(absParent)
	if err != nil {
		return fmt.Errorf("resolve parent directory: %w", err)
	}
	realTarget, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		return fmt.Errorf("resolve target directory: %w", err)
	}
	realRel, err := filepath.Rel(realParent, realTarget)
	if err != nil || realRel == "." || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resolved path %q is outside %q", realTarget, realParent)
	}
	return os.RemoveAll(absTarget)
}

func pruneEmptyParents(dir, stop string) {
	absStop, err := filepath.Abs(stop)
	if err != nil {
		return
	}
	for {
		absDir, err := filepath.Abs(dir)
		if err != nil || absDir == absStop {
			return
		}
		rel, err := filepath.Rel(absStop, absDir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		if err := os.Remove(absDir); err != nil {
			return
		}
		dir = filepath.Dir(absDir)
	}
}
