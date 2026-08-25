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
	"regexp"
	"sort"
	"strings"
	"time"
)

// CachedRepo describes the disk space occupied by one Hub repository cache.
// Size counts physical files in blobs/ plus any regular files stored directly
// in snapshots/. Snapshot symlinks are not double-counted.
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

// CachedArtifact is one independently removable model payload inside a Hub
// repository. A single-file GGUF is one artifact; numbered GGUF or
// safetensors shards are grouped into one artifact.
type CachedArtifact struct {
	ID           string               `json:"id"`
	Repo         string               `json:"repo"`
	Type         RepoType             `json:"type"`
	Commit       string               `json:"commit"`
	Name         string               `json:"name"`
	Size         int64                `json:"size"`
	FileCount    int                  `json:"file_count"`
	LastModified time.Time            `json:"last_modified,omitempty"`
	Files        []CachedArtifactFile `json:"files"`
}

// CachedArtifactFile identifies one snapshot file and its physical blob.
type CachedArtifactFile struct {
	Path     string `json:"path"`
	BlobPath string `json:"blob_path"`
	Size     int64  `json:"size"`
}

type artifactDeleteTarget struct {
	snapshot string
	blob     string
	size     int64
	isBlob   bool
}

// CacheInventory is a point-in-time summary of the repositories in a cache.
type CacheInventory struct {
	Root           string           `json:"root"`
	HubDir         string           `json:"hub_dir"`
	Repos          []CachedRepo     `json:"repos"`
	Artifacts      []CachedArtifact `json:"artifacts,omitempty"`
	TotalSize      int64            `json:"total_size"`
	IncompleteSize int64            `json:"incomplete_size"`
	TotalFiles     int              `json:"total_files"`
}

// CacheDeleteResult reports what was removed by DeleteCachedRepo.
type CacheDeleteResult struct {
	Repo         string   `json:"repo"`
	Type         RepoType `json:"type"`
	Artifact     string   `json:"artifact,omitempty"`
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
		result.Artifacts = append(result.Artifacts, scanCachedArtifacts(rd)...)
		result.TotalSize += cached.Size
		result.IncompleteSize += cached.IncompleteSize
		result.TotalFiles += cached.FileCount
	}
	return result, nil
}

var artifactShardPattern = regexp.MustCompile(`(?i)^(.*?)-[0-9]{5}-of-[0-9]{5}(\.[^.]+)$`)

var artifactExtensions = map[string]bool{
	".gguf": true, ".ggml": true, ".safetensors": true, ".bin": true,
	".onnx": true, ".pt": true, ".pth": true, ".ckpt": true,
	".h5": true, ".msgpack": true,
}

func scanCachedArtifacts(rd *RepoDir) []CachedArtifact {
	commits, err := rd.ListSnapshots()
	if err != nil {
		return nil
	}
	sort.Strings(commits)
	var artifacts []CachedArtifact
	for _, commit := range commits {
		groups := make(map[string]*CachedArtifact)
		root := rd.SnapshotDir(commit)
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if unsafeRepoPath(rel) || !artifactExtensions[strings.ToLower(filepath.Ext(rel))] {
				return nil
			}
			name := artifactGroupName(rel)
			artifact := groups[name]
			if artifact == nil {
				artifact = &CachedArtifact{Repo: rd.RepoID(), Type: rd.Type(), Commit: commit, Name: name}
				artifact.ID = string(artifact.Type) + ":" + artifact.Repo + "@" + commit + ":" + name
				groups[name] = artifact
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil
			}
			artifact.Files = append(artifact.Files, CachedArtifactFile{
				Path: rel, BlobPath: resolved, Size: info.Size(),
			})
			artifact.Size += info.Size()
			artifact.FileCount++
			if info.ModTime().After(artifact.LastModified) {
				artifact.LastModified = info.ModTime()
			}
			return nil
		})
		names := make([]string, 0, len(groups))
		for name := range groups {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			artifacts = append(artifacts, *groups[name])
		}
	}
	return artifacts
}

func artifactGroupName(rel string) string {
	dir, base := filepath.Split(rel)
	if match := artifactShardPattern.FindStringSubmatch(base); match != nil {
		return filepath.ToSlash(filepath.Join(dir, match[1]+match[2]))
	}
	return rel
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

	for _, root := range []string{rd.BlobsDir(), rd.SnapshotsDir()} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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

// DeleteCachedArtifact removes only the selected model payload. Other
// quantizations and repository metadata remain intact. Physical blobs are
// removed only when no snapshot still references them.
func (c *HFCache) DeleteCachedArtifact(artifact CachedArtifact, force bool) (*CacheDeleteResult, error) {
	if err := validateRepoID(artifact.Repo); err != nil {
		return nil, err
	}
	if !validRepoComponent(artifact.Commit) || artifact.Name == "" || len(artifact.Files) == 0 {
		return nil, fmt.Errorf("invalid cached artifact %q", artifact.Name)
	}
	rd, err := c.Repo(artifact.Repo, artifact.Type)
	if err != nil {
		return nil, err
	}
	if scanCachedRepo(rd, c.StaleTimeout).Active && !force {
		return nil, fmt.Errorf("refusing to delete %s: a download is active (use force to override)", artifact.Repo)
	}

	snapshotRoot := rd.SnapshotDir(artifact.Commit)
	targets := make([]artifactDeleteTarget, 0, len(artifact.Files))
	for _, file := range artifact.Files {
		if unsafeRepoPath(file.Path) || artifactGroupName(file.Path) != artifact.Name {
			return nil, fmt.Errorf("invalid artifact file path %q", file.Path)
		}
		snapshotPath := filepath.Join(snapshotRoot, filepath.FromSlash(file.Path))
		if !pathWithin(snapshotRoot, snapshotPath) {
			return nil, fmt.Errorf("artifact path escapes snapshot: %q", file.Path)
		}
		linkInfo, err := os.Lstat(snapshotPath)
		if err != nil {
			return nil, fmt.Errorf("inspect artifact file %q: %w", file.Path, err)
		}
		resolved, err := filepath.EvalSymlinks(snapshotPath)
		if err != nil {
			return nil, fmt.Errorf("resolve artifact file %q: %w", file.Path, err)
		}
		info, err := os.Stat(snapshotPath)
		if err != nil {
			return nil, fmt.Errorf("stat artifact file %q: %w", file.Path, err)
		}
		isBlob := pathWithin(rd.BlobsDir(), resolved)
		if linkInfo.Mode()&os.ModeSymlink != 0 && !isBlob {
			return nil, fmt.Errorf("refusing artifact symlink outside blobs: %q", snapshotPath)
		}
		targets = append(targets, artifactDeleteTarget{snapshot: snapshotPath, blob: resolved, size: info.Size(), isBlob: isBlob})
	}

	removeFriendlyArtifactLinks(rd.FriendlyPath(), targets)
	for _, target := range targets {
		if err := os.Remove(target.snapshot); err != nil {
			return nil, fmt.Errorf("remove snapshot artifact: %w", err)
		}
		pruneEmptyParents(filepath.Dir(target.snapshot), snapshotRoot)
	}

	var removed int64
	seenBlobs := make(map[string]bool)
	for _, target := range targets {
		if !target.isBlob {
			removed += target.size
			continue
		}
		if seenBlobs[target.blob] {
			continue
		}
		seenBlobs[target.blob] = true
		if snapshotReferencesBlob(rd.SnapshotsDir(), target.blob) {
			continue
		}
		if err := os.Remove(target.blob); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("remove unreferenced blob: %w", err)
		}
		removed += target.size
	}
	// A partial cleanup makes the old manifest inaccurate. Cache inspection
	// works directly from the Hub layout, so discard stale generated metadata.
	_ = os.Remove(filepath.Join(rd.FriendlyPath(), ManifestFilename))
	return &CacheDeleteResult{
		Repo: artifact.Repo, Type: artifact.Type, Artifact: artifact.Name, BytesRemoved: removed,
	}, nil
}

func pathWithin(parent, target string) bool {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absParent, absTarget)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func snapshotReferencesBlob(snapshotsDir, blob string) bool {
	found := false
	_ = filepath.WalkDir(snapshotsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil && resolved == blob {
			found = true
		}
		return nil
	})
	return found
}

func removeFriendlyArtifactLinks(friendlyRoot string, targets []artifactDeleteTarget) {
	_ = filepath.WalkDir(friendlyRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		for _, target := range targets {
			if resolved == target.snapshot || resolved == target.blob {
				_ = os.Remove(path)
				pruneEmptyParents(filepath.Dir(path), friendlyRoot)
				break
			}
		}
		return nil
	})
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
