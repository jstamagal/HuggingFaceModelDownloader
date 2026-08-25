// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/smartdl"
)

// BranchPickerResult contains the result of branch selection.
type BranchPickerResult struct {
	// Selected is the chosen branch/tag name.
	Selected string

	// Cancelled indicates if the user cancelled selection.
	Cancelled bool
}

// refItem represents a branch or tag in the picker.
type refItem struct {
	Ref       smartdl.RepoRef
	Index     int
	IsDefault bool
}

// BranchPickerModel is the bubbletea model for branch/tag selection.
type BranchPickerModel struct {
	repo     string
	branches []refItem
	tags     []refItem
	allItems []refItem

	cursor int
	width  int
	height int
	result BranchPickerResult
	done   bool
}

// NewBranchPickerModel creates a new branch picker from repo refs.
func NewBranchPickerModel(repo string, refs []smartdl.RepoRef) *BranchPickerModel {
	m := &BranchPickerModel{
		repo: repo,
	}

	// Separate branches and tags
	idx := 0
	for _, ref := range refs {
		item := refItem{
			Ref:       ref,
			Index:     idx,
			IsDefault: ref.Name == "main" || ref.Name == "master",
		}

		if ref.Type == "branch" {
			m.branches = append(m.branches, item)
		} else {
			m.tags = append(m.tags, item)
		}
		m.allItems = append(m.allItems, item)
		idx++
	}

	// Default cursor to "main" if it exists
	for i, item := range m.allItems {
		if item.Ref.Name == "main" {
			m.cursor = i
			break
		}
	}

	return m
}

// Init implements tea.Model.
func (m *BranchPickerModel) Init() tea.Cmd {
	return tea.RequestBackgroundColor
}

// Update implements tea.Model.
func (m *BranchPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		setBackgroundTheme(msg.IsDark())

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.result.Cancelled = true
			m.done = true
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < len(m.allItems)-1 {
				m.cursor++
			}

		case "enter", "space":
			if m.cursor >= 0 && m.cursor < len(m.allItems) {
				m.result.Selected = m.allItems[m.cursor].Ref.Name
				m.done = true
				return m, tea.Quit
			}
		}
	}

	return m, nil
}

// View implements tea.Model.
func (m *BranchPickerModel) render() string {
	if m.done {
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 80
	}
	if h <= 0 {
		h = 24
	}
	title := fillStyledLine(TitleStyle.Copy().Margin(0).Render("Select Branch/Tag"), w, SearchBackgroundStyle)
	subtitle := fillStyledLine(SubtitleStyle.Render(m.repo), w, SearchBackgroundStyle)
	hint := fillStyledLine(SubtitleStyle.Render("This repository has multiple versions. Select one to analyze."), w, SearchBackgroundStyle)
	separator := SearchHeavySeparatorStyle.Render(strings.Repeat("═", w))
	list := m.renderRefViewport(w, searchMax(1, h-5))
	footer := fillStyledLine(m.renderFooter(), w, SearchBottomBarStyle)
	return fitScreen(strings.Join([]string{title, subtitle, hint, separator, list, footer}, "\n"), w, h, SearchScreenStyle)
}

// View implements tea.Model.
func (m *BranchPickerModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

func (m *BranchPickerModel) renderRefViewport(width, height int) string {
	type row struct {
		text  string
		index int
	}
	rows := make([]row, 0, len(m.allItems)+4)
	addSection := func(title string, items []refItem) {
		if len(items) == 0 {
			return
		}
		if len(rows) > 0 {
			rows = append(rows, row{index: -1})
		}
		rows = append(rows, row{text: CategoryStyle.Copy().Margin(0).Render(title), index: -1})
		for _, item := range items {
			rows = append(rows, row{text: m.renderRefItem(item), index: item.Index})
		}
	}
	addSection("Branches", m.branches)
	addSection("Tags", m.tags)

	cursorRow := 0
	for i := range rows {
		if rows[i].index == m.cursor {
			cursorRow = i
			break
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

// renderRefItem renders a single ref item.
func (m *BranchPickerModel) renderRefItem(item refItem) string {
	// Cursor indicator
	cursor := "  "
	if m.cursor == item.Index {
		cursor = CursorStyle.Render("> ")
	}

	// Icon based on type
	icon := "  "
	if item.Ref.Type == "branch" {
		icon = SubtitleStyle.Render("") + " "
	} else {
		icon = SubtitleStyle.Render("") + " "
	}

	// Name with default indicator
	name := item.Ref.Name
	if item.IsDefault {
		name = name + " " + SuccessStyle.Render("(default)")
	}

	// Highlight current selection
	var line string
	if m.cursor == item.Index {
		line = fmt.Sprintf("%s%s%s", cursor, icon, SelectedItemStyle.Render(name))
	} else {
		line = fmt.Sprintf("%s%s%s", cursor, icon, ItemStyle.Render(name))
	}

	return line
}

// renderFooter renders the keybinding help footer.
func (m *BranchPickerModel) renderFooter() string {
	keys := []struct {
		key  string
		desc string
	}{
		{"↑↓", "navigate"},
		{"enter", "select"},
		{"q", "cancel"},
	}

	var parts []string
	for _, k := range keys {
		parts = append(parts, HelpKeyStyle.Render(k.key)+" "+HelpStyle.Render(k.desc))
	}

	return FooterStyle.Copy().Margin(0).Render(strings.Join(parts, " • "))
}

// Result returns the selection result.
func (m *BranchPickerModel) Result() BranchPickerResult {
	return m.result
}

// RunBranchPicker runs the branch picker TUI.
// Returns the selected branch name or empty string if cancelled.
func RunBranchPicker(repo string, refs []smartdl.RepoRef) (*BranchPickerResult, error) {
	if len(refs) == 0 {
		return &BranchPickerResult{Selected: "main"}, nil
	}

	// If only one ref, return it directly
	if len(refs) == 1 {
		return &BranchPickerResult{Selected: refs[0].Name}, nil
	}

	model := NewBranchPickerModel(repo, refs)
	p := tea.NewProgram(model, programOptions()...)

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run branch picker: %w", err)
	}

	m := finalModel.(*BranchPickerModel)
	result := m.Result()

	return &result, nil
}

// Ensure BranchPickerModel implements tea.Model.
var _ tea.Model = (*BranchPickerModel)(nil)
