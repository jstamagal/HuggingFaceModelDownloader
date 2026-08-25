// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

func TestCacheBrowserFiltersSelectsAndConfirms(t *testing.T) {
	t.Parallel()
	m := NewCacheBrowserModel(hfdownloader.NewHFCache(t.TempDir(), 0))
	m.inventory = &hfdownloader.CacheInventory{Repos: []hfdownloader.CachedRepo{
		{Repo: "acme/large", Type: hfdownloader.RepoTypeModel, Size: 200},
		{Repo: "acme/data", Type: hfdownloader.RepoTypeDataset, Size: 100},
	}}
	m.applyFilters()
	if m.nodes[0].repo.Repo != "acme/large" {
		t.Fatalf("initial sort = %q", m.nodes[0].repo.Repo)
	}

	m.updateKey(keyPress("space"))
	if m.selectedCount() != 1 {
		t.Fatalf("selected count = %d", m.selectedCount())
	}
	m.updateKey(keyPress("d"))
	if !m.confirming {
		t.Fatal("delete did not require confirmation")
	}
	m.updateKey(keyPress("n"))
	if m.confirming {
		t.Fatal("confirmation was not cancelled")
	}

	m.updateKey(keyPress("t"))
	if len(m.nodes) != 1 || m.nodes[0].repo.Type != hfdownloader.RepoTypeModel {
		t.Fatalf("type filter nodes = %+v", m.nodes)
	}
}

func TestCacheBrowserSearchAndRender(t *testing.T) {
	t.Parallel()
	m := NewCacheBrowserModel(hfdownloader.NewHFCache(t.TempDir(), 0))
	m.width, m.height = 90, 24
	m.inventory = &hfdownloader.CacheInventory{TotalSize: 42, Repos: []hfdownloader.CachedRepo{
		{Repo: "acme/llama", Type: hfdownloader.RepoTypeModel, Size: 42, LastModified: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{Repo: "acme/qwen", Type: hfdownloader.RepoTypeModel, Size: 20},
	}}
	m.applyFilters()
	m.updateKey(keyPress("/"))
	m.updateKey(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	m.updateKey(tea.KeyPressMsg(tea.Key{Code: 'w', Text: "w"}))
	if len(m.nodes) != 1 || m.nodes[0].repo.Repo != "acme/qwen" {
		t.Fatalf("search results = %+v", m.nodes)
	}

	view := m.View().Content
	if lipgloss.Height(view) != m.height {
		t.Fatalf("height = %d, want %d", lipgloss.Height(view), m.height)
	}
	if !strings.Contains(view, "acme/qwen") || !strings.Contains(view, "HF CACHE CLEANUP") {
		t.Fatalf("view missing content:\n%s", view)
	}
}

func TestCacheBrowserRendersRepoArtifactTreeAndSelectsChild(t *testing.T) {
	t.Parallel()
	m := NewCacheBrowserModel(hfdownloader.NewHFCache(t.TempDir(), 0))
	repo := hfdownloader.CachedRepo{Repo: "acme/many", Type: hfdownloader.RepoTypeModel, Size: 300}
	m.inventory = &hfdownloader.CacheInventory{
		TotalSize: 300,
		Repos:     []hfdownloader.CachedRepo{repo},
		Artifacts: []hfdownloader.CachedArtifact{
			{ID: "q4", Repo: repo.Repo, Type: repo.Type, Name: "model-Q4_K_M.gguf", Size: 100},
			{ID: "q8", Repo: repo.Repo, Type: repo.Type, Name: "model-Q8_0.gguf", Size: 200},
		},
	}
	m.applyFilters()
	if len(m.nodes) != 3 || m.nodes[0].artifact != nil || m.nodes[1].artifact == nil {
		t.Fatalf("tree nodes = %+v", m.nodes)
	}
	m.cursor = 1
	m.updateKey(keyPress("space"))
	if m.selectedCount() != 1 || m.selectedSize() != 200 { // children sort largest first
		t.Fatalf("selection = count:%d size:%d", m.selectedCount(), m.selectedSize())
	}
	view := m.View().Content
	if !strings.Contains(view, "[-] acme/many") || !strings.Contains(view, "|- model-Q8_0.gguf") {
		t.Fatalf("tree missing from view:\n%s", view)
	}
	m.cursor = 0
	m.updateKey(keyPress("space"))
	if !m.selected["repo:"+cacheRepoKey(repo)] || m.selected["artifact:q8"] || m.selectedSize() != repo.Size {
		t.Fatalf("parent selection did not replace child selection: %+v", m.selected)
	}
	if rows := m.renderRows(90, 10); !strings.Contains(rows, "[*] artifact") {
		t.Fatalf("parent selection is not reflected on child rows:\n%s", rows)
	}
	m.updateKey(keyPress("enter"))
	if len(m.nodes) != 1 || !m.collapsed[cacheRepoKey(repo)] {
		t.Fatalf("tree did not collapse: %+v", m.nodes)
	}
}

func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "space":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace})
	default:
		r, _ := utf8.DecodeRuneInString(key)
		return tea.KeyPressMsg(tea.Key{Code: r, Text: key})
	}
}
