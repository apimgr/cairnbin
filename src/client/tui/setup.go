package tui

import (
	"fmt"
	"strings"

	"github.com/apimgr/cairnbin/src/client/setup"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// setupModel is the sub-model for the shared first-run connection wizard.
type setupModel struct {
	input          textinput.Model
	tokenInput     textinput.Model
	err            string
	info           SetupServerInfo
	focus          int
	saveToConfig   bool
	setDefault     bool
	testConnection func(string, string) (SetupServerInfo, error)
	saveSetup      func(SetupOptions) error
	saveURL        func(string) error
}

// SetupOptions contains the connection settings collected by the first-run wizard.
type SetupOptions = setup.Options

// SetupServerInfo contains the public details returned by a successful connection test.
type SetupServerInfo = setup.ServerInfo

// serverURLSavedMsg is sent after the server connection has been validated and saved.
type serverURLSavedMsg struct {
	url     string
	options SetupOptions
	info    SetupServerInfo
}

// serverURLErrorMsg is sent when saving the server connection fails.
type serverURLErrorMsg struct{ err error }

// setupTestErrorMsg is sent when the connection or API token check fails.
type setupTestErrorMsg struct{ err error }

// serverSetupTestedMsg carries successful server information without saving settings.
type serverSetupTestedMsg struct{ info SetupServerInfo }

// newSetupModel creates the legacy URL-only setup model used by existing callers.
func newSetupModel(saveURL func(string) error) setupModel {
	ti := textinput.New()
	ti.Placeholder = "https://paste.example.com"
	ti.Focus()
	ti.CharLimit = 512
	ti.Width = 44
	token := textinput.New()
	token.Placeholder = t("setup_token_placeholder")
	token.CharLimit = 512
	token.Width = 44
	token.EchoMode = textinput.EchoPassword
	token.EchoCharacter = '•'
	return setupModel{input: ti, tokenInput: token, saveURL: saveURL, saveToConfig: true}
}

// newSetupWizard creates the complete first-run flow shared with the GUI.
func newSetupWizard(server, token string, saveToConfig, setDefault bool, test func(string, string) (SetupServerInfo, error), save func(SetupOptions) error) setupModel {
	m := newSetupModel(nil)
	m.input.SetValue(server)
	m.tokenInput.SetValue(token)
	m.saveToConfig = saveToConfig
	m.setDefault = setDefault
	m.testConnection = test
	m.saveSetup = save
	return m
}

// update processes key events for the setup wizard.
func (s setupModel) update(msg tea.Msg) (setupModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.Type {
		case tea.KeyTab:
			s.focus = (s.focus + 1) % 2
			if s.focus == 0 {
				s.input.Focus()
				s.tokenInput.Blur()
			} else {
				s.input.Blur()
				s.tokenInput.Focus()
			}
			return s, nil
		case tea.KeyCtrlS:
			s.saveToConfig = !s.saveToConfig
			return s, nil
		case tea.KeyCtrlD:
			s.setDefault = !s.setDefault
			return s, nil
		case tea.KeyCtrlT:
			server := strings.TrimRight(strings.TrimSpace(s.input.Value()), "/")
			if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
				s.err = t("setup_url_invalid")
				return s, nil
			}
			if s.testConnection == nil {
				s.err = t("setup_test_unavailable")
				return s, nil
			}
			testFn := s.testConnection
			token := strings.TrimSpace(s.tokenInput.Value())
			return s, func() tea.Msg {
				info, err := testFn(server, token)
				if err != nil {
					return setupTestErrorMsg{err: err}
				}
				return serverSetupTestedMsg{info: info}
			}
		case tea.KeyEnter:
			rawURL := strings.TrimSpace(s.input.Value())
			if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
				s.err = t("setup_url_invalid")
				return s, nil
			}
			s.err = ""
			options := SetupOptions{
				Server:       strings.TrimRight(rawURL, "/"),
				Token:        strings.TrimSpace(s.tokenInput.Value()),
				SaveToConfig: s.saveToConfig,
				SetDefault:   s.setDefault,
			}
			testFn := s.testConnection
			saveSetup := s.saveSetup
			saveURL := s.saveURL
			return s, func() tea.Msg {
				var info SetupServerInfo
				if testFn != nil {
					var err error
					info, err = testFn(options.Server, options.Token)
					if err != nil {
						return setupTestErrorMsg{err: err}
					}
				}
				if saveSetup != nil {
					if err := saveSetup(options); err != nil {
						return serverURLErrorMsg{err: err}
					}
				} else if saveURL != nil {
					if err := saveURL(options.Server); err != nil {
						return serverURLErrorMsg{err: err}
					}
				}
				return serverURLSavedMsg{url: options.Server, options: options, info: info}
			}
		}
	}
	var cmd tea.Cmd
	if s.focus == 0 {
		s.input, cmd = s.input.Update(msg)
	} else {
		s.tokenInput, cmd = s.tokenInput.Update(msg)
	}
	return s, cmd
}

// view renders the setup wizard.
func (s setupModel) view(styles TUIStyles) string {
	title := styles.Title.Render(t("setup_title"))
	serverPrompt := styles.Normal.Render(t("setup_server_url_label"))
	tokenPrompt := styles.Normal.Render(t("setup_token_label"))
	saveOption := "[ ] " + t("setup_save_config")
	if s.saveToConfig {
		saveOption = "[x] " + t("setup_save_config")
	}
	defaultOption := "[ ] " + t("setup_set_default")
	if s.setDefault {
		defaultOption = "[x] " + t("setup_set_default")
	}
	options := styles.Normal.Render(saveOption + "  (Ctrl+S)\n" + defaultOption + "  (Ctrl+D)")
	testButton := styles.Title.Render("[Ctrl+T] " + t("setup_test_connection") + "  [Enter] " + t("setup_connect"))
	errLine := ""
	if s.err != "" {
		errLine = "\n" + styles.Error.Render(s.err)
	}
	infoLine := ""
	if s.info.Name != "" || s.info.Version != "" {
		infoLine = "\n" + styles.Muted.Render(tf("setup_server_info", "name", s.info.Name, "version", s.info.Version))
	}
	if s.info.TokenKind != "" {
		infoLine += "\n" + styles.Muted.Render(tf("setup_token_kind", "kind", s.info.TokenKind))
	}
	hint := styles.Muted.Render(t("setup_hint"))
	inner := fmt.Sprintf("%s\n\n%s\n> %s\n\n%s\n> %s\n\n%s\n\n%s%s%s\n\n%s",
		title, serverPrompt, s.input.View(), tokenPrompt, s.tokenInput.View(), options, testButton, errLine, infoLine, hint)
	return styles.Border.Width(52).Padding(1, 2).Render(inner)
}
