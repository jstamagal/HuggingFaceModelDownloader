// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

const searchDebounce = 350 * time.Millisecond

var (
	searchSorts     = []string{"trending", "downloads", "likes", "updated", "created"}
	searchPipelines = []string{"", "text-generation", "image-text-to-text", "text-to-image", "feature-extraction", "automatic-speech-recognition"}
	searchLibraries = []string{"", "transformers", "diffusers", "sentence-transformers", "timm", "mlx", "gguf"}
	spinnerFrames   = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

// ModelSearchResult contains the user's selection from the model browser.
type ModelSearchResult struct {
	Selected  *hfdownloader.ModelSearchResult
	Cancelled bool
}

type modelSearchFetcher func(ctx context.Context, opts hfdownloader.ModelSearchOptions, pageURL string) (*hfdownloader.ModelSearchPage, error)

type searchResponseMsg struct {
	id      int
	results []hfdownloader.ModelSearchResult
	next    string
	appends bool // true when this is a "load more" page to append
	err     error
}

type searchDebounceMsg struct{ id int }
type searchSpinnerMsg struct{}

// ModelSearchModel is the Bubble Tea model for Hub search and browsing.
type ModelSearchModel struct {
	ctx     context.Context
	opts    hfdownloader.ModelSearchOptions
	fetcher modelSearchFetcher
	input   textinput.Model

	results []hfdownloader.ModelSearchResult
	cursor  int
	width   int
	height  int

	access       int // 0=all, 1=open, 2=gated
	requestID    int
	debounceID   int
	loading      bool
	loadingMore  bool
	nextPage     string
	spinnerFrame int
	err          error
	status       string
	result       ModelSearchResult
	done         bool
}

// NewModelSearchModel creates a full-screen Hugging Face model browser.
func NewModelSearchModel(ctx context.Context, opts hfdownloader.ModelSearchOptions) *ModelSearchModel {
	return newModelSearchModelWithFetcher(ctx, opts, hfdownloader.SearchModelsPage)
}

func newModelSearchModelWithFetcher(ctx context.Context, opts hfdownloader.ModelSearchOptions, fetcher modelSearchFetcher) *ModelSearchModel {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = "model name or owner/model"
	input.CharLimit = 200
	inputStyles := textinput.DefaultStyles(themeIsDark)
	inputStyles.Focused.Prompt = SearchInputPromptStyle
	inputStyles.Focused.Text = SearchInputTextStyle
	inputStyles.Focused.Placeholder = SearchMutedStyle
	inputStyles.Blurred = inputStyles.Focused
	inputStyles.Cursor.Color = ColorPrimary
	input.SetStyles(inputStyles)
	input.SetValue(opts.Query)

	if opts.Sort == "" {
		opts.Sort = "trending"
	}
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	m := &ModelSearchModel{ctx: ctx, opts: opts, fetcher: fetcher, input: input}
	if opts.Gated != nil {
		if *opts.Gated {
			m.access = 2
		} else {
			m.access = 1
		}
	}
	if strings.TrimSpace(opts.Query) == "" {
		m.input.Focus()
	}
	return m
}

// Init implements tea.Model.
func (m *ModelSearchModel) Init() tea.Cmd {
	return tea.Batch(m.startSearch(), tea.RequestBackgroundColor)
}

// Update implements tea.Model.
func (m *ModelSearchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(searchMax(8, msg.Width-12))
		return m, nil

	case tea.BackgroundColorMsg:
		setBackgroundTheme(msg.IsDark())
		styles := textinput.DefaultStyles(themeIsDark)
		styles.Focused.Prompt = SearchInputPromptStyle
		styles.Focused.Text = SearchInputTextStyle
		styles.Focused.Placeholder = SearchMutedStyle
		styles.Blurred = styles.Focused
		styles.Cursor.Color = ColorPrimary
		m.input.SetStyles(styles)
		return m, nil

	case searchSpinnerMsg:
		if !m.loading && !m.loadingMore {
			return m, nil // stop ticking while idle
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, searchSpinnerTick()

	case searchDebounceMsg:
		if msg.id == m.debounceID {
			return m, m.startSearch()
		}
		return m, nil

	case searchResponseMsg:
		if msg.id != m.requestID {
			return m, nil
		}
		if msg.appends {
			m.loadingMore = false
			if msg.err != nil {
				// Keep what we have; surface the paging failure in the status.
				m.status = "load more failed: " + msg.err.Error()
				return m, nil
			}
			m.results = append(m.results, dedupeNewResults(m.results, msg.results)...)
			m.nextPage = msg.next
			m.status = m.resultCountStatus()
			return m, nil
		}
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			previousID := ""
			if m.cursor >= 0 && m.cursor < len(m.results) {
				previousID = m.results[m.cursor].ID
			}
			m.results = msg.results
			m.nextPage = msg.next
			m.cursor = findModelResult(msg.results, previousID)
			m.status = m.resultCountStatus()
		}
		return m, nil

	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m.cancel()
		}
		if m.input.Focused() {
			return m.updateInput(msg)
		}
		return m.updateBrowser(msg)
	}

	if m.input.Focused() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *ModelSearchModel) updateInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.input.Blur()
		return m, nil
	case "enter", "down", "up", "tab":
		m.input.Blur()
		return m, m.startSearch()
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.opts.Query = m.input.Value()
		m.debounceID++
		id := m.debounceID
		debounce := tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchDebounceMsg{id: id} })
		return m, tea.Batch(cmd, debounce)
	}
	return m, cmd
}

func (m *ModelSearchModel) updateBrowser(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m.cancel()
	case "/", "tab":
		return m, m.input.Focus()
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.results)-1 {
			m.cursor++
		}
		return m, m.maybeLoadMore()
	case "pgup":
		m.cursor = searchMax(0, m.cursor-m.resultPageSize())
	case "pgdown":
		m.cursor = searchMin(searchMax(0, len(m.results)-1), m.cursor+m.resultPageSize())
		return m, m.maybeLoadMore()
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = searchMax(0, len(m.results)-1)
		return m, m.maybeLoadMore()
	case "s":
		m.opts.Sort = cycleSearchValue(m.opts.Sort, searchSorts)
		return m, m.startSearch()
	case "t":
		m.opts.PipelineTag = cycleSearchValue(m.opts.PipelineTag, searchPipelines)
		return m, m.startSearch()
	case "l":
		m.opts.Library = cycleSearchValue(m.opts.Library, searchLibraries)
		return m, m.startSearch()
	case "g":
		m.access = (m.access + 1) % 3
		return m, m.startSearch()
	case "r":
		return m, m.startSearch()
	case "c":
		if selected := m.selected(); selected != nil {
			command := "hfdownloader analyze " + selected.ID + " -i"
			if err := clipboard.WriteAll(command); err != nil {
				// No system clipboard (typical over SSH): show the command so
				// it can be copied from the terminal instead.
				m.status = "no clipboard — " + command
			} else {
				m.status = "copied: " + command
			}
		}
	case "enter":
		if selected := m.selected(); selected != nil {
			copyOfSelected := *selected
			m.result.Selected = &copyOfSelected
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *ModelSearchModel) cancel() (tea.Model, tea.Cmd) {
	m.result.Cancelled = true
	m.done = true
	return m, tea.Quit
}

func (m *ModelSearchModel) selected() *hfdownloader.ModelSearchResult {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return nil
	}
	return &m.results[m.cursor]
}

func (m *ModelSearchModel) searchOptions() hfdownloader.ModelSearchOptions {
	opts := m.opts
	opts.Query = strings.TrimSpace(m.input.Value())
	switch m.access {
	case 1:
		gated := false
		opts.Gated = &gated
	case 2:
		gated := true
		opts.Gated = &gated
	default:
		opts.Gated = nil
	}
	return opts
}

func (m *ModelSearchModel) startSearch() tea.Cmd {
	// Any explicit search supersedes a pending debounce from query editing.
	m.debounceID++
	m.requestID++
	id := m.requestID
	m.loading = true
	m.loadingMore = false
	m.err = nil
	m.status = "searching Hugging Face Hub"
	opts := m.searchOptions()
	fetch := func() tea.Msg {
		page, err := m.fetcher(m.ctx, opts, "")
		if err != nil {
			return searchResponseMsg{id: id, err: err}
		}
		return searchResponseMsg{id: id, results: page.Results, next: page.NextPageURL}
	}
	return tea.Batch(fetch, searchSpinnerTick())
}

// maybeLoadMore fetches the next page when the cursor approaches the end of
// the loaded results and the Hub reported more pages.
func (m *ModelSearchModel) maybeLoadMore() tea.Cmd {
	if m.nextPage == "" || m.loading || m.loadingMore {
		return nil
	}
	if len(m.results)-m.cursor > m.resultPageSize() {
		return nil
	}
	m.loadingMore = true
	id := m.requestID
	opts := m.searchOptions()
	pageURL := m.nextPage
	fetch := func() tea.Msg {
		page, err := m.fetcher(m.ctx, opts, pageURL)
		if err != nil {
			return searchResponseMsg{id: id, appends: true, err: err}
		}
		return searchResponseMsg{id: id, appends: true, results: page.Results, next: page.NextPageURL}
	}
	return tea.Batch(fetch, searchSpinnerTick())
}

// resultCountStatus renders the "N models" status, marking when more pages
// are available on the Hub.
func (m *ModelSearchModel) resultCountStatus() string {
	if m.nextPage != "" {
		return fmt.Sprintf("%d models — scroll for more", len(m.results))
	}
	return fmt.Sprintf("%d models", len(m.results))
}

// dedupeNewResults drops entries already present (by ID) so a shifted cursor
// page can never produce duplicate rows.
func dedupeNewResults(existing, incoming []hfdownloader.ModelSearchResult) []hfdownloader.ModelSearchResult {
	seen := make(map[string]struct{}, len(existing))
	for i := range existing {
		seen[existing[i].ID] = struct{}{}
	}
	out := incoming[:0]
	for _, r := range incoming {
		if _, dup := seen[r.ID]; dup {
			continue
		}
		out = append(out, r)
	}
	return out
}

// View implements tea.Model.
func (m *ModelSearchModel) render() string {
	if m.done {
		return ""
	}
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}

	topStatus := m.status
	if m.loading || m.loadingMore {
		topStatus = spinnerFrames[m.spinnerFrame] + " " + topStatus
	}
	top := renderTwoSidedBar("HF MODEL EXPLORER", topStatus, w, SearchTopBarStyle)
	queryPrefix := SearchInputPromptStyle.Render("Search  ")
	query := m.input.View()
	if !m.input.Focused() {
		value := m.input.Value()
		if value == "" {
			value = "all models"
		}
		query = SearchInputTextStyle.Render(value)
	}
	searchLine := fillStyledLine(queryPrefix+query, w, SearchBackgroundStyle)
	filters := m.renderFilters(w)
	separator := SearchHeavySeparatorStyle.Render(strings.Repeat("═", w))

	bodyH := searchMax(1, h-5)
	body := m.renderBody(w, bodyH)
	help := m.renderHelp(w)
	content := strings.Join([]string{top, searchLine, filters, separator, body, help}, "\n")
	return fitScreen(content, w, h, SearchScreenStyle)
}

// View implements tea.Model.
func (m *ModelSearchModel) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	if !m.input.VirtualCursor() {
		view.Cursor = m.input.Cursor()
	}
	return view
}

func (m *ModelSearchModel) renderFilters(width int) string {
	access := []string{"all", "open", "gated"}[m.access]
	filter := func(key, value string) string {
		if value == "" {
			value = "any"
		}
		return SearchFilterKeyStyle.Render(key) + " " + SearchFilterValueStyle.Render(value)
	}
	parts := []string{
		filter("s:sort", m.opts.Sort), filter("t:task", m.opts.PipelineTag),
		filter("l:library", m.opts.Library), filter("g:access", access),
	}
	if m.opts.Author != "" {
		parts = append(parts, filter("author", m.opts.Author))
	}
	line := strings.Join(parts, SearchMutedStyle.Render("  │  "))
	return fillStyledLine(line, width, SearchBackgroundStyle)
}

func (m *ModelSearchModel) renderBody(width, height int) string {
	if width >= 96 {
		detailW := searchMax(32, width/3)
		resultW := searchMax(40, width-detailW-1)
		detailW = width - resultW - 1
		return lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderResults(resultW, height), " ", m.renderDetails(detailW, height))
	}

	resultH := searchMax(6, height*3/5)
	detailH := height - resultH
	if detailH < 4 {
		detailH = 4
		resultH = searchMax(2, height-detailH)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderResults(width, resultH), m.renderDetails(width, detailH))
}

func (m *ModelSearchModel) renderResults(width, height int) string {
	title := fmt.Sprintf("Models  %d", len(m.results))
	contentW, contentH, style := panelGeometry(width, height, true)
	rows := []string{SearchPanelTitleStyle.Render(title)}
	available := searchMax(0, contentH-1)

	if m.err != nil {
		rows = append(rows, SearchErrorStyle.Render(ansi.Truncate(m.err.Error(), contentW, "…")))
	} else if len(m.results) == 0 {
		message := "No matching models"
		if m.loading {
			message = "Searching…"
		}
		rows = append(rows, SearchMutedStyle.Render(message))
	} else {
		start, end := searchWindow(m.cursor, len(m.results), available)
		for i := start; i < end; i++ {
			rows = append(rows, m.renderResultRow(m.results[i], i == m.cursor, contentW))
		}
	}
	if len(rows) > contentH {
		rows = rows[:contentH]
	}
	return style.Render(strings.Join(rows, "\n"))
}

func (m *ModelSearchModel) renderResultRow(model hfdownloader.ModelSearchResult, selected bool, width int) string {
	// Keep table metadata ASCII-only. Ambiguous-width glyphs can wrap a row in
	// terminals whose Unicode width table differs from the renderer's.
	stats := "dl:" + compactNumber(model.Downloads) + "|likes:" + compactNumber(model.Likes)
	metaW := lipgloss.Width(stats)
	idW := searchMax(8, width-metaW-2)
	line := ansi.Truncate(model.ID, idW, "…")
	line += "  " + stats
	line = ansi.Truncate(line, width, "…")
	if selected {
		return SearchSelectedStyle.Width(width).Render(line)
	}
	return SearchResultIDStyle.Render(line)
}

func (m *ModelSearchModel) renderDetails(width, height int) string {
	contentW, contentH, style := panelGeometry(width, height, false)
	rows := []string{SearchPanelTitleStyle.Render("Selected")}
	model := m.selected()
	if model == nil {
		rows = append(rows, SearchMutedStyle.Render("No model under cursor"))
		return style.Render(strings.Join(rows, "\n"))
	}

	add := func(label, value string) {
		if value == "" || len(rows) >= contentH {
			return
		}
		line := SearchDetailLabelStyle.Render(label+": ") + SearchDetailValueStyle.Render(value)
		rows = append(rows, ansi.Truncate(line, contentW, "…"))
	}
	if contentH > 1 {
		rows = append(rows, SearchAccentStyle.Render(ansi.Truncate(model.ID, contentW, "…")))
	}
	add("author", model.Author)
	add("task", model.PipelineTag)
	add("library", model.LibraryName)
	add("downloads", formatInteger(model.Downloads))
	add("likes", formatInteger(model.Likes))
	if model.TrendingScore != 0 {
		add("trending", fmt.Sprintf("%.1f", model.TrendingScore))
	}
	add("updated", friendlyDate(model.LastModified))
	access := "open"
	if model.Private {
		access = "private"
	} else if model.Gated != "" {
		access = string(model.Gated)
	}
	add("access", access)
	if len(model.Tags) > 0 && len(rows) < contentH {
		add("tags", strings.Join(model.Tags, " · "))
	}
	if len(rows) > contentH {
		rows = rows[:contentH]
	}
	return style.Render(strings.Join(rows, "\n"))
}

func (m *ModelSearchModel) renderHelp(width int) string {
	var pairs [][2]string
	if m.input.Focused() {
		pairs = [][2]string{{"type", "edit query"}, {"enter", "browse"}, {"esc", "leave search"}, {"ctrl+c", "quit"}}
	} else {
		pairs = [][2]string{{"↑↓/jk", "move"}, {"/", "search"}, {"s/t/l/g", "filter"}, {"c", "copy cmd"}, {"enter", "inspect"}, {"q", "quit"}}
	}
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, SearchHelpKeyStyle.Render(pair[0])+" "+SearchMutedStyle.Render(pair[1]))
	}
	return fillStyledLine(strings.Join(parts, "  •  "), width, SearchBottomBarStyle)
}

func (m *ModelSearchModel) resultPageSize() int {
	if m.height <= 0 {
		return 10
	}
	return searchMax(1, m.height-8)
}

// Result returns the model browser result after the program exits.
func (m *ModelSearchModel) Result() ModelSearchResult { return m.result }

// RunModelSearch launches the interactive model browser.
func RunModelSearch(ctx context.Context, opts hfdownloader.ModelSearchOptions) (*ModelSearchResult, error) {
	model := NewModelSearchModel(ctx, opts)
	final, err := tea.NewProgram(model, programOptions()...).Run()
	if err != nil {
		return nil, fmt.Errorf("run model search: %w", err)
	}
	result := final.(*ModelSearchModel).Result()
	return &result, nil
}

func searchSpinnerTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return searchSpinnerMsg{} })
}

func panelGeometry(width, height int, focused bool) (int, int, lipgloss.Style) {
	style := SearchPanelStyle
	if focused {
		style = SearchFocusedPanelStyle
	}
	borderW := style.GetBorderLeftSize() + style.GetBorderRightSize()
	borderH := style.GetBorderTopSize() + style.GetBorderBottomSize()
	styleW := searchMax(1, width-borderW)
	styleH := searchMax(1, height-borderH)
	contentW := searchMax(1, styleW-style.GetHorizontalPadding())
	contentH := searchMax(1, styleH-style.GetVerticalPadding())
	return contentW, contentH, style.Width(styleW).Height(styleH)
}

func findModelResult(results []hfdownloader.ModelSearchResult, id string) int {
	if id != "" {
		for i := range results {
			if results[i].ID == id {
				return i
			}
		}
	}
	return 0
}

func cycleSearchValue(current string, values []string) string {
	for i, value := range values {
		if strings.EqualFold(value, current) {
			return values[(i+1)%len(values)]
		}
	}
	return values[0]
}

func searchWindow(cursor, count, height int) (int, int) {
	if count <= 0 || height <= 0 {
		return 0, 0
	}
	height = searchMin(height, count)
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > count {
		start = count - height
	}
	return start, start + height
}

func compactNumber(value int64) string {
	switch {
	case value >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(value)/1_000_000_000)
	case value >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(value)/1_000_000)
	case value >= 1_000:
		return fmt.Sprintf("%.1fK", float64(value)/1_000)
	default:
		return fmt.Sprintf("%d", value)
	}
}

func formatInteger(value int64) string {
	raw := fmt.Sprintf("%d", value)
	for i := len(raw) - 3; i > 0; i -= 3 {
		raw = raw[:i] + "," + raw[i:]
	}
	return raw
}

func friendlyDate(value string) string {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.Format("2006-01-02")
	}
	return value
}

func renderTwoSidedBar(left, right string, width int, style lipgloss.Style) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		right = ansi.Truncate(right, searchMax(0, width-lipgloss.Width(left)-1), "…")
		gap = searchMax(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return fillStyledLine(left+strings.Repeat(" ", gap)+right, width, style)
}

func fillStyledLine(content string, width int, style lipgloss.Style) string {
	content = ansi.Truncate(content, searchMax(0, width), "…")
	if gap := width - lipgloss.Width(content); gap > 0 {
		content += strings.Repeat(" ", gap)
	}
	return style.Render(content)
}

func fitScreen(content string, width, height int, style lipgloss.Style) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", searchMax(0, width)))
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], searchMax(0, width), "")
		if gap := width - lipgloss.Width(lines[i]); gap > 0 {
			lines[i] += strings.Repeat(" ", gap)
		}
	}
	return style.Render(strings.Join(lines, "\n"))
}

func searchMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func searchMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ tea.Model = (*ModelSearchModel)(nil)
