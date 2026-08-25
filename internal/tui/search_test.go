// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
	"github.com/bodaay/HuggingFaceModelDownloader/pkg/smartdl"
)

func TestModelSearchViewFillsTerminal(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "llama"}, nil)
	m.width, m.height = 110, 28
	m.results = sampleSearchResults(20)
	view := m.View().Content
	plainView := ansi.Strip(view)
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("height = %d, want %d", got, m.height)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got != m.width {
			t.Fatalf("line %d width = %d, want %d", i, got, m.width)
		}
	}
	for _, want := range []string{"HF MODEL EXPLORER", "Models", "owner/model-00", "Selected", "s:sort"} {
		if !strings.Contains(plainView, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if strings.Contains(plainView, "\n│ |likes:") {
		t.Fatal("result statistics wrapped onto a second row")
	}
}

func TestModelSearchViewUsesNarrowLayout(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "qwen"}, nil)
	m.width, m.height = 72, 24
	m.results = sampleSearchResults(8)
	view := m.View().Content
	plainView := ansi.Strip(view)
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("height = %d, want %d", got, m.height)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got != m.width {
			t.Fatalf("line %d width = %d, want %d", i, got, m.width)
		}
	}
	if !strings.Contains(plainView, "Selected") {
		t.Fatal("narrow layout omitted detail panel")
	}
}

func TestModelSearchNavigationAndFilters(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "llama", Sort: "trending"},
		func(context.Context, hfdownloader.ModelSearchOptions, string) (*hfdownloader.ModelSearchPage, error) {
			return &hfdownloader.ModelSearchPage{}, nil
		})
	m.results = sampleSearchResults(3)
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = updated.(*ModelSearchModel)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d", m.cursor)
	}
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 's', Text: "s"}))
	m = updated.(*ModelSearchModel)
	if m.opts.Sort != "downloads" || cmd == nil {
		t.Fatalf("sort = %q, cmd nil = %v", m.opts.Sort, cmd == nil)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'g', Text: "g"}))
	m = updated.(*ModelSearchModel)
	if m.access != 1 || m.searchOptions().Gated == nil || *m.searchOptions().Gated {
		t.Fatalf("access filter was not changed to open")
	}
}

func TestModelSearchIgnoresStaleResponses(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "x"}, nil)
	m.requestID = 2
	m.results = sampleSearchResults(1)
	updated, _ := m.Update(searchResponseMsg{id: 1, results: []hfdownloader.ModelSearchResult{{ID: "stale/model"}}})
	m = updated.(*ModelSearchModel)
	if m.results[0].ID == "stale/model" {
		t.Fatal("stale response replaced current results")
	}
}

func TestExplicitSearchInvalidatesPendingDebounce(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "x"},
		func(context.Context, hfdownloader.ModelSearchOptions, string) (*hfdownloader.ModelSearchPage, error) {
			return &hfdownloader.ModelSearchPage{}, nil
		})
	m.debounceID = 7
	_ = m.startSearch()
	if m.debounceID != 8 {
		t.Fatalf("debounce id = %d, want 8", m.debounceID)
	}
	requestID := m.requestID
	updated, cmd := m.Update(searchDebounceMsg{id: 7})
	m = updated.(*ModelSearchModel)
	if cmd != nil || m.requestID != requestID {
		t.Fatal("stale debounce started a redundant search")
	}
}

func TestSelectorViewFillsTerminalAndKeepsControlsVisible(t *testing.T) {
	items := make([]smartdl.SelectableItem, 30)
	for i := range items {
		items[i] = smartdl.SelectableItem{
			ID: fmt.Sprintf("q%d", i), Label: fmt.Sprintf("Q%d_K_M", i),
			Description: "A model variant description", Size: int64(i+1) * 1_000_000_000,
			SizeHuman: fmt.Sprintf("%d GiB", i+1), Quality: 4, Category: "quantization",
			FilterValue: fmt.Sprintf("q%d", i), Recommended: i == 4,
		}
	}
	m := NewSelectorModel(&smartdl.RepoInfo{
		Repo: "owner/a-very-long-model-name", Type: smartdl.TypeGGUF,
		TypeDescription: "GGUF model", FileCount: 31, TotalSizeHuman: "99 GiB",
		SelectableItems: items,
	})
	m.width, m.height, m.cursor = 80, 24, 20
	view := m.View().Content
	plainView := ansi.Strip(view)
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("height = %d, want %d", got, m.height)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got != m.width {
			t.Fatalf("line %d width = %d, want %d", i, got, m.width)
		}
	}
	for _, want := range []string{"Q20_K_M", "Selected:", "Command:", "enter download"} {
		if !strings.Contains(plainView, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

func TestSelectorStateHasSingleSelectionSource(t *testing.T) {
	m := NewSelectorModel(&smartdl.RepoInfo{Repo: "owner/model", SelectableItems: []smartdl.SelectableItem{
		{Label: "one", Category: "variant", FilterValue: "one"},
		{Label: "two", Category: "variant", FilterValue: "two"},
	}})
	m.cursor = 1
	m.toggleCurrent()
	if got := m.getSelectedFilters(); len(got) != 1 || got[0] != "two" {
		t.Fatalf("selected filters = %v", got)
	}
	m.selectAll(true)
	if got := m.getSelectedFilters(); len(got) != 2 {
		t.Fatalf("selected filters after select all = %v", got)
	}
}

func TestSelectorStartsOnRecommendedItem(t *testing.T) {
	m := NewSelectorModel(&smartdl.RepoInfo{Repo: "owner/model", SelectableItems: []smartdl.SelectableItem{
		{Label: "one", FilterValue: "one"},
		{Label: "two", FilterValue: "two", Recommended: true},
	}})
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want recommended item at 1", m.cursor)
	}
}

func TestBranchPickerViewFillsTerminal(t *testing.T) {
	m := NewBranchPickerModel("owner/model", []smartdl.RepoRef{
		{Name: "main", Type: "branch"}, {Name: "dev", Type: "branch"}, {Name: "v1", Type: "tag"},
	})
	m.width, m.height = 70, 18
	if got := lipgloss.Height(m.View().Content); got != m.height {
		t.Fatalf("height = %d, want %d", got, m.height)
	}
}

func sampleSearchResults(count int) []hfdownloader.ModelSearchResult {
	results := make([]hfdownloader.ModelSearchResult, count)
	for i := range results {
		results[i] = hfdownloader.ModelSearchResult{
			ID: fmt.Sprintf("owner/model-%02d", i), Author: "owner", Downloads: int64(i+1) * 1000,
			Likes: int64(i), PipelineTag: "text-generation", LibraryName: "transformers",
			LastModified: "2026-07-14T12:00:00Z", Tags: []string{"transformers", "gguf"},
		}
	}
	return results
}

func TestModelSearchLoadsMorePagesOnScroll(t *testing.T) {
	var pageCalls []string
	fetcher := func(_ context.Context, _ hfdownloader.ModelSearchOptions, pageURL string) (*hfdownloader.ModelSearchPage, error) {
		pageCalls = append(pageCalls, pageURL)
		if pageURL == "" {
			return &hfdownloader.ModelSearchPage{Results: sampleSearchResults(10), NextPageURL: "cursor-2"}, nil
		}
		return &hfdownloader.ModelSearchPage{Results: []hfdownloader.ModelSearchResult{
			{ID: "owner/page2-model"},
			{ID: "owner/model-03"}, // duplicate of page 1: must be dropped
		}}, nil
	}
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "x"}, fetcher)
	m.height = 12

	// Simulate the initial response.
	updated, _ := m.Update(searchResponseMsg{id: m.requestID, results: sampleSearchResults(10), next: "cursor-2"})
	m = updated.(*ModelSearchModel)
	if m.nextPage != "cursor-2" {
		t.Fatalf("nextPage = %q", m.nextPage)
	}
	if !strings.Contains(m.status, "more") {
		t.Errorf("status %q does not hint at more pages", m.status)
	}

	// Jump to the end: should trigger a load-more fetch.
	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	m = updated.(*ModelSearchModel)
	if cmd == nil {
		t.Fatal("expected load-more command at end of list")
	}
	msg := extractSearchResponse(t, cmd())
	if !msg.appends {
		t.Fatalf("expected appending page response, got %+v", msg)
	}
	updated, _ = m.Update(msg)
	m = updated.(*ModelSearchModel)

	if len(m.results) != 11 {
		t.Fatalf("results = %d, want 11 (10 + 1 new, duplicate dropped)", len(m.results))
	}
	if m.results[10].ID != "owner/page2-model" {
		t.Errorf("appended result = %q", m.results[10].ID)
	}
	if m.nextPage != "" {
		t.Errorf("nextPage = %q, want cleared", m.nextPage)
	}
	if len(pageCalls) == 0 || pageCalls[len(pageCalls)-1] != "cursor-2" {
		t.Errorf("fetcher page calls = %v", pageCalls)
	}
}

// extractSearchResponse pulls the searchResponseMsg out of a possibly-batched
// command result.
func extractSearchResponse(t *testing.T, msg tea.Msg) searchResponseMsg {
	t.Helper()
	switch v := msg.(type) {
	case searchResponseMsg:
		return v
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			if resp, ok := c().(searchResponseMsg); ok {
				return resp
			}
		}
	}
	t.Fatalf("no searchResponseMsg in %T", msg)
	return searchResponseMsg{}
}

func TestModelSearchStaleLoadMoreDropped(t *testing.T) {
	m := newModelSearchModelWithFetcher(context.Background(), hfdownloader.ModelSearchOptions{Query: "x"}, nil)
	m.requestID = 3
	m.results = sampleSearchResults(2)
	updated, _ := m.Update(searchResponseMsg{id: 2, appends: true, results: []hfdownloader.ModelSearchResult{{ID: "stale/model"}}})
	m = updated.(*ModelSearchModel)
	if len(m.results) != 2 {
		t.Fatalf("stale page appended: %d results", len(m.results))
	}
}
