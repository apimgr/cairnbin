package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Tests for tui.go: Model, newModel, Init, Update, View, checkServerURL, delegateUpdate, handleEsc

func TestCheckServerURLValid(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"http://example.com", true},
		{"https://example.com", true},
		{"https://paste.example.com/api", true},
		{"", false},
		{"example.com", false},
		{"ftp://example.com", false},
		{"http:", false},
	}

	for _, tc := range tests {
		got := checkServerURL(tc.url)
		if got != tc.want {
			t.Errorf("checkServerURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestNewModelWithValidServer(t *testing.T) {
	cfg := ClientConfig{
		Server:  "https://paste.example.com",
		Lang:    "en",
		SaveURL: func(s string) error { return nil },
		CfgPath: "/path/to/cli.yml",
	}

	m := newModel(cfg)
	if m.state != stateListing {
		t.Errorf("state = %v, want stateListing", m.state)
	}
	if m.cfg.Server != "https://paste.example.com" {
		t.Errorf("cfg.Server = %q", m.cfg.Server)
	}
}

func TestNewModelWithoutServer(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		Lang:    "en",
		SaveURL: func(s string) error { return nil },
	}

	m := newModel(cfg)
	if m.state != stateSetup {
		t.Errorf("state = %v, want stateSetup", m.state)
	}
}

func TestNewModelWithInvalidServer(t *testing.T) {
	cfg := ClientConfig{
		Server:  "invalid-url",
		Lang:    "en",
		SaveURL: func(s string) error { return nil },
	}

	m := newModel(cfg)
	if m.state != stateSetup {
		t.Errorf("state = %v, want stateSetup for invalid URL", m.state)
	}
}

func TestModelInitListingState(t *testing.T) {
	cfg := ClientConfig{
		Server: "https://example.com",
		Lang:   "en",
	}
	m := newModel(cfg)
	cmd := m.Init()

	// In listing state, Init should return a load command
	if cmd == nil {
		t.Error("Init should return a cmd when in listing state")
	}
}

func TestModelInitSetupState(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	cmd := m.Init()

	// In setup state, Init should return nil
	if cmd != nil {
		t.Error("Init should return nil when in setup state")
	}
}

func TestModelUpdateWindowSizeMsg(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)

	newM, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model := newM.(Model)

	if model.width != 120 {
		t.Errorf("width = %d, want 120", model.width)
	}
	if model.height != 40 {
		t.Errorf("height = %d, want 40", model.height)
	}
	if cmd != nil {
		t.Error("cmd should be nil for WindowSizeMsg")
	}
}

func TestModelUpdateQuitKey(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q key should return a cmd")
	}

	// Execute the command to check it's Quit
	msg := cmd()
	if msg != tea.Quit() {
		t.Error("q key should trigger tea.Quit")
	}
}

func TestModelUpdateQuitKeyInSearchMode(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.list.searching = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	// In search mode, q should type a character, not quit
	// The cmd might be nil or delegate to list
	// Just ensure it doesn't quit
	if cmd != nil {
		msg := cmd()
		if msg == tea.Quit() {
			t.Error("q in search mode should not quit")
		}
	}
}

func TestModelUpdateCtrlCQuits(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C should return a cmd")
	}
	msg := cmd()
	if msg != tea.Quit() {
		t.Error("Ctrl+C should trigger tea.Quit")
	}
}

func TestModelUpdateHelpKeyTogglesHelp(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model := newM.(Model)

	if model.state != stateHelp {
		t.Errorf("state = %v, want stateHelp", model.state)
	}
	if model.prevState != stateListing {
		t.Errorf("prevState = %v, want stateListing", model.prevState)
	}
}

func TestModelUpdateHelpKeyClosesHelp(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp
	m.prevState = stateListing

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	model := newM.(Model)

	if model.state != stateListing {
		t.Errorf("state = %v, want stateListing", model.state)
	}
}

func TestModelUpdateEscFromHelp(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp
	m.prevState = stateDetail

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := newM.(Model)

	if model.state != stateDetail {
		t.Errorf("state = %v, want stateDetail", model.state)
	}
}

func TestModelUpdateEscFromDetail(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := newM.(Model)

	if model.state != stateListing {
		t.Errorf("state = %v, want stateListing", model.state)
	}
}

func TestModelUpdateEscFromSearchMode(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.list.searching = true
	m.list.searchQuery = "test"

	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model := newM.(Model)

	if model.state != stateListing {
		t.Errorf("state = %v, want stateListing", model.state)
	}
	if model.list.searching {
		t.Error("searching should be false after Esc")
	}
	if model.list.searchQuery != "" {
		t.Error("searchQuery should be cleared after Esc")
	}
}

func TestModelUpdateServerURLSavedMsg(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	m.state = stateSetup

	newM, cmd := m.Update(serverURLSavedMsg{url: "https://new.example.com"})
	model := newM.(Model)

	if model.cfg.Server != "https://new.example.com" {
		t.Errorf("cfg.Server = %q", model.cfg.Server)
	}
	if model.state != stateListing {
		t.Errorf("state = %v, want stateListing", model.state)
	}
	if cmd == nil {
		t.Error("cmd should not be nil after URL saved")
	}
}

func TestModelUpdateServerURLErrorMsg(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	m.state = stateSetup

	newM, _ := m.Update(serverURLErrorMsg{err: &testError{msg: "save failed"}})
	model := newM.(Model)

	if model.setup.err == "" {
		t.Error("setup.err should be set")
	}
	if !strings.Contains(model.setup.err, "save failed") {
		t.Errorf("setup.err = %q", model.setup.err)
	}
}

func TestModelUpdatePastesLoadedMsg(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing

	items := []PasteListItem{{ID: "abc123"}}
	newM, _ := m.Update(pastesLoadedMsg{items: items})
	model := newM.(Model)

	if len(model.list.items) != 1 {
		t.Errorf("list.items = %d, want 1", len(model.list.items))
	}
}

func TestModelUpdatePasteRawMsg(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail
	m.detail.loading = true

	newM, _ := m.Update(pasteRawMsg{content: "Hello", err: nil})
	model := newM.(Model)

	if model.detail.loading {
		t.Error("detail.loading should be false")
	}
	if model.detail.content != "Hello" {
		t.Errorf("detail.content = %q", model.detail.content)
	}
}

func TestModelUpdatePasteDeletedMsg(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail
	m.detail.deleting = true

	newM, _ := m.Update(pasteDeletedMsg{id: "abc123"})
	model := newM.(Model)

	if model.detail.deleting {
		t.Error("detail.deleting should be false")
	}
	if model.detail.deleteSuccess == "" {
		t.Error("detail.deleteSuccess should be set")
	}
}

func TestModelUpdatePasteDeleteErrMsg(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail
	m.detail.deleting = true

	newM, _ := m.Update(pasteDeleteErrMsg{err: &testError{msg: "forbidden"}})
	model := newM.(Model)

	if model.detail.deleting {
		t.Error("detail.deleting should be false")
	}
	if model.detail.deleteErr == "" {
		t.Error("detail.deleteErr should be set")
	}
}

func TestDelegateUpdateSetup(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	m.state = stateSetup

	newM, _ := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	// Just ensure no crash
	_ = newM
}

func TestDelegateUpdateListing(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.list.items = []PasteListItem{{ID: "abc123"}}
	m.list.filtered = m.list.items
	m.list.loading = false

	newM, _ := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	_ = newM
}

func TestDelegateUpdateListingEnterOpensDetail(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.list.items = []PasteListItem{{ID: "abc123", Title: "Test"}}
	m.list.filtered = m.list.items
	m.list.loading = false
	m.list.cursor = 0

	newM, cmd := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model := newM.(Model)

	if model.state != stateDetail {
		t.Errorf("state = %v, want stateDetail", model.state)
	}
	if model.detail.pasteID != "abc123" {
		t.Errorf("detail.pasteID = %q", model.detail.pasteID)
	}
	if cmd == nil {
		t.Error("cmd should not be nil when opening detail")
	}
}

func TestDelegateUpdateListingEnterInSearchMode(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.list.items = []PasteListItem{{ID: "abc123"}}
	m.list.filtered = m.list.items
	m.list.loading = false
	m.list.searching = true

	newM, _ := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model := newM.(Model)

	// In search mode, Enter should confirm search, not open detail
	if model.state != stateListing {
		t.Errorf("state = %v, want stateListing (search confirmed)", model.state)
	}
}

func TestDelegateUpdateDetail(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail
	// Initialize detail properly with newDetailModel to avoid nil pointer
	m.detail, _ = newDetailModel("https://example.com", "en", "test123", 80, 24)
	m.detail.loading = false

	newM, _ := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	model := newM.(Model)

	if !model.detail.deleting {
		t.Error("d key should enter delete mode")
	}
}

func TestDelegateUpdateHelp(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp

	// Help overlay doesn't process messages
	newM, cmd := m.delegateUpdate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	model := newM.(Model)

	if model.state != stateHelp {
		t.Errorf("state = %v, want stateHelp", model.state)
	}
	if cmd != nil {
		t.Error("cmd should be nil for help state")
	}
}

func TestModelViewSetup(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	m.state = stateSetup
	m.width = 80
	m.height = 24

	view := m.View()
	if !strings.Contains(view, "cairnbin-cli setup") {
		t.Error("setup view should contain setup title")
	}
}

func TestModelViewListing(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateListing
	m.width = 80
	m.height = 24
	m.list.loading = false
	m.list.items = []PasteListItem{{ID: "abc123"}}
	m.list.filtered = m.list.items

	view := m.View()
	if !strings.Contains(view, "cairnbin") {
		t.Error("listing view should contain cairnbin title")
	}
}

func TestModelViewDetail(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateDetail
	m.width = 80
	m.height = 24
	m.detail.pasteID = "test123"
	m.detail.loading = false
	m.detail.vpReady = true

	view := m.View()
	if !strings.Contains(view, "Paste:") {
		t.Error("detail view should contain Paste header")
	}
}

func TestModelViewHelp(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp
	m.prevState = stateListing
	m.width = 80
	m.height = 24
	m.list.loading = false

	view := m.View()
	if !strings.Contains(view, "Keyboard shortcuts") {
		t.Error("help view should contain Keyboard shortcuts")
	}
}

func TestModelViewUnknownStateReturnsEmpty(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	// Invalid state
	m.state = state(99)

	view := m.View()
	if view != "" {
		t.Errorf("unknown state should return empty view, got %q", view)
	}
}

func TestViewSetupCentersContent(t *testing.T) {
	cfg := ClientConfig{
		Server:  "",
		SaveURL: func(s string) error { return nil },
	}
	m := newModel(cfg)
	m.state = stateSetup
	m.width = 80
	m.height = 30

	view := m.viewSetup()
	lines := strings.Split(view, "\n")
	// First lines should be empty for vertical centering
	emptyCount := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			emptyCount++
		} else {
			break
		}
	}
	if emptyCount == 0 {
		t.Error("viewSetup should have leading newlines for centering")
	}
}

func TestViewHelpOverlayFromListing(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp
	m.prevState = stateListing
	m.width = 80
	m.height = 24
	m.list.loading = false

	view := m.viewHelpOverlay()
	// Should contain both help content and background from listing
	if !strings.Contains(view, "Keyboard shortcuts") {
		t.Error("overlay should contain help content")
	}
}

func TestViewHelpOverlayFromDetail(t *testing.T) {
	cfg := ClientConfig{Server: "https://example.com"}
	m := newModel(cfg)
	m.state = stateHelp
	m.prevState = stateDetail
	m.width = 80
	m.height = 24
	m.detail.loading = false
	m.detail.vpReady = true

	view := m.viewHelpOverlay()
	if !strings.Contains(view, "Keyboard shortcuts") {
		t.Error("overlay should contain help content")
	}
}

func TestStateConstants(t *testing.T) {
	// Verify state constants are distinct
	states := []state{stateSetup, stateListing, stateDetail, stateHelp}
	seen := make(map[state]bool)
	for _, s := range states {
		if seen[s] {
			t.Errorf("duplicate state value: %v", s)
		}
		seen[s] = true
	}
}

func TestClientConfigFields(t *testing.T) {
	cfg := ClientConfig{
		Server:  "https://test.com",
		Lang:    "en-US",
		SaveURL: func(s string) error { return nil },
		CfgPath: "/home/user/.config/cli.yml",
	}

	if cfg.Server != "https://test.com" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.Lang != "en-US" {
		t.Errorf("Lang = %q", cfg.Lang)
	}
	if cfg.CfgPath != "/home/user/.config/cli.yml" {
		t.Errorf("CfgPath = %q", cfg.CfgPath)
	}
	if cfg.SaveURL == nil {
		t.Error("SaveURL should not be nil")
	}
}
