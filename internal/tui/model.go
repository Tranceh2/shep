// Package tui is shep's embedded Bubble Tea fuzzy picker. It is the universal
// interactive fallback in the `shep open` selector cascade, used when there
// is no exact match and fzf is unavailable.
//
// The model renders a left list of filtered candidates and a right preview
// showing the highlighted candidate's path + source. Filtering is
// case-insensitive subsequence scoring over label+path. Navigation uses
// up/down/j/k; enter selects; esc/q/ctrl+c cancels. The palette is Catppuccin
// Mocha, centralised in palette.go so colors live in one place.
package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tranceh2/shep/internal/source"
)

// Model is the Bubble Tea model for the shep picker. It owns the candidate
// list, the filtered view, the query text, the cursor and the final pick.
type Model struct {
	candidates []source.Candidate
	filtered   []int // indices into candidates
	query      string
	cursor     int
	width      int
	height     int
	selected   int // -1 until a candidate is chosen
	cancelled  bool
}

// NewModel builds a model over the supplied candidates. The filtered view is
// initialised to every candidate in order; width/height are populated by the
// first WindowSizeMsg.
func NewModel(candidates []source.Candidate) Model {
	m := Model{
		candidates: make([]source.Candidate, len(candidates)),
		filtered:   make([]int, len(candidates)),
		selected:   -1,
	}
	copy(m.candidates, candidates)
	for i := range candidates {
		m.filtered[i] = i
	}
	return m
}

// Selected returns the chosen candidate and ok=true after enter is pressed.
// ok=false means the user cancelled or has not selected yet.
func (m Model) Selected() (source.Candidate, bool) {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		return source.Candidate{}, false
	}
	idx := m.filtered[m.selected]
	if idx < 0 || idx >= len(m.candidates) {
		return source.Candidate{}, false
	}
	return m.candidates[idx], true
}

// Cancelled reports whether the user quit without selecting (esc/q/ctrl+c).
func (m Model) Cancelled() bool { return m.cancelled }

// Init is a no-op; shep's TUI has no initial command.
func (m Model) Init() tea.Cmd { return nil }

// Update handles key presses and window sizing. It mutates a copy of the
// model and returns it; the Bubble Tea runtime replaces the model with the
// returned value.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if len(m.filtered) > 0 {
				m.selected = m.cursor
				return m, tea.Quit
			}
		case "esc", "q", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case "down", "j":
			if len(m.filtered) > 0 && m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "backspace":
			if len(m.query) > 0 {
				m.query = m.query[:len(m.query)-1]
				m.applyFilter()
			}
		default:
			// Any other printable rune is appended to the query and re-filters.
			if isPrintable(msg.String()) {
				m.query += msg.String()
				m.applyFilter()
			}
		}
	}
	return m, nil
}

// applyFilter recomputes the filtered indices from the query using
// case-insensitive subsequence matching over "label path". The cursor is
// clamped back into range so an empty result never leaves a dangling cursor.
func (m *Model) applyFilter() {
	m.filtered = m.filtered[:0]
	needle := strings.ToLower(m.query)
	for i, c := range m.candidates {
		hay := strings.ToLower(c.Label + " " + c.Path)
		if needle == "" || subsequence(needle, hay) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(len(m.filtered)-1, 0)
	}
	// scoring: stable order preserved; subsequence match is enough for v1.
}

// subsequence reports whether every rune of needle appears in haystack in
// order (case already normalised by the caller). Used for fuzzy filtering.
func subsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	ni := 0
	for hi := 0; hi < len(haystack) && ni < len(needle); hi++ {
		if haystack[hi] == needle[ni] {
			ni++
		}
	}
	return ni == len(needle)
}

// isPrintable returns true for single-rune printable input that should extend
// the query. We avoid pulling in unicode classes for the v1 picker.
func isPrintable(s string) bool {
	if s == "" || len([]rune(s)) != 1 {
		return false
	}
	r := []rune(s)[0]
	return r >= 0x20 && r != 0x7f
}

// View renders the two-pane UI: a left candidate list with the cursor and a
// right preview of the highlighted candidate. Widths auto-balance based on
// the reported window size (falling back to 60/40 when no size yet).
func (m Model) View() string {
	listW, prevW := splitWidths(m.width)
	listPane := m.renderList(listW)
	previewPane := m.renderPreview(prevW)
	return lipgloss.JoinHorizontal(lipgloss.Top, listPane, gap(), previewPane)
}

func splitWidths(width int) (int, int) {
	if width <= 0 {
		width = 80
	}
	list := width * 3 / 5
	if list < 20 {
		list = 20
	}
	prev := width - list - 1
	if prev < 10 {
		prev = 10
	}
	return list, prev
}

func gap() string { return " " }

// renderList draws the filtered candidates with a cursor marker and the query
// line at the top. The highlighted row uses the accent palette.
func (m Model) renderList(width int) string {
	var b strings.Builder
	styleQuery := palette.queryStyle.Width(width)
	b.WriteString(styleQuery.Render("> " + m.query))
	b.WriteString("\n")
	if len(m.filtered) == 0 {
		b.WriteString(palette.mutedStyle.Render("  no matches"))
		b.WriteString("\n")
		return b.String()
	}
	// Cap visible rows to a sane height when we know it.
	visible := m.filtered
	maxRows := m.height
	if maxRows <= 0 {
		maxRows = len(visible)
	}
	if len(visible) > maxRows && maxRows > 2 {
		visible = visible[clamp(m.cursor-maxRows/2, 0, len(visible)-maxRows):]
		if len(visible) > maxRows {
			visible = visible[:maxRows]
		}
	}
	for i, candIdx := range visible {
		c := m.candidates[candIdx]
		marker := "  "
		row := c.Label
		if row == "" {
			row = c.Path
		}
		if i == m.cursor {
			marker = " >"
			b.WriteString(palette.cursorStyle.Render(marker + " " + row))
		} else {
			b.WriteString(palette.rowStyle.Render(marker + " " + row))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderPreview shows the highlighted candidate's path and source plus a
// short help line so the user always knows the keybindings.
func (m Model) renderPreview(width int) string {
	header := palette.previewHeaderStyle.Width(width).Render("preview")
	body := palette.mutedStyle.Width(width).Render("(no selection)")
	if len(m.filtered) > 0 {
		idx := m.cursor
		if idx < 0 || idx >= len(m.filtered) {
			idx = 0
		}
		c := m.candidates[m.filtered[idx]]
		body = lipgloss.NewStyle().Width(width).Render(
			palette.labelStyle.Render("label  ") + c.Label + "\n" +
				palette.labelStyle.Render("path   ") + c.Path + "\n" +
				palette.labelStyle.Render("source ") + c.Source,
		)
	}
	help := palette.mutedStyle.Width(width).Render("enter select  esc cancel  j/k move")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, "", help)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Run drives the model through a Bubble Tea program and returns the selected
// candidate. The query seeds the live filter so users get a head-start (the
// fzf path forwards a query the same way). It is the entry point used by the
// selector's TUI selector.
func Run(ctx context.Context, candidates []source.Candidate, query string) (source.Candidate, bool, error) {
	m := NewModel(candidates)
	m.query = query
	m.applyFilter()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return source.Candidate{}, false, err
	}
	res, ok := final.(Model).Selected()
	return res, ok && !final.(Model).Cancelled(), nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
