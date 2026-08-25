// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

type cacheLoadedMsg struct {
	inventory *hfdownloader.CacheInventory
	err       error
}

type cacheDeletedMsg struct {
	inventory *hfdownloader.CacheInventory
	removed   int
	bytes     int64
	errs      []error
}

// CacheBrowserResult summarizes cleanup performed by the interactive browser.
type CacheBrowserResult struct {
	Removed int
	Bytes   int64
}

// CacheBrowserModel browses and removes repositories from the local HF cache.
type CacheBrowserModel struct {
	cache *hfdownloader.HFCache

	inventory *hfdownloader.CacheInventory
	repos     []hfdownloader.CachedRepo
	selected  map[string]bool
	cursor    int
	width     int
	height    int

	query      string
	searching  bool
	typeFilter int
	sortMode   int
	confirming bool
	deleting   bool
	status     string
	err        error
	done       bool
	result     CacheBrowserResult
}

var cacheTypes = []string{"all", "model", "dataset", "space"}
var cacheSorts = []string{"size", "name", "recent"}

// NewCacheBrowserModel creates a cache browser. Scanning starts in Init.
func NewCacheBrowserModel(cache *hfdownloader.HFCache) *CacheBrowserModel {
	return &CacheBrowserModel{cache: cache, selected: make(map[string]bool), width: 100, height: 30}
}

func (m *CacheBrowserModel) Init() tea.Cmd {
	return tea.Batch(m.load(), tea.RequestBackgroundColor)
}

func (m *CacheBrowserModel) load() tea.Cmd {
	return func() tea.Msg {
		inventory, err := m.cache.Scan()
		return cacheLoadedMsg{inventory: inventory, err: err}
	}
}

func (m *CacheBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		setBackgroundTheme(msg.IsDark())
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case cacheLoadedMsg:
		m.inventory, m.err = msg.inventory, msg.err
		if msg.err == nil {
			m.status = fmt.Sprintf("found %d repositories", len(msg.inventory.Repos))
			m.applyFilters()
		}
	case cacheDeletedMsg:
		m.deleting = false
		m.confirming = false
		m.inventory = msg.inventory
		m.result.Removed += msg.removed
		m.result.Bytes += msg.bytes
		m.selected = make(map[string]bool)
		m.applyFilters()
		if len(msg.errs) > 0 {
			m.status = fmt.Sprintf("removed %d; %d failed: %v", msg.removed, len(msg.errs), msg.errs[0])
		} else {
			m.status = fmt.Sprintf("removed %d repositories and reclaimed %s", msg.removed, humanBytes(msg.bytes))
		}
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *CacheBrowserModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.done = true
		return m, tea.Quit
	}
	if m.deleting {
		return m, nil
	}
	if m.confirming {
		switch key {
		case "y":
			m.deleting = true
			m.status = "deleting selected repositories"
			return m, m.deleteSelection()
		case "n", "esc", "q":
			m.confirming = false
			m.status = "cleanup cancelled"
		}
		return m, nil
	}
	if m.searching {
		switch key {
		case "enter":
			m.searching = false
		case "esc":
			m.searching = false
		case "backspace":
			_, n := utf8.DecodeLastRuneInString(m.query)
			if n > 0 {
				m.query = m.query[:len(m.query)-n]
			}
		default:
			if msg.Key().Text != "" {
				m.query += msg.Key().Text
			}
		}
		m.applyFilters()
		return m, nil
	}

	switch key {
	case "q", "esc":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.repos)-1 {
			m.cursor++
		}
	case "pgup":
		m.cursor = searchMax(0, m.cursor-m.pageSize())
	case "pgdown":
		m.cursor = searchMin(searchMax(0, len(m.repos)-1), m.cursor+m.pageSize())
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = searchMax(0, len(m.repos)-1)
	case "space":
		if repo := m.current(); repo != nil {
			key := cacheRepoKey(*repo)
			m.selected[key] = !m.selected[key]
		}
	case "a":
		visibleSelected := 0
		for _, repo := range m.repos {
			if m.selected[cacheRepoKey(repo)] {
				visibleSelected++
			}
		}
		selectAll := visibleSelected != len(m.repos)
		for _, repo := range m.repos {
			m.selected[cacheRepoKey(repo)] = selectAll
		}
	case "/":
		m.searching = true
	case "backspace":
		if m.query != "" {
			m.query = ""
			m.applyFilters()
		}
	case "t":
		m.typeFilter = (m.typeFilter + 1) % len(cacheTypes)
		m.applyFilters()
	case "s":
		m.sortMode = (m.sortMode + 1) % len(cacheSorts)
		m.applyFilters()
	case "r":
		m.status = "rescanning cache"
		return m, m.load()
	case "d":
		if len(m.repos) == 0 {
			return m, nil
		}
		if m.selectedCount() == 0 {
			repo := m.repos[m.cursor]
			m.selected[cacheRepoKey(repo)] = true
		}
		m.confirming = true
	}
	return m, nil
}

func (m *CacheBrowserModel) applyFilters() {
	if m.inventory == nil {
		return
	}
	m.repos = m.repos[:0]
	query := strings.ToLower(strings.TrimSpace(m.query))
	typeFilter := cacheTypes[m.typeFilter]
	for _, repo := range m.inventory.Repos {
		if typeFilter != "all" && string(repo.Type) != typeFilter {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(repo.Repo), query) {
			continue
		}
		m.repos = append(m.repos, repo)
	}
	switch cacheSorts[m.sortMode] {
	case "name":
		sort.Slice(m.repos, func(i, j int) bool { return m.repos[i].Repo < m.repos[j].Repo })
	case "recent":
		sort.Slice(m.repos, func(i, j int) bool { return m.repos[i].LastModified.After(m.repos[j].LastModified) })
	default:
		sort.Slice(m.repos, func(i, j int) bool { return m.repos[i].Size > m.repos[j].Size })
	}
	if m.cursor >= len(m.repos) {
		m.cursor = searchMax(0, len(m.repos)-1)
	}
}

func (m *CacheBrowserModel) deleteSelection() tea.Cmd {
	targets := make([]hfdownloader.CachedRepo, 0, len(m.selected))
	for _, repo := range m.inventory.Repos {
		if m.selected[cacheRepoKey(repo)] {
			targets = append(targets, repo)
		}
	}
	return func() tea.Msg {
		var result cacheDeletedMsg
		for _, repo := range targets {
			deleted, err := m.cache.DeleteCachedRepo(repo.Repo, repo.Type, false)
			if err != nil {
				result.errs = append(result.errs, err)
				continue
			}
			result.removed++
			result.bytes += deleted.BytesRemoved
		}
		result.inventory, _ = m.cache.Scan()
		return result
	}
}

func (m *CacheBrowserModel) current() *hfdownloader.CachedRepo {
	if m.cursor < 0 || m.cursor >= len(m.repos) {
		return nil
	}
	return &m.repos[m.cursor]
}

func (m *CacheBrowserModel) selectedCount() int {
	if m.inventory == nil {
		return 0
	}
	count := 0
	for _, repo := range m.inventory.Repos {
		if m.selected[cacheRepoKey(repo)] {
			count++
		}
	}
	return count
}

func (m *CacheBrowserModel) selectedSize() int64 {
	if m.inventory == nil {
		return 0
	}
	var size int64
	for _, repo := range m.inventory.Repos {
		if m.selected[cacheRepoKey(repo)] {
			size += repo.Size
		}
	}
	return size
}

func cacheRepoKey(repo hfdownloader.CachedRepo) string {
	return string(repo.Type) + ":" + repo.Repo
}

func (m *CacheBrowserModel) render() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	total := "scanning…"
	if m.inventory != nil {
		total = fmt.Sprintf("%d repos  •  %s", len(m.inventory.Repos), humanBytes(m.inventory.TotalSize))
	}
	top := renderTwoSidedBar("HF CACHE CLEANUP", total, w, SearchTopBarStyle)
	query := m.query
	if query == "" {
		query = "all repositories"
	}
	if m.searching {
		query += "█"
	}
	filters := fmt.Sprintf("Search %s  │  t:type %s  │  s:sort %s", query, cacheTypes[m.typeFilter], cacheSorts[m.sortMode])
	filterLine := fillStyledLine(filters, w, SearchBackgroundStyle)

	bodyHeight := searchMax(1, h-7)
	rows := m.renderRows(w, bodyHeight)
	detail := m.renderDetail(w)
	status := m.status
	if m.err != nil {
		status = m.err.Error()
	}
	if m.confirming {
		status = fmt.Sprintf("Delete %d repositories and reclaim about %s?  y confirm • n cancel", m.selectedCount(), humanBytes(m.selectedSize()))
	} else if m.deleting {
		status = "Deleting selected repositories…"
	}
	statusLine := fillStyledLine(status, w, SearchBottomBarStyle)
	help := fillStyledLine("↑↓/jk move  •  space select  •  a all  •  / search  •  t type  •  s sort  •  d delete  •  q quit", w, SearchBottomBarStyle)
	content := strings.Join([]string{top, filterLine, rows, detail, statusLine, help}, "\n")
	return fitScreen(content, w, h, SearchScreenStyle)
}

func (m *CacheBrowserModel) renderRows(width, height int) string {
	if m.inventory == nil {
		return padLines(SearchMutedStyle.Render("Scanning "+m.cache.Root+" …"), width, height)
	}
	if len(m.repos) == 0 {
		return padLines(SearchMutedStyle.Render("No matching cached repositories."), width, height)
	}
	header := fmt.Sprintf("   %-8s %10s  %-*s  %10s", "TYPE", "SIZE", searchMax(12, width-45), "REPOSITORY", "MODIFIED")
	lines := []string{SearchPanelTitleStyle.Render(ansi.Truncate(header, width, "…"))}
	available := searchMax(0, height-1)
	start, end := searchWindow(m.cursor, len(m.repos), available)
	repoWidth := searchMax(12, width-45)
	for i := start; i < end; i++ {
		repo := m.repos[i]
		mark := "[ ]"
		if m.selected[cacheRepoKey(repo)] {
			mark = "[x]"
		}
		lastUsed := "—"
		if !repo.LastModified.IsZero() {
			lastUsed = repo.LastModified.Format("2006-01-02")
		}
		name := ansi.Truncate(repo.Repo, repoWidth, "…")
		line := fmt.Sprintf("%s %-8s %10s  %-*s  %10s", mark, repo.Type, humanBytes(repo.Size), repoWidth, name, lastUsed)
		line = ansi.Truncate(line, width, "…")
		if i == m.cursor {
			line = SearchSelectedStyle.Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	return padLines(strings.Join(lines, "\n"), width, height)
}

func (m *CacheBrowserModel) renderDetail(width int) string {
	repo := m.current()
	if repo == nil {
		return fillStyledLine("", width, SearchBackgroundStyle)
	}
	flags := fmt.Sprintf("%d blobs • %d snapshots", repo.FileCount, repo.SnapshotCount)
	if repo.IncompleteFiles > 0 {
		flags += fmt.Sprintf(" • %d partial (%s)", repo.IncompleteFiles, humanBytes(repo.IncompleteSize))
	}
	if repo.Active {
		flags += " • ACTIVE DOWNLOAD"
	}
	return fillStyledLine(ansi.Truncate(repo.Path+"  •  "+flags, width, "…"), width, SearchBackgroundStyle)
}

func padLines(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fillStyledLine(lines[i], width, SearchBackgroundStyle)
	}
	return strings.Join(lines, "\n")
}

func (m *CacheBrowserModel) pageSize() int { return searchMax(1, m.height-9) }

// View implements tea.Model.
func (m *CacheBrowserModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	view.WindowTitle = "Hugging Face Cache Cleanup"
	return view
}

// Result returns cleanup totals after the program exits.
func (m *CacheBrowserModel) Result() CacheBrowserResult { return m.result }

// RunCacheBrowser launches the interactive cache browser.
func RunCacheBrowser(cache *hfdownloader.HFCache) (*CacheBrowserResult, error) {
	model := NewCacheBrowserModel(cache)
	final, err := tea.NewProgram(model, programOptions()...).Run()
	if err != nil {
		return nil, fmt.Errorf("run cache browser: %w", err)
	}
	result := final.(*CacheBrowserModel).Result()
	return &result, nil
}

var _ tea.Model = (*CacheBrowserModel)(nil)
