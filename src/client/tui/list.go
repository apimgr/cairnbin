package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/common/terminal"
	tea "github.com/charmbracelet/bubbletea"
)

// listModel is the sub-model for the paste list view.
type listModel struct {
	items        []PasteListItem
	filtered     []PasteListItem
	cursor       int
	scrollOffset int
	searching    bool
	searchQuery  string
	loading      bool
	err          error
	server       string
	lang         string
}

// pastesLoadedMsg carries the result of a fetchPastes call.
type pastesLoadedMsg struct {
	items []PasteListItem
	err   error
}

// newListModel creates an initialised list sub-model and fires the first load.
func newListModel(server, lang string) (listModel, tea.Cmd) {
	m := listModel{
		server:  server,
		lang:    lang,
		loading: true,
	}
	return m, m.loadCmd()
}

// loadCmd returns the tea.Cmd that fetches the paste list.
func (m listModel) loadCmd() tea.Cmd {
	server := m.server
	lang := m.lang
	return func() tea.Msg {
		items, err := fetchPastes(server, lang, 1, 50)
		return pastesLoadedMsg{items: items, err: err}
	}
}

// applySearch filters items by the current search query.
func (m *listModel) applySearch() {
	if m.searchQuery == "" {
		m.filtered = m.items
		return
	}
	q := strings.ToLower(m.searchQuery)
	var out []PasteListItem
	for _, it := range m.items {
		if strings.Contains(strings.ToLower(it.ID), q) ||
			strings.Contains(strings.ToLower(it.Title), q) ||
			strings.Contains(strings.ToLower(it.Language), q) {
			out = append(out, it)
		}
	}
	m.filtered = out
}

// clampCursor keeps the cursor in range.
func (m *listModel) clampCursor() {
	n := len(m.filtered)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
}

// update processes messages for the list sub-model.
func (m listModel) update(msg tea.Msg) (listModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pastesLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.items = msg.items
			m.applySearch()
		}
		return m, nil

	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(msg)
		}
		return m.updateNav(msg)
	}
	return m, nil
}

// updateSearch handles key input while in search mode.
func (m listModel) updateSearch(msg tea.KeyMsg) (listModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.searching = false
		m.searchQuery = ""
		m.applySearch()
		m.cursor = 0
	case tea.KeyEnter:
		m.searching = false
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.searchQuery) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.searchQuery)
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-size]
			m.applySearch()
			m.cursor = 0
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.searchQuery += string(msg.Runes)
			m.applySearch()
			m.cursor = 0
		}
	}
	return m, nil
}

// updateNav handles navigation key input in normal mode.
func (m listModel) updateNav(msg tea.KeyMsg) (listModel, tea.Cmd) {
	n := len(m.filtered)
	switch msg.String() {
	case "j", "down":
		m.cursor++
		m.clampCursor()
	case "k", "up":
		m.cursor--
		m.clampCursor()
	case "g":
		m.cursor = 0
	case "G":
		if n > 0 {
			m.cursor = n - 1
		}
	case "r":
		m.loading = true
		m.err = nil
		return m, m.loadCmd()
	case "/":
		m.searching = true
	}
	return m, nil
}

// selectedItem returns the currently highlighted paste or nil.
func (m listModel) selectedItem() *PasteListItem {
	if len(m.filtered) == 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	it := m.filtered[m.cursor]
	return &it
}

// view renders the paste list.
func (m listModel) view(styles TUIStyles, width, height int, sizeMode terminal.SizeMode) string {
	if m.loading {
		return styles.Muted.Render(t("list_loading"))
	}
	if m.err != nil {
		return styles.Error.Render(tf("list_error", "error", m.err.Error()))
	}

	cfg := GetLayoutConfig(sizeMode)

	var sb strings.Builder

	// Search bar.
	if m.searching {
		sb.WriteString(styles.Header.Render("/") + m.searchQuery + "█\n")
	} else if m.searchQuery != "" {
		sb.WriteString(styles.Muted.Render(tf("list_search_hint", "query", m.searchQuery)) + "\n")
	}

	if len(m.filtered) == 0 {
		sb.WriteString(styles.Muted.Render(t("list_empty")))
		return sb.String()
	}

	// Column headers based on layout.
	headers := columnsForMode(cfg.MaxColumns)
	if cfg.ShowBorders {
		sb.WriteString(styles.Header.Render(formatRow(headers, cfg.MaxColumns, width, cfg.TruncateAt)) + "\n")
		sb.WriteString(styles.Muted.Render(strings.Repeat("─", min(width-2, 80))) + "\n")
	} else {
		sb.WriteString(styles.Muted.Render(formatRow(headers, cfg.MaxColumns, width, cfg.TruncateAt)) + "\n")
	}

	// Compute visible rows.
	listHeight := height - 5
	if listHeight < 1 {
		listHeight = 1
	}

	// Adjust scroll offset so cursor is visible.
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}
	if m.cursor >= m.scrollOffset+listHeight {
		m.scrollOffset = m.cursor - listHeight + 1
	}

	end := m.scrollOffset + listHeight
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	for i := m.scrollOffset; i < end; i++ {
		item := m.filtered[i]
		row := formatPasteRow(item, cfg.MaxColumns, width, cfg.TruncateAt)
		if i == m.cursor {
			sb.WriteString(styles.Selected.Render(row) + "\n")
		} else {
			sb.WriteString(styles.Normal.Render(row) + "\n")
		}
	}

	return sb.String()
}

// columnsForMode returns the header column labels for the given max column count.
func columnsForMode(maxCols int) []string {
	all := []string{t("list_col_id"), t("list_col_title"), t("list_col_lang"), t("list_col_views"), t("list_col_created"), t("list_col_expires")}
	if maxCols >= len(all) {
		return all
	}
	return all[:maxCols]
}

// formatPasteRow formats a single paste as a row string for the given column count.
func formatPasteRow(item PasteListItem, maxCols, width, truncAt int) string {
	title := item.Title
	// truncAt <= 0 means "no truncation" (AI.md SizeModeMassive: TruncateAt: 0).
	if truncAt > 3 && len(title) > truncAt {
		title = title[:truncAt-3] + "..."
	}
	if title == "" {
		title = t("list_untitled")
	}

	cols := []string{
		item.ID,
		title,
		item.Language,
		fmt.Sprintf("%d", item.Views),
		item.CreatedAt.Format("2006-01-02"),
		formatExpiry(item.ExpiresAt),
	}

	if maxCols < len(cols) {
		cols = cols[:maxCols]
	}
	return formatRow(cols, maxCols, width, truncAt)
}

// formatRow joins columns with padding.
func formatRow(cols []string, maxCols, width, _ int) string {
	if len(cols) == 0 {
		return ""
	}

	// Fixed widths per column index.
	colWidths := []int{10, 30, 10, 6, 12, 12}
	var parts []string
	for i, c := range cols {
		w := 10
		if i < len(colWidths) {
			w = colWidths[i]
		}
		if len(c) > w {
			c = c[:w-1] + "…"
		}
		parts = append(parts, fmt.Sprintf("%-*s", w, c))
	}
	return strings.Join(parts, " ")
}

// formatExpiry returns a short string for the expiry time.
func formatExpiry(exp time.Time) string {
	if exp.IsZero() {
		return t("list_expiry_never")
	}
	return exp.Format("2006-01-02")
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
