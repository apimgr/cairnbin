package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/common/terminal"
	tea "github.com/charmbracelet/bubbletea"
)

// Tests for list.go: listModel, formatRow, formatPasteRow, formatExpiry, columnsForMode, min

func TestNewListModelInitializesCorrectly(t *testing.T) {
	m, cmd := newListModel("https://example.com", "en")
	if m.server != "https://example.com" {
		t.Errorf("server = %q, want %q", m.server, "https://example.com")
	}
	if m.lang != "en" {
		t.Errorf("lang = %q, want %q", m.lang, "en")
	}
	if !m.loading {
		t.Error("loading should be true after newListModel")
	}
	if cmd == nil {
		t.Error("newListModel should return a non-nil cmd")
	}
}

func TestListModelUpdateWithPastesLoadedMsg(t *testing.T) {
	m, _ := newListModel("https://example.com", "en")
	items := []PasteListItem{
		{ID: "abc123", Title: "Test Paste", Language: "go"},
		{ID: "def456", Title: "Another", Language: "python"},
	}

	updated, cmd := m.update(pastesLoadedMsg{items: items, err: nil})
	if updated.loading {
		t.Error("loading should be false after pastesLoadedMsg")
	}
	if updated.err != nil {
		t.Errorf("err = %v, want nil", updated.err)
	}
	if len(updated.items) != 2 {
		t.Errorf("len(items) = %d, want 2", len(updated.items))
	}
	if len(updated.filtered) != 2 {
		t.Errorf("len(filtered) = %d, want 2", len(updated.filtered))
	}
	if cmd != nil {
		t.Error("cmd should be nil after pastesLoadedMsg")
	}
}

func TestListModelUpdateWithError(t *testing.T) {
	m, _ := newListModel("https://example.com", "en")
	testErr := strings.NewReader("connection refused")

	updated, _ := m.update(pastesLoadedMsg{items: nil, err: &testError{msg: "connection refused"}})
	if updated.loading {
		t.Error("loading should be false after error")
	}
	if updated.err == nil {
		t.Error("err should not be nil")
	}
	_ = testErr
}

// testError is a simple error implementation for testing
type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestListModelNavigationKeys(t *testing.T) {
	tests := []struct {
		name        string
		key         string
		startCursor int
		wantCursor  int
	}{
		{"j moves down from 0", "j", 0, 1},
		{"down moves down from 1", "down", 1, 2},
		{"k moves up from 2", "k", 2, 1},
		{"up moves up from 1", "up", 1, 0},
		{"G goes to end from 0", "G", 0, 4},
		{"g goes to top from 2", "g", 2, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := listModel{
				items:    make([]PasteListItem, 5),
				filtered: make([]PasteListItem, 5),
				cursor:   tc.startCursor,
				loading:  false,
			}
			for i := range m.items {
				m.items[i] = PasteListItem{ID: string(rune('a' + i))}
				m.filtered[i] = m.items[i]
			}
			updated, _ := m.updateNav(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
			if updated.cursor != tc.wantCursor {
				t.Errorf("cursor = %d, want %d", updated.cursor, tc.wantCursor)
			}
		})
	}
}

func TestListModelRefreshKey(t *testing.T) {
	m := listModel{
		items:    make([]PasteListItem, 3),
		filtered: make([]PasteListItem, 3),
		loading:  false,
		server:   "https://test.com",
		lang:     "en",
	}

	updated, cmd := m.updateNav(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if !updated.loading {
		t.Error("loading should be true after 'r' key")
	}
	if cmd == nil {
		t.Error("cmd should not be nil after 'r' key")
	}
}

func TestListModelSearchModeToggle(t *testing.T) {
	m := listModel{searching: false}
	updated, _ := m.updateNav(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !updated.searching {
		t.Error("searching should be true after '/' key")
	}
}

func TestListModelSearchInput(t *testing.T) {
	m := listModel{
		searching:   true,
		searchQuery: "",
		items:       []PasteListItem{{ID: "abc", Title: "Test"}},
		filtered:    []PasteListItem{{ID: "abc", Title: "Test"}},
	}

	// Type a character
	updated, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if updated.searchQuery != "t" {
		t.Errorf("searchQuery = %q, want %q", updated.searchQuery, "t")
	}

	// Type another character
	updated, _ = updated.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if updated.searchQuery != "te" {
		t.Errorf("searchQuery = %q, want %q", updated.searchQuery, "te")
	}
}

func TestListModelSearchBackspace(t *testing.T) {
	m := listModel{
		searching:   true,
		searchQuery: "test",
		items:       []PasteListItem{{ID: "abc", Title: "Test"}},
		filtered:    []PasteListItem{{ID: "abc", Title: "Test"}},
	}

	updated, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyBackspace})
	if updated.searchQuery != "tes" {
		t.Errorf("searchQuery = %q, want %q", updated.searchQuery, "tes")
	}
}

func TestListModelSearchEscape(t *testing.T) {
	m := listModel{
		searching:   true,
		searchQuery: "test",
		items:       []PasteListItem{{ID: "abc", Title: "Test"}},
		filtered:    []PasteListItem{},
	}

	updated, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.searching {
		t.Error("searching should be false after Esc")
	}
	if updated.searchQuery != "" {
		t.Errorf("searchQuery = %q, want empty", updated.searchQuery)
	}
}

func TestListModelSearchEnter(t *testing.T) {
	m := listModel{
		searching:   true,
		searchQuery: "test",
		items:       []PasteListItem{{ID: "abc", Title: "Test"}},
		filtered:    []PasteListItem{{ID: "abc", Title: "Test"}},
	}

	updated, _ := m.updateSearch(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.searching {
		t.Error("searching should be false after Enter")
	}
	if updated.searchQuery != "test" {
		t.Errorf("searchQuery should remain %q", "test")
	}
}

func TestApplySearchFiltersItems(t *testing.T) {
	m := &listModel{
		items: []PasteListItem{
			{ID: "abc123", Title: "Go Code", Language: "go"},
			{ID: "def456", Title: "Python Script", Language: "python"},
			{ID: "ghi789", Title: "Rust Example", Language: "rust"},
		},
		searchQuery: "go",
	}
	m.applySearch()

	// Should match "Go Code" (title) and "abc123" doesn't match, but "go" language matches
	// Both "Go Code" and the go language item should be found
	if len(m.filtered) != 1 {
		t.Errorf("filtered count = %d, want 1", len(m.filtered))
	}
	if len(m.filtered) > 0 && m.filtered[0].Language != "go" {
		t.Errorf("filtered[0].Language = %q, want %q", m.filtered[0].Language, "go")
	}
}

func TestApplySearchEmptyQuery(t *testing.T) {
	m := &listModel{
		items: []PasteListItem{
			{ID: "abc123"},
			{ID: "def456"},
		},
		searchQuery: "",
	}
	m.applySearch()

	if len(m.filtered) != 2 {
		t.Errorf("filtered count = %d, want 2 (all items)", len(m.filtered))
	}
}

func TestClampCursor(t *testing.T) {
	tests := []struct {
		name     string
		filtered int
		cursor   int
		want     int
	}{
		{"negative cursor", 5, -1, 0},
		{"cursor exceeds length", 5, 10, 4},
		{"cursor within range", 5, 2, 2},
		{"empty list", 0, 5, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &listModel{
				filtered: make([]PasteListItem, tc.filtered),
				cursor:   tc.cursor,
			}
			m.clampCursor()
			if m.cursor != tc.want {
				t.Errorf("cursor = %d, want %d", m.cursor, tc.want)
			}
		})
	}
}

func TestSelectedItem(t *testing.T) {
	m := listModel{
		filtered: []PasteListItem{
			{ID: "abc123", Title: "First"},
			{ID: "def456", Title: "Second"},
		},
		cursor: 1,
	}

	item := m.selectedItem()
	if item == nil {
		t.Fatal("selectedItem returned nil")
	}
	if item.ID != "def456" {
		t.Errorf("selectedItem.ID = %q, want %q", item.ID, "def456")
	}
}

func TestSelectedItemEmpty(t *testing.T) {
	m := listModel{filtered: []PasteListItem{}, cursor: 0}
	if m.selectedItem() != nil {
		t.Error("selectedItem should return nil for empty list")
	}
}

func TestSelectedItemOutOfRange(t *testing.T) {
	m := listModel{
		filtered: []PasteListItem{{ID: "abc"}},
		cursor:   5,
	}
	if m.selectedItem() != nil {
		t.Error("selectedItem should return nil when cursor out of range")
	}
}

func TestColumnsForMode(t *testing.T) {
	tests := []struct {
		maxCols int
		want    int
	}{
		{2, 2},
		{3, 3},
		{4, 4},
		{6, 6},
		{10, 6},
		{20, 6},
	}

	for _, tc := range tests {
		// max is 6 columns
		cols := columnsForMode(tc.maxCols)
		if len(cols) != tc.want {
			t.Errorf("columnsForMode(%d) returned %d columns, want %d", tc.maxCols, len(cols), tc.want)
		}
	}
}

func TestFormatExpiry(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{"zero time", time.Time{}, "never"},
		{"valid time", time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC), "2024-06-15"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatExpiry(tc.time)
			if got != tc.want {
				t.Errorf("formatExpiry = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatPasteRow(t *testing.T) {
	item := PasteListItem{
		ID:        "abc123",
		Title:     "Test Paste",
		Language:  "go",
		Views:     42,
		CreatedAt: time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC),
	}

	row := formatPasteRow(item, 6, 100, 60)
	if !strings.Contains(row, "abc123") {
		t.Error("row should contain ID")
	}
	if !strings.Contains(row, "Test Paste") {
		t.Error("row should contain Title")
	}
	if !strings.Contains(row, "go") {
		t.Error("row should contain Language")
	}
	if !strings.Contains(row, "42") {
		t.Error("row should contain Views")
	}
}

func TestFormatPasteRowTruncation(t *testing.T) {
	item := PasteListItem{
		ID:    "abc",
		Title: "This is a very very very very long title that should be truncated",
	}
	row := formatPasteRow(item, 2, 100, 20)
	// Title should be truncated to 17 chars + "..."
	if !strings.Contains(row, "...") {
		t.Error("long title should be truncated with ...")
	}
}

func TestFormatPasteRowUntitled(t *testing.T) {
	item := PasteListItem{ID: "abc", Title: ""}
	row := formatPasteRow(item, 2, 100, 60)
	if !strings.Contains(row, "(untitled)") {
		t.Error("empty title should show (untitled)")
	}
}

func TestFormatRow(t *testing.T) {
	cols := []string{"ID", "TITLE", "LANG"}
	row := formatRow(cols, 3, 100, 60)
	if !strings.Contains(row, "ID") {
		t.Error("row should contain ID column")
	}
	if !strings.Contains(row, "TITLE") {
		t.Error("row should contain TITLE column")
	}
	if !strings.Contains(row, "LANG") {
		t.Error("row should contain LANG column")
	}
}

func TestFormatRowEmpty(t *testing.T) {
	row := formatRow([]string{}, 0, 100, 60)
	if row != "" {
		t.Errorf("formatRow empty should return empty, got %q", row)
	}
}

func TestMinFunction(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 1},
		{5, 3, 3},
		{4, 4, 4},
		{-1, 0, -1},
	}

	for _, tc := range tests {
		got := min(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("min(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestListModelViewLoading(t *testing.T) {
	m := listModel{loading: true}
	styles := StylesFromTerminalPalette(DarkTheme())
	view := m.view(styles, 80, 24, terminal.SizeModeStandard)
	if !strings.Contains(view, "Loading") {
		t.Error("loading view should contain 'Loading'")
	}
}

func TestListModelViewError(t *testing.T) {
	m := listModel{loading: false, err: &testError{msg: "test error"}}
	styles := StylesFromTerminalPalette(DarkTheme())
	view := m.view(styles, 80, 24, terminal.SizeModeStandard)
	if !strings.Contains(view, "Error") {
		t.Error("error view should contain 'Error'")
	}
}

func TestListModelViewWithItems(t *testing.T) {
	m := listModel{
		loading: false,
		filtered: []PasteListItem{
			{ID: "abc123", Title: "Test", Language: "go"},
		},
		cursor: 0,
	}
	styles := StylesFromTerminalPalette(DarkTheme())
	view := m.view(styles, 80, 24, terminal.SizeModeStandard)
	if !strings.Contains(view, "abc123") {
		t.Error("view should contain paste ID")
	}
}

func TestListModelViewEmpty(t *testing.T) {
	m := listModel{loading: false, filtered: []PasteListItem{}}
	styles := StylesFromTerminalPalette(DarkTheme())
	view := m.view(styles, 80, 24, terminal.SizeModeStandard)
	if !strings.Contains(view, "No pastes") {
		t.Error("empty view should indicate no pastes")
	}
}

func TestListModelViewSearching(t *testing.T) {
	m := listModel{
		loading:     false,
		searching:   true,
		searchQuery: "test",
		filtered:    []PasteListItem{{ID: "abc"}},
	}
	styles := StylesFromTerminalPalette(DarkTheme())
	view := m.view(styles, 80, 24, terminal.SizeModeStandard)
	if !strings.Contains(view, "/") || !strings.Contains(view, "test") {
		t.Error("searching view should show search query")
	}
}
