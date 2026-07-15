// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/smartdl"
)

// SelectorResult contains the result of the interactive selection.
type SelectorResult struct {
	// Action is what the user chose: "download", "copy", "cancel"
	Action string

	// SelectedFilters is the list of filter values to use with -F flag.
	SelectedFilters []string

	// CLICommand is the generated CLI command.
	CLICommand string
}

// categoryGroup groups items by category for display.
type categoryGroup struct {
	Title       string
	ItemIndexes []int
}

// itemState tracks the selection state of an item.
type itemState struct {
	Item     smartdl.SelectableItem
	Selected bool
}

// SelectorModel is the bubbletea model for interactive selection.
type SelectorModel struct {
	// Input data
	repoInfo *smartdl.RepoInfo

	// Grouped items
	categories []categoryGroup

	// All items flat (for index mapping)
	allItems []itemState

	// Navigation state
	cursor    int
	maxCursor int

	// Terminal dimensions
	width  int
	height int

	// Result
	result SelectorResult
	done   bool
}

// NewSelectorModel creates a new selector model from repo analysis.
func NewSelectorModel(info *smartdl.RepoInfo) *SelectorModel {
	m := &SelectorModel{
		repoInfo: info,
	}
	foundRecommended := false

	// Group items by category
	categoryMap := make(map[string][]smartdl.SelectableItem)
	categoryOrder := []string{}

	for _, item := range info.SelectableItems {
		cat := item.Category
		if cat == "" {
			cat = "options"
		}
		if _, exists := categoryMap[cat]; !exists {
			categoryOrder = append(categoryOrder, cat)
		}
		categoryMap[cat] = append(categoryMap[cat], item)
	}

	// Build category groups
	globalIdx := 0
	for _, catName := range categoryOrder {
		items := categoryMap[catName]
		group := categoryGroup{
			Title: FormatCategoryTitle(catName),
		}

		for _, item := range items {
			state := itemState{
				Item:     item,
				Selected: item.Recommended, // Pre-select recommended items
			}
			group.ItemIndexes = append(group.ItemIndexes, globalIdx)
			m.allItems = append(m.allItems, state)
			if item.Recommended && !foundRecommended {
				m.cursor = globalIdx
				foundRecommended = true
			}
			globalIdx++
		}

		m.categories = append(m.categories, group)
	}

	m.maxCursor = len(m.allItems) - 1
	if m.maxCursor < 0 {
		m.maxCursor = 0
	}

	return m
}

// Init implements tea.Model.
func (m *SelectorModel) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m *SelectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.result.Action = "cancel"
			m.done = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < m.maxCursor {
				m.cursor++
			}

		case " ": // Space to toggle
			m.toggleCurrent()

		case "a": // Select all
			m.selectAll(true)

		case "n": // Select none
			m.selectAll(false)

		case "enter": // Download
			m.result.Action = "download"
			m.result.SelectedFilters = m.getSelectedFilters()
			m.result.CLICommand = m.generateCommand()
			m.done = true
			return m, tea.Quit

		case "c": // Copy command
			cmd := m.generateCommand()
			if err := clipboard.WriteAll(cmd); err == nil {
				m.result.Action = "copy"
				m.result.CLICommand = cmd
				m.result.SelectedFilters = m.getSelectedFilters()
				m.done = true
				return m, tea.Quit
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	return m, nil
}

// View implements tea.Model.
func (m *SelectorModel) View() string {
	if m.done {
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 30
	}

	title := fillStyledLine(TitleStyle.Copy().Margin(0).Render(m.repoInfo.Repo), w, SearchBackgroundStyle)
	typeInfo := fillStyledLine(HeaderInfoStyle.Render(fmt.Sprintf("Type: %s (%s)", m.repoInfo.Type, m.repoInfo.TypeDescription)), w, SearchBackgroundStyle)
	statsInfo := fillStyledLine(SubtitleStyle.Render(fmt.Sprintf("%d files • %s total", m.repoInfo.FileCount, m.repoInfo.TotalSizeHuman)), w, SearchBackgroundStyle)
	separator := SearchHeavySeparatorStyle.Render(strings.Repeat("═", w))

	// Four header rows plus summary, three-row command box, and one footer.
	viewportHeight := searchMax(1, h-9)
	items := m.renderItemViewport(w, viewportHeight)

	selectedCount, totalSize := m.getSelectionStats()
	summaryLine := SummaryLabelStyle.Render("Selected: ") +
		SummaryValueStyle.Render(fmt.Sprintf("%d items", selectedCount)) +
		SummaryLabelStyle.Render(" • ") +
		SummaryValueStyle.Render(humanSize(totalSize))
	summaryLine = fillStyledLine(summaryLine, w, SearchBackgroundStyle)
	command := m.renderCommandBox(w)
	footer := fillStyledLine(m.renderFooter(w), w, SearchBottomBarStyle)

	content := strings.Join([]string{title, typeInfo, statsInfo, separator, items, summaryLine, command, footer}, "\n")
	return fitScreen(content, w, h, SearchScreenStyle)
}

type selectorRow struct {
	text string
}

func (m *SelectorModel) renderItemViewport(width, height int) string {
	rows := make([]selectorRow, 0, len(m.allItems)+len(m.categories)*2)
	cursorRow := 0
	for categoryIndex, category := range m.categories {
		if categoryIndex > 0 {
			rows = append(rows, selectorRow{text: ""})
		}
		rows = append(rows, selectorRow{
			text: CategoryStyle.Copy().Margin(0).Render(category.Title),
		})
		for _, itemIndex := range category.ItemIndexes {
			if itemIndex == m.cursor {
				cursorRow = len(rows)
			}
			rows = append(rows, selectorRow{text: m.renderItem(itemIndex, width)})
			item := m.allItems[itemIndex].Item
			if itemIndex == m.cursor && item.Description != "" {
				rows = append(rows, selectorRow{
					text: DescriptionStyle.Render("    " + item.Description),
				})
			}
		}
	}

	start := cursorRow - height/2
	if start < 0 {
		start = 0
	}
	if start+height > len(rows) {
		start = searchMax(0, len(rows)-height)
	}
	end := searchMin(len(rows), start+height)
	visible := make([]string, 0, height)
	for _, row := range rows[start:end] {
		visible = append(visible, fillStyledLine(row.text, width, SearchBackgroundStyle))
	}
	for len(visible) < height {
		visible = append(visible, fillStyledLine("", width, SearchBackgroundStyle))
	}
	return strings.Join(visible, "\n")
}

func (m *SelectorModel) renderItem(index, width int) string {
	state := m.allItems[index]
	label := state.Item.Label
	if state.Item.Recommended {
		label += "  recommended"
	}
	parts := []string{RenderCheckbox(state.Selected), label}
	if state.Item.SizeHuman != "" {
		parts = append(parts, state.Item.SizeHuman)
	}
	if state.Item.Quality > 0 {
		parts = append(parts, RenderStars(state.Item.Quality))
	}
	if state.Item.RAMHuman != "" {
		parts = append(parts, "~"+state.Item.RAMHuman+" RAM")
	}
	line := strings.Join(parts, "  ")
	if index == m.cursor {
		line = "> " + line
		line = ansi.Truncate(line, width, "…")
		return SearchSelectedStyle.Width(width).Render(line)
	}
	return ItemStyle.Copy().PaddingLeft(2).Render(line)
}

func (m *SelectorModel) renderCommandBox(width int) string {
	style := CommandBoxStyle.Copy().Margin(0)
	borderWidth := style.GetBorderLeftSize() + style.GetBorderRightSize()
	styleWidth := searchMax(1, width-borderWidth)
	contentWidth := searchMax(1, styleWidth-style.GetHorizontalPadding())
	label := CommandLabelStyle.Render("Command: ")
	commandWidth := searchMax(1, contentWidth-lipgloss.Width(label))
	command := CommandTextStyle.Render(ansi.Truncate(m.generateCommand(), commandWidth, "…"))
	return style.Width(styleWidth).Height(1).Render(label + command)
}

// toggleCurrent toggles the selection of the current item.
func (m *SelectorModel) toggleCurrent() {
	if m.cursor >= 0 && m.cursor < len(m.allItems) {
		m.allItems[m.cursor].Selected = !m.allItems[m.cursor].Selected
	}
}

// selectAll selects or deselects all items.
func (m *SelectorModel) selectAll(selected bool) {
	for i := range m.allItems {
		m.allItems[i].Selected = selected
	}
}

// getSelectedFilters returns the filter values of selected items.
func (m *SelectorModel) getSelectedFilters() []string {
	var filters []string
	for _, item := range m.allItems {
		if item.Selected && item.Item.FilterValue != "" {
			filters = append(filters, item.Item.FilterValue)
		}
	}
	return filters
}

// getSelectionStats returns the count and total size of selected items.
func (m *SelectorModel) getSelectionStats() (count int, size int64) {
	for _, item := range m.allItems {
		if item.Selected {
			count++
			size += item.Item.Size
		}
	}
	return
}

// generateCommand generates the CLI command for current selection.
func (m *SelectorModel) generateCommand() string {
	filters := m.getSelectedFilters()
	return m.repoInfo.GenerateCLICommand(filters)
}

// renderFooter renders the keybinding help footer.
func (m *SelectorModel) renderFooter(width int) string {
	keys := []struct {
		key  string
		desc string
	}{
		{"↑↓", "navigate"},
		{"space", "toggle"},
		{"a", "all"},
		{"n", "none"},
		{"enter", "download"},
		{"c", "copy cmd"},
		{"q", "quit"},
	}
	if width < 96 {
		keys = []struct {
			key  string
			desc string
		}{
			{"↑↓", "move"},
			{"space", "toggle"},
			{"a/n", "all/none"},
			{"enter", "download"},
			{"c", "copy"},
			{"q", "quit"},
		}
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, HelpKeyStyle.Render(k.key)+" "+HelpStyle.Render(k.desc))
	}

	return FooterStyle.Copy().Margin(0).Render(strings.Join(parts, " • "))
}

// Result returns the selection result (call after tea.Program ends).
func (m *SelectorModel) Result() SelectorResult {
	return m.result
}

// RunSelector runs the interactive selector TUI.
// Returns the selection result or an error.
func RunSelector(info *smartdl.RepoInfo) (*SelectorResult, error) {
	if len(info.SelectableItems) == 0 {
		return nil, fmt.Errorf("no selectable items found for %s", info.Repo)
	}

	model := NewSelectorModel(info)
	p := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run selector: %w", err)
	}

	m := finalModel.(*SelectorModel)
	result := m.Result()

	return &result, nil
}

// humanSize converts bytes to human readable format.
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Ensure SelectorModel implements tea.Model.
var _ tea.Model = (*SelectorModel)(nil)
