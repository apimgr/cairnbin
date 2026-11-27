package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// detailModel is the sub-model for the paste detail view.
type detailModel struct {
	pasteID       string
	content       string
	loading       bool
	err           error
	vp            viewport.Model
	vpReady       bool
	deleting      bool
	deleteInput   textinput.Model
	deleteErr     string
	deleteSuccess string
	server        string
	lang          string
	token         string
}

// pasteRawMsg carries the result of a fetchPasteRaw call.
type pasteRawMsg struct {
	content string
	err     error
}

// pasteDeletedMsg signals a successful delete.
type pasteDeletedMsg struct{ id string }

// pasteDeleteErrMsg signals a failed delete.
type pasteDeleteErrMsg struct{ err error }

// newDetailModel creates a detail model for the given paste ID and kicks off the load.
func newDetailModel(server, lang, pasteID string, width, height int) (detailModel, tea.Cmd) {
	return newDetailModelWithToken(server, lang, "", pasteID, width, height)
}

// newDetailModelWithToken creates a detail model whose reads use optional API authentication.
func newDetailModelWithToken(server, lang, token, pasteID string, width, height int) (detailModel, tea.Cmd) {
	vp := viewport.New(width, height-4)
	vp.SetContent("")

	ti := textinput.New()
	ti.Placeholder = t("detail_delete_token_placeholder")
	ti.CharLimit = 128
	ti.Width = 40

	m := detailModel{
		pasteID:     pasteID,
		loading:     true,
		vp:          vp,
		vpReady:     false,
		deleteInput: ti,
		server:      server,
		lang:        lang,
		token:       token,
	}
	return m, m.loadCmd()
}

// loadCmd returns the tea.Cmd that fetches raw paste content.
func (m detailModel) loadCmd() tea.Cmd {
	server := m.server
	lang := m.lang
	token := m.token
	id := m.pasteID
	return func() tea.Msg {
		content, err := fetchPasteRawWithToken(server, lang, token, id)
		return pasteRawMsg{content: content, err: err}
	}
}

// update processes messages for the detail sub-model.
func (m detailModel) update(msg tea.Msg) (detailModel, tea.Cmd) {
	switch msg := msg.(type) {
	case pasteRawMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.content = msg.content
			m.vp.SetContent(msg.content)
			m.vpReady = true
		}
		return m, nil

	case pasteDeletedMsg:
		m.deleting = false
		m.deleteSuccess = tf("detail_delete_success", "id", msg.id)
		return m, nil

	case pasteDeleteErrMsg:
		m.deleting = false
		m.deleteErr = msg.err.Error()
		return m, nil

	case tea.KeyMsg:
		if m.deleting {
			return m.updateDelete(msg)
		}
		return m.updateNav(msg)
	}

	// Forward scroll events to the viewport.
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// updateNav handles navigation when not in delete mode.
func (m detailModel) updateNav(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		m.vp.ScrollDown(1)
	case "k", "up":
		m.vp.ScrollUp(1)
	case "d":
		m.deleting = true
		m.deleteInput.SetValue("")
		m.deleteErr = ""
		m.deleteSuccess = ""
		m.deleteInput.Focus()
	}
	return m, nil
}

// updateDelete handles key input in delete-confirm mode.
func (m detailModel) updateDelete(msg tea.KeyMsg) (detailModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.deleting = false
		m.deleteInput.Blur()
		return m, nil

	case tea.KeyEnter:
		token := strings.TrimSpace(m.deleteInput.Value())
		if token == "" {
			m.deleteErr = t("detail_token_required")
			return m, nil
		}
		server := m.server
		lang := m.lang
		id := m.pasteID
		return m, func() tea.Msg {
			if err := deletePaste(server, lang, id, token); err != nil {
				return pasteDeleteErrMsg{err: err}
			}
			return pasteDeletedMsg{id: id}
		}
	}

	var cmd tea.Cmd
	m.deleteInput, cmd = m.deleteInput.Update(msg)
	return m, cmd
}

// resize updates the viewport dimensions.
func (m *detailModel) resize(width, height int) {
	m.vp.Width = width
	m.vp.Height = height - 4
}

// view renders the detail view.
func (m detailModel) view(styles TUIStyles, width int) string {
	var sb strings.Builder

	if m.deleteSuccess != "" {
		sb.WriteString(styles.Success.Render(m.deleteSuccess) + "\n\n")
		sb.WriteString(styles.Muted.Render(t("detail_back_hint")))
		return sb.String()
	}

	header := styles.Title.Render(tf("detail_header", "id", m.pasteID)) + "  " +
		styles.Muted.Render(t("detail_keys_hint"))
	sb.WriteString(header + "\n")
	sb.WriteString(styles.Muted.Render(strings.Repeat("─", min(width-2, 80))) + "\n")

	if m.loading {
		sb.WriteString(styles.Muted.Render(t("detail_loading")))
		return sb.String()
	}

	if m.err != nil {
		sb.WriteString(styles.Error.Render(tf("list_error", "error", m.err.Error())))
		return sb.String()
	}

	sb.WriteString(m.vp.View())

	if m.deleting {
		sb.WriteString("\n\n")
		sb.WriteString(styles.Warning.Render(t("detail_delete_prompt")) + "\n")
		sb.WriteString(m.deleteInput.View())
		if m.deleteErr != "" {
			sb.WriteString("\n" + styles.Error.Render(m.deleteErr))
		}
	}

	return sb.String()
}
