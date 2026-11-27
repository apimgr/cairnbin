//go:build gui && !freebsd && !netbsd && !openbsd

package gui

import (
	"fmt"
	"strings"

	"github.com/gogpu/ui/core/button"
	"github.com/gogpu/ui/core/checkbox"
	"github.com/gogpu/ui/core/scrollview"
	"github.com/gogpu/ui/core/textfield"
	"github.com/gogpu/ui/primitives"
	"github.com/gogpu/ui/widget"

	"github.com/apimgr/cairnbin/src/client/setup"
)

// errColor is the text color used for inline error messages on every
// screen, matching the accent previously drawn from the Gio theme's
// ContrastBg palette entry.
var errColor = widget.RGBA8(220, 68, 68, 255)

// showSetup builds and installs the first-run "enter server URL" screen.
func (s *guiState) showSetup() {
	s.serverField = textfield.New(
		textfield.Placeholder(t("setup_server_url_label")),
		textfield.InitialValue(s.config.Server),
	)
	s.tokenField = textfield.New(
		textfield.Placeholder(t("setup_token_label")),
		textfield.InitialValue(s.config.Token),
		textfield.InputTypeOpt(textfield.TypePassword),
	)

	testBtn := button.New(
		button.TextOpt(t("setup_test_connection")),
		button.OnClick(func() {
			s.testSetupConnection()
		}),
	)
	connectBtn := button.New(
		button.TextOpt(t("setup_connect")),
		button.OnClick(func() { s.trySetupConnect() }),
	)
	cancelBtn := button.New(
		button.TextOpt(t("setup_cancel")),
		button.OnClick(func() {
			if s.close != nil {
				s.close()
			}
		}),
	)
	options := []widget.Widget{
		checkbox.New(checkbox.Label(t("setup_save_config")), checkbox.Checked(s.saveConfig), checkbox.OnToggle(func(value bool) { s.saveConfig = value })),
		checkbox.New(checkbox.Label(t("setup_set_default")), checkbox.Checked(s.setDefault), checkbox.OnToggle(func(value bool) { s.setDefault = value })),
	}
	if s.config.SetAutostart != nil {
		options = append(options, checkbox.New(checkbox.Label(t("setup_start_on_login")), checkbox.Checked(s.startOnLogin), checkbox.OnToggle(func(value bool) { s.startOnLogin = value })))
	}

	children := []widget.Widget{
		primitives.Text(t("setup_title")).FontSize(28).Bold(),
		primitives.Text(t("setup_hint")).FontSize(14),
		s.serverField,
		s.tokenField,
		primitives.VBox(options...).Gap(4),
		testBtn,
		primitives.HBox(cancelBtn, connectBtn).Gap(8),
	}
	if s.setupInfo.Name != "" || s.setupInfo.Version != "" {
		children = append(children, primitives.Text(tf("setup_server_info", "name", s.setupInfo.Name, "version", s.setupInfo.Version)))
	}
	if s.setupInfo.TokenKind != "" {
		children = append(children, primitives.Text(tf("setup_token_kind", "kind", s.setupInfo.TokenKind)))
	}
	if s.setupErr != "" {
		children = append(children, primitives.Text(s.setupErr).Color(errColor))
	}

	s.app.SetRoot(primitives.Box(primitives.VBox(children...).Gap(12)).Padding(24))
}

// testSetupConnection validates the connection without changing configuration.
func (s *guiState) testSetupConnection() {
	server := strings.TrimRight(strings.TrimSpace(s.serverField.Text()), "/")
	token := strings.TrimSpace(s.tokenField.Text())
	if server == "" {
		s.setupErr = t("setup_url_invalid")
		s.showSetup()
		return
	}
	if s.config.TestSetup == nil {
		s.setupErr = t("setup_test_unavailable")
		s.showSetup()
		return
	}
	s.config.Server = server
	s.config.Token = token
	info, err := s.config.TestSetup(server, token)
	if err != nil {
		s.setupErr = tf("setup_test_failed", "error", err.Error())
		s.showSetup()
		return
	}
	s.setupInfo = info
	s.setupErr = ""
	s.showSetup()
}

// trySetupConnect validates and saves the entered server URL, then advances
// to the list screen and kicks off the first paste fetch.
func (s *guiState) trySetupConnect() {
	url := strings.TrimRight(strings.TrimSpace(s.serverField.Text()), "/")
	token := strings.TrimSpace(s.tokenField.Text())
	if url == "" {
		s.setupErr = t("setup_url_invalid")
		s.showSetup()
		return
	}
	s.config.Server = url
	s.config.Token = token
	if s.config.TestSetup == nil {
		s.setupErr = t("setup_test_unavailable")
		s.showSetup()
		return
	}
	info, err := s.config.TestSetup(url, token)
	if err != nil {
		s.setupErr = tf("setup_test_failed", "error", err.Error())
		s.showSetup()
		return
	}
	options := setup.Options{Server: url, Token: token, SaveToConfig: s.saveConfig, SetDefault: s.setDefault, StartOnLogin: s.startOnLogin}
	if s.config.SaveSetup != nil {
		if err := s.config.SaveSetup(options); err != nil {
			s.setupErr = tf("setup_save_failed", "error", err.Error())
			s.showSetup()
			return
		}
	} else if s.config.SaveURL != nil && options.SaveToConfig {
		if err := s.config.SaveURL(url); err != nil {
			s.setupErr = tf("setup_save_failed", "error", err.Error())
			s.showSetup()
			return
		}
	}
	if s.config.SetAutostart != nil && options.StartOnLogin {
		if err := s.config.SetAutostart(true); err != nil {
			s.setupErr = tf("setup_autostart_failed", "error", err.Error())
			s.showSetup()
			return
		}
	}
	s.config.Server = url
	s.config.Token = token
	s.setupInfo = info
	s.setupErr = ""
	s.showList()
	s.startRefresh()
}

// showList builds and installs the paste list screen.
func (s *guiState) showList() {
	refreshBtn := button.New(
		button.TextOpt(t("list_refresh")),
		button.OnClick(func() {
			s.startRefresh()
		}),
	)

	header := primitives.HBox(
		primitives.Text(t("list_title")).FontSize(22).Bold(),
		primitives.Expanded(primitives.Box()),
		refreshBtn,
	).Gap(8)

	children := []widget.Widget{header}
	if s.listErr != "" {
		children = append(children, primitives.Text(s.listErr).Color(errColor))
	}

	if len(s.pastes) == 0 {
		children = append(children, primitives.Text(t("list_empty")))
	} else {
		rows := make([]widget.Widget, 0, len(s.pastes))
		for _, p := range s.pastes {
			id := p.ID
			label := fmt.Sprintf("%s   %s", p.ID, p.CreatedAt.Format("2006-01-02 15:04"))
			rows = append(rows, button.New(
				button.TextOpt(label),
				button.VariantOpt(button.TextOnly),
				button.OnClick(func() {
					s.openPaste(id)
				}),
			))
		}
		children = append(children, primitives.Expanded(scrollview.New(primitives.VBox(rows...).Gap(4))))
	}

	s.app.SetRoot(primitives.Box(primitives.VBox(children...).Gap(12)).Padding(16))
}

// startRefresh loads the paste list, sized to match the current server/lang,
// then re-renders the list screen with the fetched data or an error.
func (s *guiState) startRefresh() {
	s.listErr = ""
	pastes, err := fetchPastesWithToken(s.config.Server, s.config.Lang, s.config.Token, listPage, listPageSize)
	if err != nil {
		s.listErr = tf("list_error", "error", err.Error())
		s.showList()
		return
	}
	s.pastes = pastes
	s.showList()
}

// openPaste fetches a single paste's raw content and switches to the detail
// screen.
func (s *guiState) openPaste(id string) {
	body, err := fetchPasteRawWithToken(s.config.Server, s.config.Lang, s.config.Token, id)
	if err != nil {
		s.listErr = tf("list_error", "error", err.Error())
		s.showList()
		return
	}
	s.selectedID = id
	s.selectedBody = body
	s.detailErr = ""
	s.deleting = false
	s.deleteField = nil
	s.showDetail()
}

// showDetail builds and installs the single-paste detail screen with
// back/delete actions. Delete is a two-step flow: the delete button reveals
// a token entry field (mirroring the TUI's deleting/deleteInput pattern in
// tui/detail.go), and a second confirm button submits the token to
// deletePaste.
func (s *guiState) showDetail() {
	backBtn := button.New(
		button.TextOpt(t("detail_back")),
		button.OnClick(func() {
			s.showList()
		}),
	)
	deleteBtn := button.New(
		button.TextOpt(t("detail_delete")),
		button.OnClick(func() {
			s.deleting = true
			s.detailErr = ""
			s.deleteField = textfield.New(
				textfield.Placeholder(t("detail_delete_token_placeholder")),
			)
			s.showDetail()
		}),
	)

	header := primitives.HBox(
		primitives.Text(tf("detail_title", "id", s.selectedID)).FontSize(22).Bold(),
		primitives.Expanded(primitives.Box()),
		backBtn,
		deleteBtn,
	).Gap(8)

	children := []widget.Widget{header}

	if s.deleting {
		confirmBtn := button.New(
			button.TextOpt(t("detail_delete_confirm")),
			button.OnClick(func() {
				s.tryDelete()
			}),
		)
		children = append(children, primitives.HBox(
			primitives.Expanded(s.deleteField),
			confirmBtn,
		).Gap(8))
	}

	if s.detailErr != "" {
		children = append(children, primitives.Text(s.detailErr).Color(errColor))
	}

	children = append(children, primitives.Expanded(scrollview.New(primitives.Text(s.selectedBody))))

	s.app.SetRoot(primitives.Box(primitives.VBox(children...).Gap(12)).Padding(16))
}

// tryDelete validates the entered token, removes the selected paste, and
// returns to the list screen on success.
func (s *guiState) tryDelete() {
	token := s.deleteField.Text()
	if token == "" {
		s.detailErr = t("detail_token_required")
		s.showDetail()
		return
	}
	id := s.selectedID
	if err := deletePaste(s.config.Server, s.config.Lang, id, token); err != nil {
		s.detailErr = tf("detail_error", "error", err.Error())
		s.showDetail()
		return
	}
	s.deleting = false
	// startRefresh resets listErr immediately, mirroring the original Gio
	// screen's behavior where the success message was likewise cleared by
	// the refresh it triggers.
	s.listErr = tf("detail_delete_success", "id", id)
	s.startRefresh()
}
