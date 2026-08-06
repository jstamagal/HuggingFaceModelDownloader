// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/bodaay/HuggingFaceModelDownloader/internal/tui"
	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
	"github.com/bodaay/HuggingFaceModelDownloader/pkg/smartdl"
)

func newSearchCmd(ctx context.Context, ro *RootOpts) *cobra.Command {
	var (
		author   string
		pipeline string
		library  string
		access   string
		sortBy   string
		limit    int
		endpoint string
	)

	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search and browse models on the Hugging Face Hub",
		Long: `Open an interactive model browser backed by Hugging Face Hub search.

Type a query, browse results with the arrow keys, and cycle task, library,
access, and sort filters from inside the TUI. Selecting a model analyzes it and
opens the existing smart download selector when the repository has variants.

Examples:
  hfdownloader search
  hfdownloader search llama
  hfdownloader search mistral --library transformers --sort downloads
  hfdownloader search --pipeline text-to-image --gated open
  hfdownloader search llama --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
				// Pasting an hf:// URI or hub URL into search should find
				// that repo, not fail the text match on "https://...".
				if hfdownloader.LooksLikeRepoURI(query) {
					if ref, err := hfdownloader.ParseRepoRef(query); err == nil {
						query = ref.Repo
					}
				}
			}
			gated, err := parseSearchAccess(access)
			if err != nil {
				return err
			}
			if !validSearchSort(sortBy) {
				return fmt.Errorf("invalid sort %q (use trending, downloads, likes, updated, or created)", sortBy)
			}
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("search limit must be between 1 and 1000")
			}

			token := strings.TrimSpace(ro.Token)
			if token == "" {
				token = strings.TrimSpace(os.Getenv("HF_TOKEN"))
			}
			opts := hfdownloader.ModelSearchOptions{
				Query: query, Author: author, PipelineTag: pipeline, Library: library,
				Gated: gated, Sort: sortBy, Limit: limit, Token: token, Endpoint: endpoint,
			}

			if ro.JSONOut {
				models, err := hfdownloader.SearchModels(ctx, opts)
				if err != nil {
					return err
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(models)
			}

			// Without a terminal (piped/scripted), print a plain listing
			// instead of trying to start the interactive browser.
			if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
				models, err := hfdownloader.SearchModels(ctx, opts)
				if err != nil {
					return err
				}
				for _, model := range models {
					task := model.PipelineTag
					if task == "" {
						task = "-"
					}
					fmt.Printf("%s\t%d downloads\t%d likes\t%s\n", model.ID, model.Downloads, model.Likes, task)
				}
				return nil
			}

			result, err := tui.RunModelSearch(ctx, opts)
			if err != nil {
				return err
			}
			if result.Cancelled || result.Selected == nil {
				return nil
			}
			return inspectSearchSelection(ctx, *result.Selected, token, endpoint, ro)
		},
	}

	cmd.Flags().StringVar(&author, "author", "", "Filter by model author or organization")
	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Filter by pipeline task (for example text-generation)")
	cmd.Flags().StringVar(&library, "library", "", "Filter by library (for example transformers or diffusers)")
	cmd.Flags().StringVar(&access, "gated", "all", "Access filter: all, open, gated")
	cmd.Flags().StringVar(&sortBy, "sort", "trending", "Sort: trending, downloads, likes, updated, created")
	cmd.Flags().IntVar(&limit, "limit", 50, "Maximum results per search (1-1000)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Custom Hugging Face endpoint URL")
	return cmd
}

func inspectSearchSelection(ctx context.Context, selected hfdownloader.ModelSearchResult, token, endpoint string, ro *RootOpts) error {
	fmt.Printf("Analyzing %s…\n", selected.ID)
	analyzer := smartdl.NewAnalyzer(smartdl.AnalyzerOptions{Token: token, Endpoint: endpoint})
	info, err := analyzer.AnalyzeWithRevision(ctx, selected.ID, false, "main")
	if err != nil {
		return fmt.Errorf("analysis failed: %w", err)
	}

	if len(info.Refs) > 1 {
		branchResult, err := tui.RunBranchPicker(selected.ID, info.Refs)
		if err != nil {
			return fmt.Errorf("branch picker failed: %w", err)
		}
		if branchResult.Cancelled {
			return nil
		}
		if branchResult.Selected != "" && branchResult.Selected != info.Branch {
			fmt.Printf("Analyzing %s (%s)…\n", selected.ID, branchResult.Selected)
			info, err = analyzer.AnalyzeWithRevision(ctx, selected.ID, false, branchResult.Selected)
			if err != nil {
				return fmt.Errorf("analysis failed: %w", err)
			}
		}
	}
	return runInteractiveSelector(ctx, info, ro, "")
}

func parseSearchAccess(value string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all", "any":
		return nil, nil
	case "open", "ungated", "false":
		value := false
		return &value, nil
	case "gated", "true":
		value := true
		return &value, nil
	default:
		return nil, fmt.Errorf("invalid gated filter %q (use all, open, or gated)", value)
	}
}

func validSearchSort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "trending", "downloads", "likes", "updated", "created":
		return true
	default:
		return false
	}
}
