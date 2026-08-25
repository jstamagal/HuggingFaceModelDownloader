// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

func TestCacheCmdJSON(t *testing.T) {
	t.Parallel()
	root := createCLICachedRepo(t, "models--acme--model", "some model data")
	cmd := newCacheCmd(&RootOpts{})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--cache-dir", root, "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var result struct {
		Repos []struct {
			Repo string `json:"repo"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, output.String())
	}
	if len(result.Repos) != 1 || result.Repos[0].Repo != "acme/model" {
		t.Fatalf("repos = %+v", result.Repos)
	}
}

func TestCacheDeleteCmdRequiresConfirmationWhenPiped(t *testing.T) {
	t.Parallel()
	root := createCLICachedRepo(t, "models--acme--model", "some model data")
	cmd := newCacheCmd(&RootOpts{})
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--cache-dir", root, "delete", "model:acme/model"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "hub", "models--acme--model")); statErr != nil {
		t.Fatalf("cache was deleted without confirmation: %v", statErr)
	}
}

func TestCacheDeleteCmd(t *testing.T) {
	t.Parallel()
	root := createCLICachedRepo(t, "datasets--acme--data", "dataset data")
	cmd := newCacheCmd(&RootOpts{})
	var output bytes.Buffer
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--cache-dir", root, "delete", "dataset:acme/data", "--yes"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(output.String(), "Reclaimed") {
		t.Fatalf("output = %q", output.String())
	}
	if _, err := os.Stat(filepath.Join(root, "hub", "datasets--acme--data")); !os.IsNotExist(err) {
		t.Fatalf("cache still exists: %v", err)
	}
}

func TestResolveCacheTargetsRejectsAmbiguousRepo(t *testing.T) {
	t.Parallel()
	repos := []hfdownloader.CachedRepo{
		{Repo: "acme/shared", Type: hfdownloader.RepoTypeModel},
		{Repo: "acme/shared", Type: hfdownloader.RepoTypeDataset},
	}
	if _, err := resolveCacheTargets(repos, []string{"acme/shared"}); err == nil {
		t.Fatal("resolveCacheTargets() error = nil")
	}
}

func TestFilteredCacheInventoryRecalculatesTotals(t *testing.T) {
	t.Parallel()
	inventory := &hfdownloader.CacheInventory{Root: "/cache", HubDir: "/cache/hub", TotalSize: 999}
	repos := []hfdownloader.CachedRepo{{Repo: "acme/model", Size: 10, FileCount: 2, IncompleteSize: 3}}
	got := filteredCacheInventory(inventory, repos)
	if got.TotalSize != 10 || got.TotalFiles != 2 || got.IncompleteSize != 3 {
		t.Fatalf("filtered totals = size:%d files:%d incomplete:%d", got.TotalSize, got.TotalFiles, got.IncompleteSize)
	}
}

func createCLICachedRepo(t *testing.T, name, contents string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "hub", name, "blobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blob"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}
