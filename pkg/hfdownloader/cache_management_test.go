// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"encoding/json"
	"os"
	"path/filepath"
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
