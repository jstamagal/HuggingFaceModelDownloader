// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bodaay/HuggingFaceModelDownloader/internal/tui"
	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

func newCacheCmd(ro *RootOpts) *cobra.Command {
	var cacheDir string
	var format string
	var filterType string
	var search string
	var sortBy string

	cmd := &cobra.Command{
		Use:     "cache",
		Aliases: []string{"clean"},
		Short:   "Browse downloaded repositories and reclaim disk space",
		Long: `Browse everything in the Hugging Face Hub cache, including models,
datasets, and Spaces. In a terminal this opens an interactive cleanup browser.
Piped output falls back to a deterministic table.`,
		Example: `  hfdownloader cache
  hfdownloader cache --type model --sort size
  hfdownloader cache --format json
  hfdownloader cache delete model:owner/repo --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "table" && format != "json" {
				return fmt.Errorf("invalid --format %q (use table or json)", format)
			}
			root := resolveCacheDir(cacheDir)
			cache := hfdownloader.NewHFCache(root, 0)
			outputFlagsChanged := cmd.Flags().Changed("format") || cmd.Flags().Changed("type") ||
				cmd.Flags().Changed("search") || cmd.Flags().Changed("sort")
			if !ro.JSONOut && !outputFlagsChanged && terminalIO(cmd) {
				result, err := tui.RunCacheBrowser(cache)
				if err != nil {
					return err
				}
				if result.Removed > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "Removed %d cache items; reclaimed %s.\n", result.Removed, humanSize(result.Bytes))
				}
				return nil
			}

			inventory, err := cache.Scan()
			if err != nil {
				return err
			}
			repos, err := filterCachedRepos(inventory.Repos, filterType, search, sortBy)
			if err != nil {
				return err
			}
			filtered := filteredCacheInventory(inventory, repos)
			if ro.JSONOut || format == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(filtered)
			}
			printCacheTable(cmd.OutOrStdout(), &filtered)
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&cacheDir, "cache-dir", "", "Hugging Face cache directory (default: ~/.cache/huggingface or HF_HOME)")
	cmd.Flags().StringVar(&format, "format", "table", "Output format for non-interactive use: table or json")
	cmd.Flags().StringVar(&filterType, "type", "", "Filter by type: model, dataset, or space")
	cmd.Flags().StringVar(&search, "search", "", "Filter repository names")
	cmd.Flags().StringVar(&sortBy, "sort", "size", "Sort by size, name, or recent")
	cmd.AddCommand(newCacheDeleteCmd(ro, &cacheDir))
	return cmd
}

func filteredCacheInventory(inventory *hfdownloader.CacheInventory, repos []hfdownloader.CachedRepo) hfdownloader.CacheInventory {
	result := hfdownloader.CacheInventory{
		Root:   inventory.Root,
		HubDir: inventory.HubDir,
		Repos:  repos,
	}
	for _, repo := range repos {
		result.TotalSize += repo.Size
		result.IncompleteSize += repo.IncompleteSize
		result.TotalFiles += repo.FileCount
	}
	allowed := make(map[string]bool, len(repos))
	for _, repo := range repos {
		allowed[string(repo.Type)+":"+repo.Repo] = true
	}
	for _, artifact := range inventory.Artifacts {
		if allowed[string(artifact.Type)+":"+artifact.Repo] {
			result.Artifacts = append(result.Artifacts, artifact)
		}
	}
	return result
}

func newCacheDeleteCmd(ro *RootOpts, inheritedCacheDir *string) *cobra.Command {
	var yes bool
	var force bool
	cmd := &cobra.Command{
		Use:   "delete [TYPE:]OWNER/NAME...",
		Short: "Remove repositories from the Hugging Face cache",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache := hfdownloader.NewHFCache(resolveCacheDir(*inheritedCacheDir), 0)
			inventory, err := cache.Scan()
			if err != nil {
				return err
			}
			targets, err := resolveCacheTargets(inventory.Repos, args)
			if err != nil {
				return err
			}
			var total int64
			for _, target := range targets {
				total += target.Size
			}
			if !yes {
				if !terminalIO(cmd) {
					return errors.New("refusing to delete without confirmation; pass --yes")
				}
				ok, err := confirmCacheDelete(cmd, len(targets), total)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "Cleanup cancelled.")
					return nil
				}
			}

			results := make([]hfdownloader.CacheDeleteResult, 0, len(targets))
			for _, target := range targets {
				result, err := cache.DeleteCachedRepo(target.Repo, target.Type, force)
				if err != nil {
					return err
				}
				results = append(results, *result)
			}
			if ro.JSONOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
			}
			var removed int64
			for _, result := range results {
				removed += result.BytesRemoved
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s:%s (%s)\n", result.Type, result.Repo, humanSize(result.BytesRemoved))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Reclaimed %s.\n", humanSize(removed))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Delete without an interactive confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "Allow deletion even when an active download is detected")
	return cmd
}

func resolveCacheDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if cfg := loadConfigMap(); cfg != nil {
		if value, ok := cfg["cache-dir"].(string); ok && value != "" {
			return value
		}
	}
	return hfdownloader.DefaultCacheDir()
}

func terminalIO(cmd *cobra.Command) bool {
	in, inOK := cmd.InOrStdin().(interface{ Fd() uintptr })
	out, outOK := cmd.OutOrStdout().(interface{ Fd() uintptr })
	return inOK && outOK && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func filterCachedRepos(repos []hfdownloader.CachedRepo, filterType, search, sortBy string) ([]hfdownloader.CachedRepo, error) {
	filterType = strings.ToLower(strings.TrimSpace(filterType))
	if filterType != "" && filterType != "model" && filterType != "dataset" && filterType != "space" {
		return nil, fmt.Errorf("invalid --type %q (use model, dataset, or space)", filterType)
	}
	query := strings.ToLower(strings.TrimSpace(search))
	filtered := make([]hfdownloader.CachedRepo, 0, len(repos))
	for _, repo := range repos {
		if filterType != "" && string(repo.Type) != filterType {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(repo.Repo), query) {
			continue
		}
		filtered = append(filtered, repo)
	}
	switch strings.ToLower(sortBy) {
	case "size":
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].Size > filtered[j].Size })
	case "name":
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].Repo < filtered[j].Repo })
	case "recent":
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].LastModified.After(filtered[j].LastModified) })
	default:
		return nil, fmt.Errorf("invalid --sort %q (use size, name, or recent)", sortBy)
	}
	return filtered, nil
}

func resolveCacheTargets(repos []hfdownloader.CachedRepo, args []string) ([]hfdownloader.CachedRepo, error) {
	byKey := make(map[string]hfdownloader.CachedRepo, len(repos))
	byRepo := make(map[string][]hfdownloader.CachedRepo, len(repos))
	for _, repo := range repos {
		byKey[string(repo.Type)+":"+repo.Repo] = repo
		byRepo[repo.Repo] = append(byRepo[repo.Repo], repo)
	}
	seen := make(map[string]bool)
	targets := make([]hfdownloader.CachedRepo, 0, len(args))
	for _, arg := range args {
		var matches []hfdownloader.CachedRepo
		if repoType, repoID, found := strings.Cut(arg, ":"); found {
			if target, ok := byKey[strings.ToLower(repoType)+":"+repoID]; ok {
				matches = []hfdownloader.CachedRepo{target}
			}
		} else {
			matches = byRepo[arg]
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("cached repository %q not found", arg)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("cached repository %q has multiple types; use model:%s, dataset:%s, or space:%s", arg, arg, arg, arg)
		}
		key := string(matches[0].Type) + ":" + matches[0].Repo
		if !seen[key] {
			seen[key] = true
			targets = append(targets, matches[0])
		}
	}
	return targets, nil
}

func confirmCacheDelete(cmd *cobra.Command, count int, size int64) (bool, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "Delete %d cached repositories and reclaim about %s? [y/N] ", count, humanSize(size))
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func printCacheTable(w io.Writer, inventory *hfdownloader.CacheInventory) {
	if len(inventory.Repos) == 0 {
		fmt.Fprintln(w, "No cached repositories found.")
		fmt.Fprintf(w, "Cache directory: %s\n", inventory.Root)
		return
	}
	artifacts := make(map[string][]hfdownloader.CachedArtifact)
	for _, artifact := range inventory.Artifacts {
		key := string(artifact.Type) + ":" + artifact.Repo
		artifacts[key] = append(artifacts[key], artifact)
	}
	fmt.Fprintf(w, "%-8s  %10s  %8s  %-10s  %s\n", "TYPE", "SIZE", "FILES", "MODIFIED", "REPOSITORY / ARTIFACT")
	fmt.Fprintf(w, "%-8s  %10s  %8s  %-10s  %s\n", "--------", "----------", "--------", "----------", "----------")
	var total int64
	for _, repo := range inventory.Repos {
		lastUsed := "-"
		if !repo.LastModified.IsZero() {
			lastUsed = repo.LastModified.Format("2006-01-02")
		}
		fmt.Fprintf(w, "%-8s  %10s  %8d  %-10s  [-] %s\n", repo.Type, humanSize(repo.Size), repo.FileCount, lastUsed, repo.Repo)
		for _, artifact := range artifacts[string(repo.Type)+":"+repo.Repo] {
			name := artifact.Name
			if artifact.FileCount > 1 {
				name += fmt.Sprintf(" (%d shards)", artifact.FileCount)
			}
			fmt.Fprintf(w, "%-8s  %10s  %8d  %-10s      |- %s\n", "artifact", humanSize(artifact.Size), artifact.FileCount, "", name)
		}
		total += repo.Size
	}
	fmt.Fprintf(w, "\n%d repositories, %d artifacts, %s total\n", len(inventory.Repos), len(inventory.Artifacts), humanSize(total))
	fmt.Fprintf(w, "Cache directory: %s\n", inventory.Root)
}
