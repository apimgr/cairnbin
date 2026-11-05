package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Tests for setup.go: setupModel, newSetupModel, update, view

func TestNewSetupModelInitializesInput(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)

	if m.input.Placeholder != "https://paste.example.com" {
		t.Errorf("Placeholder = %q, want %q", m.input.Placeholder, "https://paste.example.com")
	}
	if m.input.CharLimit != 512 {
		t.Errorf("CharLimit = %d, want 512", m.input.CharLimit)
	}
	if m.err != "" {
		t.Errorf("err should be empty, got %q", m.err)
	}
	if m.saveURL == nil {
		t.Error("saveURL should not be nil")
	}
}

func TestSetupModelUpdateEnterWithInvalidURL(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)
	m.input.SetValue("invalid-url")

	updated, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.err == "" {
		t.Error("err should be set for invalid URL")
	}
	if !strings.Contains(updated.err, "http://") {
		t.Errorf("err = %q, should mention http://", updated.err)
	}
	if cmd != nil {
		t.Error("cmd should be nil for invalid URL")
	}
}

func TestSetupModelUpdateEnterWithValidHTTPURL(t *testing.T) {
	savedURL := ""
	saveFn := func(s string) error {
		savedURL = s
		return nil
	}
	m := newSetupModel(saveFn)
	m.input.SetValue("http://example.com")

	updated, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.err != "" {
		t.Errorf("err = %q, want empty", updated.err)
	}
	if cmd == nil {
		t.Fatal("cmd should not be nil for valid URL")
	}

	// Execute the command to trigger the save
	msg := cmd()
	if _, ok := msg.(serverURLSavedMsg); !ok {
		t.Errorf("expected serverURLSavedMsg, got %T", msg)
	}
	if savedURL != "http://example.com" {
		t.Errorf("savedURL = %q, want %q", savedURL, "http://example.com")
	}
}

func TestSetupModelUpdateEnterWithValidHTTPSURL(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)
	m.input.SetValue("https://secure.example.com")

	updated, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.err != "" {
		t.Errorf("err = %q, want empty", updated.err)
	}
	if cmd == nil {
		t.Error("cmd should not be nil for valid HTTPS URL")
	}
}

func TestSetupModelUpdateEnterWithSaveError(t *testing.T) {
	saveFn := func(s string) error { return errors.New("disk full") }
	m := newSetupModel(saveFn)
	m.input.SetValue("https://example.com")

	_, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("cmd should not be nil")
	}

	msg := cmd()
	errMsg, ok := msg.(serverURLErrorMsg)
	if !ok {
		t.Fatalf("expected serverURLErrorMsg, got %T", msg)
	}
	if errMsg.err == nil {
		t.Error("errMsg.err should not be nil")
	}
	if errMsg.err.Error() != "disk full" {
		t.Errorf("errMsg.err = %v, want 'disk full'", errMsg.err)
	}
}

func TestSetupModelUpdateTrimsWhitespace(t *testing.T) {
	savedURL := ""
	saveFn := func(s string) error {
		savedURL = s
		return nil
	}
	m := newSetupModel(saveFn)
	m.input.SetValue("  https://example.com  ")

	_, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("cmd should not be nil")
	}

	cmd()
	if savedURL != "https://example.com" {
		t.Errorf("savedURL = %q, should be trimmed", savedURL)
	}
}

func TestSetupModelUpdateDelegatesOtherKeys(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)

	// Type a character
	updated, _ := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !strings.Contains(updated.input.Value(), "a") {
		t.Error("typing 'a' should add it to input")
	}
}

func TestSetupModelViewRendersComponents(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)
	styles := StylesFromTerminalPalette(DarkTheme())

	view := m.view(styles)
	if !strings.Contains(view, "cairnbin-cli setup") {
		t.Error("view should contain title")
	}
	if !strings.Contains(view, "Server URL") {
		t.Error("view should contain prompt")
	}
	if !strings.Contains(view, "Enter") {
		t.Error("view should contain hint")
	}
}

func TestSetupModelViewShowsError(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)
	m.err = "test error message"
	styles := StylesFromTerminalPalette(DarkTheme())

	view := m.view(styles)
	if !strings.Contains(view, "test error message") {
		t.Error("view should contain error message")
	}
}

func TestSetupModelViewNoErrorWhenEmpty(t *testing.T) {
	saveFn := func(s string) error { return nil }
	m := newSetupModel(saveFn)
	m.err = ""
	styles := StylesFromTerminalPalette(DarkTheme())

	view := m.view(styles)
	// The view should not have an error line when err is empty
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		// Error lines would typically contain style-rendered error text
		// which would show up differently, but at minimum the raw error shouldn't appear
		if strings.Contains(line, "Error:") && m.err == "" {
			t.Error("view should not contain error line when err is empty")
		}
	}
}

func TestServerURLSavedMsgFields(t *testing.T) {
	msg := serverURLSavedMsg{url: "https://test.com"}
	if msg.url != "https://test.com" {
		t.Errorf("url = %q, want %q", msg.url, "https://test.com")
	}
}

func TestServerURLErrorMsgFields(t *testing.T) {
	err := errors.New("test error")
	msg := serverURLErrorMsg{err: err}
	if msg.err != err {
		t.Errorf("err = %v, want %v", msg.err, err)
	}
}
