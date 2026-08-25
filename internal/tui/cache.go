// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
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

type cacheTreeNode struct {
	repo     hfdownloader.CachedRepo
	artifact *hfdownloader.CachedArtifact
}

func (n cacheTreeNode) key() string {
	if n.artifact != nil {
		return "artifact:" + n.artifact.ID
	}
	return "repo:" + cacheRepoKey(n.repo)
}

func (n cacheTreeNode) name() string {
	if n.artifact != nil {
		return n.artifact.Name
	}
	return n.repo.Repo
}

func (n cacheTreeNode) size() int64 {
	if n.artifact != nil {
		return n.artifact.Size
	}
	return n.repo.Size
}

func (n cacheTreeNode) modified() time.Time {
	if n.artifact != nil {
		return n.artifact.LastModified
	}
	return n.repo.LastModified
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
	nodes     []cacheTreeNode
	selected  map[string]bool
	collapsed map[string]bool
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
	return &CacheBrowserModel{
		cache: cache, selected: make(map[string]bool), collapsed: make(map[string]bool),
		width: 100, height: 30,
	}
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
			m.status = fmt.Sprintf("found %d repositories with %d removable artifacts", len(msg.inventory.Repos), len(msg.inventory.Artifacts))
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
			m.status = fmt.Sprintf("removed %d items and reclaimed %s", msg.removed, humanBytes(msg.bytes))
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
			m.status = "deleting selected items"
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
		if m.cursor < len(m.nodes)-1 {
			m.cursor++
		}
	case "pgup":
		m.cursor = searchMax(0, m.cursor-m.pageSize())
	case "pgdown":
		m.cursor = searchMin(searchMax(0, len(m.nodes)-1), m.cursor+m.pageSize())
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = searchMax(0, len(m.nodes)-1)
	case "space":
		if node := m.current(); node != nil {
			m.toggleSelection(*node)
		}
	case "a":
		parents, selectedParents := 0, 0
		for _, node := range m.nodes {
			if node.artifact == nil {
				parents++
				if m.selected[node.key()] {
					selectedParents++
				}
			}
		}
		selectAll := selectedParents != parents
		for _, node := range m.nodes {
			if node.artifact == nil {
				m.selected[node.key()] = selectAll
				m.clearArtifactSelections(node.repo)
			}
		}
	case "enter":
		if node := m.current(); node != nil && node.artifact == nil {
			key := cacheRepoKey(node.repo)
			m.collapsed[key] = !m.collapsed[key]
			m.applyFilters()
		}
	case "right", "l":
		if node := m.current(); node != nil && node.artifact == nil {
			m.collapsed[cacheRepoKey(node.repo)] = false
			m.applyFilters()
		}
	case "left", "h":
		if node := m.current(); node != nil {
			if node.artifact == nil {
				m.collapsed[cacheRepoKey(node.repo)] = true
				m.applyFilters()
			} else {
				m.moveToParent(node.repo)
			}
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
		if len(m.nodes) == 0 {
			return m, nil
		}
		if m.selectedCount() == 0 {
			m.toggleSelection(m.nodes[m.cursor])
		}
		m.confirming = true
	}
	return m, nil
}

func (m *CacheBrowserModel) applyFilters() {
	if m.inventory == nil {
		return
	}
	m.nodes = m.nodes[:0]
	query := strings.ToLower(strings.TrimSpace(m.query))
	typeFilter := cacheTypes[m.typeFilter]
	artifacts := make(map[string][]hfdownloader.CachedArtifact)
	for _, artifact := range m.inventory.Artifacts {
		key := string(artifact.Type) + ":" + artifact.Repo
		artifacts[key] = append(artifacts[key], artifact)
	}
	repos := append([]hfdownloader.CachedRepo(nil), m.inventory.Repos...)
	switch cacheSorts[m.sortMode] {
	case "name":
		sort.Slice(repos, func(i, j int) bool { return repos[i].Repo < repos[j].Repo })
	case "recent":
		sort.Slice(repos, func(i, j int) bool { return repos[i].LastModified.After(repos[j].LastModified) })
	default:
		sort.Slice(repos, func(i, j int) bool { return repos[i].Size > repos[j].Size })
	}
	for _, repo := range repos {
		if typeFilter != "all" && string(repo.Type) != typeFilter {
			continue
		}
		children := append([]hfdownloader.CachedArtifact(nil), artifacts[cacheRepoKey(repo)]...)
		repoMatches := query == "" || strings.Contains(strings.ToLower(repo.Repo), query)
		if query != "" && !repoMatches {
			matched := children[:0]
			for _, artifact := range children {
				if strings.Contains(strings.ToLower(artifact.Name), query) {
					matched = append(matched, artifact)
				}
			}
			children = matched
		}
		if !repoMatches && len(children) == 0 {
			continue
		}
		m.nodes = append(m.nodes, cacheTreeNode{repo: repo})
		if m.collapsed[cacheRepoKey(repo)] && query == "" {
			continue
		}
		sort.Slice(children, func(i, j int) bool {
			switch cacheSorts[m.sortMode] {
			case "name":
				return children[i].Name < children[j].Name
			case "recent":
				return children[i].LastModified.After(children[j].LastModified)
			default:
				return children[i].Size > children[j].Size
			}
		})
		for i := range children {
			artifact := children[i]
			m.nodes = append(m.nodes, cacheTreeNode{repo: repo, artifact: &artifact})
		}
	}
	if m.cursor >= len(m.nodes) {
		m.cursor = searchMax(0, len(m.nodes)-1)
	}
}

func (m *CacheBrowserModel) deleteSelection() tea.Cmd {
	var repos []hfdownloader.CachedRepo
	var artifacts []hfdownloader.CachedArtifact
	for _, repo := range m.inventory.Repos {
		if m.selected["repo:"+cacheRepoKey(repo)] {
			repos = append(repos, repo)
		}
	}
	for _, artifact := range m.inventory.Artifacts {
		if m.selected["artifact:"+artifact.ID] && !m.selected["repo:"+string(artifact.Type)+":"+artifact.Repo] {
			artifacts = append(artifacts, artifact)
		}
	}
	return func() tea.Msg {
		var result cacheDeletedMsg
		for _, artifact := range artifacts {
			deleted, err := m.cache.DeleteCachedArtifact(artifact, false)
			if err != nil {
				result.errs = append(result.errs, err)
				continue
			}
			result.removed++
			result.bytes += deleted.BytesRemoved
		}
		for _, repo := range repos {
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

func (m *CacheBrowserModel) current() *cacheTreeNode {
	if m.cursor < 0 || m.cursor >= len(m.nodes) {
		return nil
	}
	return &m.nodes[m.cursor]
}

func (m *CacheBrowserModel) selectedCount() int {
	count := 0
	for _, selected := range m.selected {
		if selected {
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
		if m.selected["repo:"+cacheRepoKey(repo)] {
			size += repo.Size
		}
	}
	for _, artifact := range m.inventory.Artifacts {
		if m.selected["artifact:"+artifact.ID] && !m.selected["repo:"+string(artifact.Type)+":"+artifact.Repo] {
			size += artifact.Size
		}
	}
	return size
}

func (m *CacheBrowserModel) toggleSelection(node cacheTreeNode) {
	key := node.key()
	selecting := !m.selected[key]
	m.selected[key] = selecting
	if node.artifact == nil {
		m.clearArtifactSelections(node.repo)
	} else if selecting {
		delete(m.selected, "repo:"+cacheRepoKey(node.repo))
	}
}

func (m *CacheBrowserModel) clearArtifactSelections(repo hfdownloader.CachedRepo) {
	for _, artifact := range m.inventory.Artifacts {
		if artifact.Type == repo.Type && artifact.Repo == repo.Repo {
			delete(m.selected, "artifact:"+artifact.ID)
		}
	}
}

func (m *CacheBrowserModel) moveToParent(repo hfdownloader.CachedRepo) {
	for i, node := range m.nodes {
		if node.artifact == nil && node.repo.Type == repo.Type && node.repo.Repo == repo.Repo {
			m.cursor = i
			return
		}
	}
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
		total = fmt.Sprintf("%d repos / %d artifacts  •  %s", len(m.inventory.Repos), len(m.inventory.Artifacts), humanBytes(m.inventory.TotalSize))
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
		status = fmt.Sprintf("Delete %d selected items and reclaim about %s?  y confirm • n cancel", m.selectedCount(), humanBytes(m.selectedSize()))
	} else if m.deleting {
		status = "Deleting selected items…"
	}
	statusLine := fillStyledLine(status, w, SearchBottomBarStyle)
	help := fillStyledLine("↑↓/jk move  •  ←→/hl tree  •  space select  •  a all repos  •  / search  •  d delete  •  q quit", w, SearchBottomBarStyle)
	content := strings.Join([]string{top, filterLine, rows, detail, statusLine, help}, "\n")
	return fitScreen(content, w, h, SearchScreenStyle)
}

func (m *CacheBrowserModel) renderRows(width, height int) string {
	if m.inventory == nil {
		return padLines(SearchMutedStyle.Render("Scanning "+m.cache.Root+" …"), width, height)
	}
	if len(m.nodes) == 0 {
		return padLines(SearchMutedStyle.Render("No matching cached repositories."), width, height)
	}
	header := fmt.Sprintf("   %-8s %10s  %-*s  %10s", "TYPE", "SIZE", searchMax(12, width-45), "REPOSITORY / ARTIFACT", "MODIFIED")
	lines := []string{SearchPanelTitleStyle.Render(ansi.Truncate(header, width, "…"))}
	available := searchMax(0, height-1)
	start, end := searchWindow(m.cursor, len(m.nodes), available)
	repoWidth := searchMax(12, width-45)
	for i := start; i < end; i++ {
		node := m.nodes[i]
		mark := "[ ]"
		if m.selected[node.key()] {
			mark = "[x]"
		} else if node.artifact != nil && m.selected["repo:"+cacheRepoKey(node.repo)] {
			// Selecting a repository implicitly selects every child artifact.
			mark = "[*]"
		}
		lastUsed := "—"
		if !node.modified().IsZero() {
			lastUsed = node.modified().Format("2006-01-02")
		}
		typeName := string(node.repo.Type)
		name := node.repo.Repo
		if node.artifact == nil {
			prefix := "[-] "
			if m.collapsed[cacheRepoKey(node.repo)] {
				prefix = "[+] "
			}
			name = prefix + name
		} else {
			typeName = "artifact"
			name = "    |- " + node.artifact.Name
			if node.artifact.FileCount > 1 {
				name += fmt.Sprintf(" (%d shards)", node.artifact.FileCount)
			}
		}
		name = ansi.Truncate(name, repoWidth, "…")
		line := fmt.Sprintf("%s %-8s %10s  %-*s  %10s", mark, typeName, humanBytes(node.size()), repoWidth, name, lastUsed)
		line = ansi.Truncate(line, width, "…")
		if i == m.cursor {
			line = SearchSelectedStyle.Width(width).Render(line)
		}
		lines = append(lines, line)
	}
	return padLines(strings.Join(lines, "\n"), width, height)
}

func (m *CacheBrowserModel) renderDetail(width int) string {
	node := m.current()
	if node == nil {
		return fillStyledLine("", width, SearchBackgroundStyle)
	}
	if node.artifact != nil {
		detail := fmt.Sprintf("%s  •  %d file(s)  •  snapshot %s", node.artifact.Name, node.artifact.FileCount, shortSHA(node.artifact.Commit))
		return fillStyledLine(ansi.Truncate(detail, width, "…"), width, SearchBackgroundStyle)
	}
	repo := node.repo
	flags := fmt.Sprintf("%d physical files • %d snapshots", repo.FileCount, repo.SnapshotCount)
	if repo.IncompleteFiles > 0 {
		flags += fmt.Sprintf(" • %d partial (%s)", repo.IncompleteFiles, humanBytes(repo.IncompleteSize))
	}
	if repo.Active {
		flags += " • ACTIVE DOWNLOAD"
	}
	return fillStyledLine(ansi.Truncate(repo.Path+"  •  "+flags, width, "…"), width, SearchBackgroundStyle)
}

func shortSHA(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
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
