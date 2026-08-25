// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHFCache_Scan(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)

	createCachedRepo(t, root, "models--acme--model", "model-data")
	createCachedRepo(t, root, "datasets--acme--data", "dataset-data")
	createCachedRepo(t, root, "spaces--acme--demo", "space-data")
	if err := os.MkdirAll(filepath.Join(root, "hub", "version.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := cache.Scan()
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(got.Repos) != 3 {
		t.Fatalf("Scan() returned %d repos, want 3", len(got.Repos))
	}
	if got.TotalSize != int64(len("model-data")+len("dataset-data")+len("space-data")) {
		t.Errorf("TotalSize = %d", got.TotalSize)
	}
	if got.Repos[2].Type != RepoTypeSpace {
		t.Errorf("space type = %q", got.Repos[2].Type)
	}
}

func TestHFCache_ScanGroupsArtifactsWithinRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)
	repo := filepath.Join(root, "hub", "models--acme--many")
	createSnapshotArtifact(t, repo, "abc123", "model-Q4_K_M.gguf", "q4")
	createSnapshotArtifact(t, repo, "abc123", "model-Q8_0.gguf", "q8")
	createSnapshotArtifact(t, repo, "abc123", "Q6/model-00001-of-00002.gguf", "part1")
	createSnapshotArtifact(t, repo, "abc123", "Q6/model-00002-of-00002.gguf", "part2")

	inventory, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repos) != 1 {
		t.Fatalf("repos = %d", len(inventory.Repos))
	}
	if len(inventory.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v", inventory.Artifacts)
	}
	var sharded *CachedArtifact
	for i := range inventory.Artifacts {
		if inventory.Artifacts[i].Name == "Q6/model.gguf" {
			sharded = &inventory.Artifacts[i]
		}
	}
	if sharded == nil || sharded.FileCount != 2 || sharded.Size != int64(len("part1")+len("part2")) {
		t.Fatalf("sharded artifact = %+v", sharded)
	}
}

func TestHFCache_ScanCountsRegularFilesStoredInSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)
	path := filepath.Join(root, "hub", "models--acme--direct", "snapshots", "abc123", "model-Q4_0.gguf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("direct-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	inventory, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Repos) != 1 || inventory.Repos[0].Size != int64(len("direct-data")) {
		t.Fatalf("repos = %+v", inventory.Repos)
	}
	if len(inventory.Artifacts) != 1 || inventory.Artifacts[0].Size != inventory.Repos[0].Size {
		t.Fatalf("artifacts = %+v", inventory.Artifacts)
	}
}

func TestHFCache_DeleteCachedArtifactLeavesSiblingQuant(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)
	repo := filepath.Join(root, "hub", "models--acme--many")
	createSnapshotArtifact(t, repo, "abc123", "model-Q4_K_M.gguf", "q4-data")
	createSnapshotArtifact(t, repo, "abc123", "model-Q8_0.gguf", "q8-data")

	inventory, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var q4 CachedArtifact
	for _, artifact := range inventory.Artifacts {
		if artifact.Name == "model-Q4_K_M.gguf" {
			q4 = artifact
		}
	}
	result, err := cache.DeleteCachedArtifact(q4, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesRemoved != int64(len("q4-data")) {
		t.Fatalf("removed = %d", result.BytesRemoved)
	}
	after, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Repos) != 1 || len(after.Artifacts) != 1 || after.Artifacts[0].Name != "model-Q8_0.gguf" {
		t.Fatalf("after delete = repos:%+v artifacts:%+v", after.Repos, after.Artifacts)
	}
}

func TestHFCache_DeleteCachedArtifactKeepsBlobUsedByAnotherSnapshot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)
	repo := filepath.Join(root, "hub", "models--acme--shared")
	blob := filepath.Join(repo, "blobs", "shared-blob")
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("shared-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, commit := range []string{"abc123", "def456"} {
		snapshot := filepath.Join(repo, "snapshots", commit, "model-Q4_K_M.gguf")
		if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
			t.Fatal(err)
		}
		target, err := filepath.Rel(filepath.Dir(snapshot), blob)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, snapshot); err != nil {
			t.Fatal(err)
		}
	}

	inventory, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", inventory.Artifacts)
	}
	result, err := cache.DeleteCachedArtifact(inventory.Artifacts[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if result.BytesRemoved != 0 {
		t.Fatalf("removed shared bytes = %d, want 0", result.BytesRemoved)
	}
	if _, err := os.Stat(blob); err != nil {
		t.Fatalf("shared blob was removed: %v", err)
	}
	after, err := cache.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Artifacts) != 1 {
		t.Fatalf("remaining artifacts = %+v", after.Artifacts)
	}
}

func TestHFCache_DeleteCachedRepo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Minute)
	createCachedRepo(t, root, "models--acme--model", "model-data")

	friendly := filepath.Join(root, "models", "acme", "model")
	if err := os.MkdirAll(friendly, 0o755); err != nil {
		t.Fatal(err)
	}
	locks := filepath.Join(root, "hub", ".locks", "models--acme--model")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := cache.DeleteCachedRepo("acme/model", RepoTypeModel, false)
	if err != nil {
		t.Fatalf("DeleteCachedRepo() error = %v", err)
	}
	if got.BytesRemoved != int64(len("model-data")) {
		t.Errorf("BytesRemoved = %d", got.BytesRemoved)
	}
	for _, path := range []string{
		filepath.Join(root, "hub", "models--acme--model"), friendly, locks,
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("path still exists after deletion: %s", path)
		}
	}
}

func TestHFCache_DeleteCachedRepoRejectsUnsafeTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		repo string
	}{
		{name: "traversal", repo: "../outside"},
		{name: "extra component", repo: "one/two/three"},
		{name: "backslash", repo: `one\two/name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := NewHFCache(t.TempDir(), time.Minute)
			if _, err := cache.DeleteCachedRepo(tt.repo, RepoTypeModel, false); err == nil {
				t.Fatal("DeleteCachedRepo() error = nil")
			}
		})
	}
}

func TestHFCache_DeleteCachedRepoProtectsActiveDownload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := NewHFCache(root, time.Hour)
	dir := filepath.Join(root, "hub", "models--acme--active", "blobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	incomplete := filepath.Join(dir, "blob.incomplete")
	if err := os.WriteFile(incomplete, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(IncompleteMeta{PID: os.Getpid(), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(incomplete+".meta", meta, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := cache.DeleteCachedRepo("acme/active", RepoTypeModel, false); err == nil {
		t.Fatal("DeleteCachedRepo() allowed deletion of active download")
	}
	if _, err := os.Stat(filepath.Dir(dir)); err != nil {
		t.Fatalf("active cache was removed: %v", err)
	}
	if _, err := cache.DeleteCachedRepo("acme/active", RepoTypeModel, true); err != nil {
		t.Fatalf("forced DeleteCachedRepo() error = %v", err)
	}
}

func TestRemoveContainedDirectoryRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := removeContainedDirectory(root, link); err == nil {
		t.Fatal("removeContainedDirectory() error = nil")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target was affected: %v", err)
	}
}

func TestRemoveContainedDirectoryRejectsIntermediateSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "owner")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := removeContainedDirectory(root, filepath.Join(link, "child")); err == nil {
		t.Fatal("removeContainedDirectory() error = nil")
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); err != nil {
		t.Fatalf("outside target was affected: %v", err)
	}
}

func createCachedRepo(t *testing.T, root, dirName, contents string) {
	t.Helper()
	dir := filepath.Join(root, "hub", dirName)
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "snapshots", "abc123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blobs", "blob"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createSnapshotArtifact(t *testing.T, repo, commit, rel, contents string) {
	t.Helper()
	blobName := strings.NewReplacer("/", "-", ".", "-").Replace(rel)
	blob := filepath.Join(repo, "blobs", blobName)
	snapshot := filepath.Join(repo, "snapshots", commit, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(blob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Rel(filepath.Dir(snapshot), blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, snapshot); err != nil {
		t.Fatal(err)
	}
}
