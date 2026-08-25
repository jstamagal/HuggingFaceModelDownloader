// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

// LiveRenderer displays download progress. On a terminal it runs a Bubble Tea
// program with styled progress bars, per-file speed and ETA; without a
// terminal it degrades to plain, event-driven log lines (one line per file,
// no repeated frames), which keeps piped output scriptable.
type LiveRenderer struct {
	prog  *tea.Program
	model *downloadModel
	runWG sync.WaitGroup

	plainMu sync.Mutex
	plain   bool
}

// NewLiveRenderer creates a progress renderer for a download job.
func NewLiveRenderer(job hfdownloader.Job, cfg hfdownloader.Settings) *LiveRenderer {
	lr := &LiveRenderer{}
	if !term.IsTerminal(int(os.Stdout.Fd())) || os.Getenv("TERM") == "dumb" {
		lr.plain = true
		return lr
	}

	lr.model = newDownloadModel(job, cfg)
	opts := []tea.ProgramOption{tea.WithOutput(os.Stdout)}
	opts = append(opts, programOptions()...)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// No interactive input available; rely on SIGINT for cancellation.
		opts = append(opts, tea.WithInput(nil))
	}
	lr.prog = tea.NewProgram(lr.model, opts...)
	lr.runWG.Add(1)
	go func() {
		defer lr.runWG.Done()
		if _, err := lr.prog.Run(); err != nil {
			// Fall back to plain output if the TUI cannot start.
			lr.plainMu.Lock()
			lr.plain = true
			lr.plainMu.Unlock()
		}
	}()
	return lr
}

// Handler returns a ProgressFunc that feeds events to the renderer.
// Safe to call from multiple goroutines.
func (lr *LiveRenderer) Handler() hfdownloader.ProgressFunc {
	return func(ev hfdownloader.ProgressEvent) {
		lr.plainMu.Lock()
		plain := lr.plain
		lr.plainMu.Unlock()
		if plain {
			lr.printPlain(ev)
			return
		}
		lr.prog.Send(ev)
	}
}

// Close stops the renderer, leaving the final summary frame on screen.
func (lr *LiveRenderer) Close() {
	if lr.prog != nil {
		lr.prog.Send(downloadFinishedMsg{})
		lr.runWG.Wait()
	}
}

// printPlain emits one log line per meaningful event (no progress spam).
func (lr *LiveRenderer) printPlain(ev hfdownloader.ProgressEvent) {
	lr.plainMu.Lock()
	defer lr.plainMu.Unlock()
	switch ev.Event {
	case "scan_start":
		fmt.Println("scanning repository ...")
	case "file_start":
		fmt.Printf("downloading: %s (%s)\n", ev.Path, humanBytes(ev.Total))
	case "file_done":
		if strings.HasPrefix(ev.Message, "skip") {
			fmt.Printf("skip: %s (%s)\n", ev.Path, ev.Message)
		} else {
			fmt.Printf("done: %s\n", ev.Path)
		}
	case "retry":
		fmt.Printf("retry %s (attempt %d): %s\n", ev.Path, ev.Attempt, ev.Message)
	case "error":
		fmt.Fprintf(os.Stderr, "error: %s\n", ev.Message)
	case "done":
		fmt.Println(ev.Message)
	}
}

// --- Bubble Tea model ---

type downloadFinishedMsg struct{}
type progressTickMsg struct{}

type fileState struct {
	path    string
	total   int64
	bytes   int64
	status  string // "queued","downloading","done","skip","error"
	err     string
	started time.Time

	lastBytes     int64
	lastTime      time.Time
	smoothedSpeed float64
}

// EMA smoothing factor (0.1 = very smooth, 0.5 = responsive).
const speedSmoothingFactor = 0.3

func smoothSpeed(current, previous float64) float64 {
	if previous == 0 {
		return current
	}
	return speedSmoothingFactor*current + (1-speedSmoothingFactor)*previous
}

type downloadModel struct {
	job hfdownloader.Job
	cfg hfdownloader.Settings

	files map[string]*fileState
	order []string // stable file ordering as planned

	totalBar progress.Model
	fileBar  progress.Model

	width    int
	start    time.Time
	scanning bool
	finished bool
	doneMsg  string
	errors   []string

	lastTotalBytes int64
	lastTick       time.Time
	smoothedSpeed  float64
}

func newDownloadModel(job hfdownloader.Job, cfg hfdownloader.Settings) *downloadModel {
	newBar := func() progress.Model {
		b := progress.New(
			progress.WithColors(lipgloss.Color("#5A56E0"), lipgloss.Color("#EE6FF8")),
			progress.WithoutPercentage(),
			progress.WithWidth(30),
		)
		return b
	}
	return &downloadModel{
		job:      job,
		cfg:      cfg,
		files:    map[string]*fileState{},
		totalBar: newBar(),
		fileBar:  newBar(),
		start:    time.Now(),
		scanning: true,
		width:    100,
	}
}

func (m *downloadModel) Init() tea.Cmd {
	return tea.Batch(progressTick(), tea.RequestBackgroundColor)
}

func progressTick() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return progressTickMsg{} })
}

func (m *downloadModel) ensure(path string) *fileState {
	if fs, ok := m.files[path]; ok {
		return fs
	}
	fs := &fileState{path: path, status: "queued"}
	m.files[path] = fs
	m.order = append(m.order, path)
	return fs
}

func (m *downloadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		setBackgroundTheme(msg.IsDark())
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyPressMsg:
		// Only ctrl+c cancels — a stray letter key must never abort a
		// multi-hour download.
		if msg.String() == "ctrl+c" {
			// Cancel the download through the process signal path so the
			// engine shuts down gracefully and persists resume state.
			// (os.Process.Signal is portable, unlike syscall.Kill.)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(os.Interrupt)
			}
		}
		return m, nil

	case progressTickMsg:
		if m.finished {
			return m, nil
		}
		return m, progressTick()

	case downloadFinishedMsg:
		m.finished = true
		return m, tea.Quit

	case hfdownloader.ProgressEvent:
		m.apply(msg)
		return m, nil
	}
	return m, nil
}

func (m *downloadModel) apply(ev hfdownloader.ProgressEvent) {
	switch ev.Event {
	case "scan_start":
		m.scanning = true
	case "plan_item":
		m.scanning = false
		fs := m.ensure(ev.Path)
		fs.total = ev.Total
	case "file_start":
		fs := m.ensure(ev.Path)
		if ev.Total > 0 {
			fs.total = ev.Total
		}
		fs.status = "downloading"
		if fs.started.IsZero() {
			fs.started = time.Now()
		}
	case "file_progress":
		fs := m.ensure(ev.Path)
		if ev.Total > 0 {
			fs.total = ev.Total
		}
		if ev.Downloaded > 0 {
			fs.bytes = ev.Downloaded
		} else if ev.Bytes > 0 {
			fs.bytes = ev.Bytes
		}
	case "file_done":
		fs := m.ensure(ev.Path)
		if strings.HasPrefix(strings.ToLower(ev.Message), "skip") {
			fs.status = "skip"
		} else {
			fs.status = "done"
		}
		fs.bytes = fs.total
	case "error":
		if ev.Path != "" {
			fs := m.ensure(ev.Path)
			fs.status = "error"
			fs.err = ev.Message
		}
		m.errors = append(m.errors, ev.Message)
	case "done":
		m.doneMsg = ev.Message
	}
}

// speeds computes the overall EMA speed; called from View via tick cadence.
func (m *downloadModel) overallSpeed(aggBytes int64) float64 {
	now := time.Now()
	if m.lastTick.IsZero() {
		m.lastTick = now
		m.lastTotalBytes = aggBytes
		return m.smoothedSpeed
	}
	dt := now.Sub(m.lastTick).Seconds()
	if dt < 0.1 {
		return m.smoothedSpeed
	}
	instant := float64(aggBytes-m.lastTotalBytes) / dt
	if instant >= 0 {
		m.smoothedSpeed = smoothSpeed(instant, m.smoothedSpeed)
	}
	m.lastTick = now
	m.lastTotalBytes = aggBytes
	return m.smoothedSpeed
}

func (m *downloadModel) fileSpeed(fs *fileState) float64 {
	now := time.Now()
	if fs.lastTime.IsZero() {
		fs.lastTime = now
		fs.lastBytes = fs.bytes
		return fs.smoothedSpeed
	}
	dt := now.Sub(fs.lastTime).Seconds()
	if dt < 0.1 {
		return fs.smoothedSpeed
	}
	instant := float64(fs.bytes-fs.lastBytes) / dt
	if instant >= 0 {
		fs.smoothedSpeed = smoothSpeed(instant, fs.smoothedSpeed)
	}
	fs.lastTime = now
	fs.lastBytes = fs.bytes
	return fs.smoothedSpeed
}

func (m *downloadModel) render() string {
	w := m.width
	if w < 60 {
		w = 60
	}

	var (
		aggBytes, aggTotal int64
		active             []*fileState
		doneCnt, skipCnt   int
		errCnt, queuedCnt  int
	)
	for _, path := range m.order {
		fs := m.files[path]
		aggTotal += fs.total
		switch fs.status {
		case "downloading":
			active = append(active, fs)
			aggBytes += fs.bytes
		case "done", "skip":
			aggBytes += fs.total
			if fs.status == "done" {
				doneCnt++
			} else {
				skipCnt++
			}
		case "error":
			errCnt++
			aggBytes += fs.bytes
		default:
			queuedCnt++
		}
	}

	rev := m.job.Revision
	if rev == "" {
		rev = "main"
	}
	var b strings.Builder

	title := SearchAccentStyle.Render(m.job.Repo)
	if rev != "main" {
		title += SearchMutedStyle.Render(" @ " + rev)
	}
	b.WriteString(title + "\n")

	if m.scanning && len(m.order) == 0 {
		b.WriteString(SearchMutedStyle.Render("scanning repository ...") + "\n")
		return b.String()
	}

	// Overall progress line.
	speed := m.overallSpeed(aggBytes)
	pct := 0.0
	if aggTotal > 0 {
		pct = float64(aggBytes) / float64(aggTotal)
		if pct > 1 {
			pct = 1
		}
	}
	eta := "--:--"
	if speed > 0 && aggBytes < aggTotal {
		eta = fmtDuration(time.Duration(float64(aggTotal-aggBytes)/speed) * time.Second)
	}
	m.totalBar.SetWidth(clampInt(w/3, 10, 40))
	b.WriteString(fmt.Sprintf("%s %3.0f%%  %s / %s  %s/s  ETA %s\n",
		m.totalBar.ViewAs(pct), pct*100,
		humanBytes(aggBytes), humanBytes(aggTotal),
		humanBytes(int64(speed)), eta))

	counts := fmt.Sprintf("%d files: %d done, %d active, %d queued", len(m.order), doneCnt, len(active), queuedCnt)
	if skipCnt > 0 {
		counts += fmt.Sprintf(", %d skipped", skipCnt)
	}
	if errCnt > 0 {
		counts += fmt.Sprintf(", %d failed", errCnt)
	}
	b.WriteString(SearchMutedStyle.Render(counts) + "\n")

	// Active file rows (largest first, capped).
	sort.Slice(active, func(i, j int) bool { return active[i].total > active[j].total })
	maxRows := 8
	for i, fs := range active {
		if i >= maxRows {
			b.WriteString(SearchMutedStyle.Render(fmt.Sprintf("  ... and %d more active files", len(active)-maxRows)) + "\n")
			break
		}
		filePct := 0.0
		if fs.total > 0 {
			filePct = float64(fs.bytes) / float64(fs.total)
			if filePct > 1 {
				filePct = 1
			}
		}
		fspeed := m.fileSpeed(fs)
		fileEta := "--:--"
		if fspeed > 0 && fs.bytes < fs.total {
			fileEta = fmtDuration(time.Duration(float64(fs.total-fs.bytes)/fspeed) * time.Second)
		}
		m.fileBar.SetWidth(clampInt(w/5, 8, 24))
		nameW := clampInt(w-m.fileBar.Width()-46, 16, 60)
		name := ellipsizeMiddle(fs.path, nameW)
		b.WriteString(fmt.Sprintf("  %s %s %3.0f%%  %9s/s  ETA %s\n",
			name, m.fileBar.ViewAs(filePct), filePct*100,
			humanBytes(int64(fspeed)), fileEta))
	}

	// Errors (deduplicated tail).
	if n := len(m.errors); n > 0 {
		last := m.errors[n-1]
		b.WriteString(ErrorStyle.Render("error: ") + ansi.Truncate(last, w-8, "...") + "\n")
	}

	if m.doneMsg != "" {
		b.WriteString(SuccessStyle.Render(m.doneMsg) +
			SearchMutedStyle.Render(fmt.Sprintf("  in %s", fmtDuration(time.Since(m.start)))) + "\n")
	} else {
		b.WriteString(SearchMutedStyle.Render("ctrl+c to cancel (resume is safe)") + "\n")
	}
	return b.String()
}

// View implements tea.Model.
func (m *downloadModel) View() tea.View {
	return tea.NewView(m.render())
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ellipsizeMiddle(s string, w int) string {
	if w <= 3 || utf8.RuneCountInString(s) <= w {
		return pad(s, w)
	}
	runes := []rune(s)
	half := (w - 3) / 2
	if 2*half+3 > len(runes) {
		return pad(s, w)
	}
	return pad(string(runes[:half])+"..."+string(runes[len(runes)-half:]), w)
}

func pad(s string, w int) string {
	r := utf8.RuneCountInString(s)
	if r >= w {
		return s
	}
	return s + strings.Repeat(" ", w-r)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 6 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	mn := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, mn, s)
	}
	return fmt.Sprintf("%02d:%02d", mn, s)
}

var _ tea.Model = (*downloadModel)(nil)

// Ensure lipgloss is referenced (styles come from styles.go).
var _ = lipgloss.NewStyle
