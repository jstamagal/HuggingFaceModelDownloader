// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

// ListEntry represents a single repo in the list output.
type ListEntry struct {
	Type       string `json:"type"`
	Repo       string `json:"repo"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	Files      int    `json:"files"`
	Size       int64  `json:"size"`
	SizeHuman  string `json:"size_human"`
	Downloaded string `json:"downloaded"`
	Path       string `json:"path"`
}

func newListCmd(ro *RootOpts) *cobra.Command {
	var cacheDir string
	var filterType string
	var sortBy string
	var formatOut string
	var scan bool
	var manifestsOnly bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List downloaded models and datasets in the cache",
		Long: `List all models and datasets that have been downloaded to the HuggingFace cache.

By default, the Hub cache is scanned directly, including repositories downloaded
by hf, huggingface-cli, Python libraries, and hfdownloader.

Examples:
  hfdownloader list                     # List every repo in the Hub cache
  hfdownloader list --type model        # List only models
  hfdownloader list --type dataset      # List only datasets
  hfdownloader list --sort size         # Sort by size (largest first)
  hfdownloader list --format json       # Output as JSON`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Determine cache directory: CLI flag > config file > HF_HOME > default
			if cacheDir == "" {
				if cfg := loadConfigMap(); cfg != nil {
					if v, ok := cfg["cache-dir"].(string); ok && v != "" {
						cacheDir = v
					}
				}
			}
			if cacheDir == "" {
				cacheDir = hfdownloader.DefaultCacheDir()
			}

			var entries []ListEntry
			var err error

			if manifestsOnly && !scan {
				entries, err = scanManifests(cacheDir, filterType)
			} else {
				entries, err = scanCacheStructure(cacheDir, filterType)
			}
			if err != nil {
				return err
			}

			// Sort entries
			sortEntries(entries, sortBy)

			// Output
			if formatOut == "json" || ro.JSONOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}

			// Table output
			if len(entries) == 0 {
				fmt.Println("No downloaded repos found.")
				fmt.Printf("Cache directory: %s\n", cacheDir)
				return nil
			}

			printTable(entries)
			fmt.Printf("\nTotal: %d repos\n", len(entries))
			return nil
		},
	}

	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "HuggingFace cache directory (default: ~/.cache/huggingface or HF_HOME)")
	cmd.Flags().StringVar(&filterType, "type", "", "Filter by type: model, dataset, space")
	cmd.Flags().StringVar(&sortBy, "sort", "name", "Sort by: name, size, date")
	cmd.Flags().StringVar(&formatOut, "format", "table", "Output format: table, json")
	cmd.Flags().BoolVar(&scan, "scan", false, "Scan cache structure (retained for compatibility; now the default)")
	cmd.Flags().BoolVar(&manifestsOnly, "manifests-only", false, "Only list repositories with hfdownloader manifests")

	return cmd
}

// scanCacheStructure scans hub/ directory structure directly (for repos without manifests)
func scanCacheStructure(cacheDir, filterType string) ([]ListEntry, error) {
	cache := hfdownloader.NewHFCache(cacheDir, hfdownloader.DefaultStaleTimeout)
	inventory, err := cache.Scan()
	if err != nil {
		return nil, err
	}
	entries := make([]ListEntry, 0, len(inventory.Repos))
	for _, cached := range inventory.Repos {
		repoType := string(cached.Type)
		if filterType != "" && !strings.EqualFold(repoType, filterType) {
			continue
		}
		branch, commit := cachedRepoRevision(cached.Path)
		downloaded := ""
		if !cached.LastModified.IsZero() {
			downloaded = cached.LastModified.Format("2006-01-02")
		}
		entry := ListEntry{
			Type:       repoType,
			Repo:       cached.Repo,
			Branch:     branch,
			Commit:     shortCommit(commit),
			Files:      cached.FileCount,
			Size:       cached.Size,
			SizeHuman:  humanSize(cached.Size),
			Downloaded: downloaded,
			Path:       cached.Path,
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func cachedRepoRevision(repoPath string) (branch, commit string) {
	branch = "main"
	if data, err := os.ReadFile(filepath.Join(repoPath, "refs", branch)); err == nil {
		return branch, strings.TrimSpace(string(data))
	}
	snapshots, err := os.ReadDir(filepath.Join(repoPath, "snapshots"))
	if err == nil && len(snapshots) == 1 && snapshots[0].IsDir() {
		return branch, snapshots[0].Name()
	}
	return branch, ""
}

func scanManifests(cacheDir, filterType string) ([]ListEntry, error) {
	var entries []ListEntry

	// Scan both models/ and datasets/ directories
	dirs := []string{
		filepath.Join(cacheDir, "models"),
		filepath.Join(cacheDir, "datasets"),
	}

	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		// Walk looking for hfd.yaml files
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip errors
			}
			if info.IsDir() {
				return nil
			}
			if info.Name() != hfdownloader.ManifestFilename {
				return nil
			}

			manifest, err := hfdownloader.ReadManifest(path)
			if err != nil {
				return nil // Skip invalid manifests
			}

			// Filter by type if specified
			if filterType != "" && manifest.Type != filterType {
				return nil
			}

			entry := ListEntry{
				Type:       manifest.Type,
				Repo:       manifest.Repo,
				Branch:     manifest.Branch,
				Commit:     shortCommit(manifest.Commit),
				Files:      manifest.TotalFiles,
				Size:       manifest.TotalSize,
				SizeHuman:  humanSize(manifest.TotalSize),
				Downloaded: manifest.CompletedAt.Format("2006-01-02"),
				Path:       filepath.Dir(path),
			}
			entries = append(entries, entry)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return entries, nil
}

func sortEntries(entries []ListEntry, sortBy string) {
	switch strings.ToLower(sortBy) {
	case "size":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Size > entries[j].Size // Largest first
		})
	case "date":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Downloaded > entries[j].Downloaded // Newest first
		})
	default: // "name"
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Repo < entries[j].Repo
		})
	}
}

func printTable(entries []ListEntry) {
	// Calculate column widths
	maxRepo := 4 // "REPO"
	for _, e := range entries {
		if len(e.Repo) > maxRepo {
			maxRepo = len(e.Repo)
		}
	}
	if maxRepo > 50 {
		maxRepo = 50
	}

	// Header
	fmt.Printf("%-7s  %-*s  %-7s  %-10s  %5s  %10s  %s\n",
		"TYPE", maxRepo, "REPO", "COMMIT", "DOWNLOADED", "FILES", "SIZE", "BRANCH")
	fmt.Printf("%-7s  %-*s  %-7s  %-10s  %5s  %10s  %s\n",
		"-------", maxRepo, strings.Repeat("-", maxRepo), "-------", "----------", "-----", "----------", "------")

	// Rows
	for _, e := range entries {
		repo := e.Repo
		if len(repo) > maxRepo {
			repo = repo[:maxRepo-3] + "..."
		}
		fmt.Printf("%-7s  %-*s  %-7s  %-10s  %5d  %10s  %s\n",
			e.Type, maxRepo, repo, e.Commit, e.Downloaded, e.Files, e.SizeHuman, e.Branch)
	}
}

func shortCommit(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

func humanSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/GB)
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/MB)
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/KB)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
