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
	if m.repos[0].Repo != "acme/large" {
		t.Fatalf("initial sort = %q", m.repos[0].Repo)
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
	if len(m.repos) != 1 || m.repos[0].Type != hfdownloader.RepoTypeModel {
		t.Fatalf("type filter repos = %+v", m.repos)
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
	if len(m.repos) != 1 || m.repos[0].Repo != "acme/qwen" {
		t.Fatalf("search results = %+v", m.repos)
	}

	view := m.View().Content
	if lipgloss.Height(view) != m.height {
		t.Fatalf("height = %d, want %d", lipgloss.Height(view), m.height)
	}
	if !strings.Contains(view, "acme/qwen") || !strings.Contains(view, "HF CACHE CLEANUP") {
		t.Fatalf("view missing content:\n%s", view)
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
